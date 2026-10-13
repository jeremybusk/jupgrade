package upgrade

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Real builds deliberately remain opt-in. CI supplies each target JDK and the
// pinned Gradle runtime; the same test can be run on a developer machine.
func TestGradleBuildMatrixIntegration(t *testing.T) {
	if os.Getenv("JUPGRADE_GRADLE_INTEGRATION") != "1" {
		t.Skip("set JUPGRADE_GRADLE_INTEGRATION=1 to execute Gradle")
	}
	java := 21
	if value := os.Getenv("JUPGRADE_TEST_JAVA"); value != "" {
		var err error
		java, err = strconv.Atoi(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	policy, err := PolicyForJava(java)
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.AddPreset("common"); err != nil {
		t.Fatal(err)
	}
	policy.Gradle = gradleTarget(java)
	for _, fixture := range []string{"gradle-groovy", "gradle-kotlin"} {
		for _, wrapper := range []bool{false, true} {
			name := fixture + "/installed"
			if wrapper {
				name = fixture + "/wrapper"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				originals := copyIntegrationFixture(t, fixture, root)
				var log bytes.Buffer
				options := VerifyOptions{JavaHome: os.Getenv("JUPGRADE_JAVA_HOME"), GradleCommand: os.Getenv("JUPGRADE_GRADLE_COMMAND"), GradleUserHome: os.Getenv("JUPGRADE_GRADLE_USER_HOME"), Offline: os.Getenv("JUPGRADE_OFFLINE") == "1", Timeout: 5 * time.Minute, Stdout: &log, Stderr: &log}
				defer saveIntegrationLog(t, name, &log)
				selected := policy
				if wrapper {
					// Generate infrastructure in a clean build: a target Gradle
					// runtime may not evaluate the repository's legacy DSL yet.
					wrapperRoot := t.TempDir()
					writeTestFile(t, wrapperRoot, "settings.gradle", `rootProject.name = 'wrapper-generator'`)
					executable := options.GradleCommand
					if executable == "" {
						executable = "gradle"
					}
					env, err := workerEnvironment(options)
					if err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), options.Timeout)
					defer cancel()
					args := []string{"--no-daemon", "--console", "plain", "wrapper", "--gradle-version", policy.Gradle, "--distribution-type", "bin"}
					if options.Offline {
						args = append(args, "--offline", "--no-validate-url")
					}
					command := exec.CommandContext(ctx, executable, args...)
					command.Dir, command.Env, command.Stdout, command.Stderr = wrapperRoot, env, &log, &log
					if err := command.Run(); err != nil {
						t.Fatalf("generate real wrapper: %v\n%s", err, log.String())
					}
					for _, path := range []string{"gradlew", "gradlew.bat", "gradle/wrapper/gradle-wrapper.jar", gradleWrapperProperties} {
						data, err := os.ReadFile(filepath.Join(wrapperRoot, path))
						if err != nil {
							t.Fatal(err)
						}
						info, err := os.Stat(filepath.Join(wrapperRoot, path))
						if err != nil {
							t.Fatal(err)
						}
						destination := filepath.Join(root, path)
						if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(destination, data, info.Mode().Perm()); err != nil {
							t.Fatal(err)
						}
					}
					props := readTestFile(t, root, gradleWrapperProperties)
					checksum, _, _, err := wrapperProperty([]byte(props), "distributionSha256Sum")
					if err != nil {
						t.Fatal(err)
					}
					selected.GradleSHA256 = checksum
					older := "8.13"
					if java == 25 {
						older = "9.0.0"
					}
					writeTestFile(t, root, gradleWrapperProperties, strings.ReplaceAll(props, "gradle-"+policy.Gradle+"-bin.zip", "gradle-"+older+"-bin.zip"))
					for _, path := range []string{"gradlew", "gradlew.bat", "gradle/wrapper/gradle-wrapper.jar", gradleWrapperProperties} {
						originals[path] = readTestFile(t, root, path)
					}
					options.GradleCommand = "" // Verify must execute the upgraded wrapper.
				}
				p := testPlan(t, root, selected)
				if p.HasBlockers() {
					t.Fatal(p.Findings)
				}
				if wrapper {
					upgraded := false
					for _, file := range p.Files {
						if file.Path == gradleWrapperProperties {
							upgraded = gradleWrapperVersion([]byte(file.After)) == policy.Gradle && file.Before != file.After
						}
					}
					if !upgraded {
						t.Fatal("wrapper fixture did not exercise a distribution upgrade")
					}
				}
				if again := testPlan(t, root, selected); !bytes.Equal(planJSON(t, p), planJSON(t, again)) {
					t.Fatal("planning did not produce identical plans")
				}
				planPath := filepath.Join(t.TempDir(), "plan.json")
				if err := WritePlan(planPath, p); err != nil {
					t.Fatal(err)
				}
				p, err := ReadPlan(planPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := Apply(p, false); err != nil {
					t.Fatal(err)
				}
				if err := Apply(p, false); err != nil {
					t.Fatal(err)
				}
				if err := Verify(context.Background(), p, options); err != nil {
					t.Fatalf("%v\n%s", err, log.String())
				}
				model, err := DiscoverBuildModel(context.Background(), root, options)
				if err != nil {
					t.Fatalf("effective Gradle model: %v\n%s", err, log.String())
				}
				inspection, err := InspectBuildModel(root, model)
				if err != nil {
					t.Fatal(err)
				}
				foundJUnit := false
				for _, artifact := range inspection.Artifacts {
					if artifact.Coordinate == "junit:junit" && artifact.Resolved == "4.13.2" {
						foundJUnit = true
					}
				}
				if !foundJUnit {
					t.Fatal("effective Gradle model omitted selected JUnit version")
				}
				module := root
				if fixture == "gradle-kotlin" {
					module = filepath.Join(root, "app")
				}
				class, err := os.ReadFile(filepath.Join(module, "build/classes/java/main/example/Greeting.class"))
				if err != nil {
					t.Fatal(err)
				}
				if len(class) < 8 || binary.BigEndian.Uint16(class[6:8]) != uint16(java+44) {
					t.Fatalf("expected Java %d bytecode", java)
				}
				report, err := os.ReadFile(filepath.Join(module, "build/test-results/test/TEST-example.SmokeTest.xml"))
				if err != nil {
					t.Fatal(err)
				}
				var suite struct {
					Tests    int `xml:"tests,attr"`
					Failures int `xml:"failures,attr"`
					Errors   int `xml:"errors,attr"`
					Skipped  int `xml:"skipped,attr"`
				}
				if err := xml.Unmarshal(report, &suite); err != nil || suite.Tests != 1 || suite.Failures != 0 || suite.Errors != 0 || suite.Skipped != 0 {
					t.Fatalf("JUnit did not execute successfully: %s (%v)", report, err)
				}
				if next := testPlan(t, root, p.Policy); len(next.Files) != 0 || next.HasBlockers() {
					t.Fatal("build did not converge", next.Findings)
				}
				if err := Apply(p, true); err != nil {
					t.Fatal(err)
				}
				for path, before := range originals {
					if readTestFile(t, root, path) != before {
						t.Fatal("rollback changed original", path)
					}
				}
			})
		}
	}
}

func copyIntegrationFixture(t *testing.T, name, root string) map[string]string {
	t.Helper()
	fixture := filepath.Join("..", "..", "testdata", name)
	originals := map[string]string{}
	err := filepath.WalkDir(fixture, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(fixture, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		originals[relative] = string(data)
		writeTestFile(t, root, relative, string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return originals
}

func saveIntegrationLog(t *testing.T, name string, log *bytes.Buffer) {
	t.Helper()
	if directory := os.Getenv("JUPGRADE_TEST_REPORTS"); directory != "" {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Error(err)
			return
		}
		if err := os.WriteFile(filepath.Join(directory, strings.ReplaceAll(name, "/", "-")+".log"), log.Bytes(), 0600); err != nil {
			t.Error(err)
		}
	}
}

func planJSON(t *testing.T, p Plan) []byte {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
