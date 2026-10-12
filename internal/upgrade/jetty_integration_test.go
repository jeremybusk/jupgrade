package upgrade

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This isolates the failing Jetty annotation-scanning code path using cached
// Jetty 9.4.48 and ASM 9.3/9.7. It does not claim to test a downloaded Jetty
// 9.4.58 Maven plugin; that requires its own artifacts and startup smoke test.
func TestJettyJava21AnnotationScanningIntegration(t *testing.T) {
	if os.Getenv("JUPGRADE_JETTY_PARSER_INTEGRATION") != "1" {
		t.Skip("set JUPGRADE_JETTY_PARSER_INTEGRATION=1 to execute the cached Jetty scanner regression")
	}
	repo := os.Getenv("JUPGRADE_MAVEN_REPO")
	if repo == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		repo = filepath.Join(home, ".m2", "repository")
	}
	artifact := func(group, name, version string) string {
		path := filepath.Join(repo, filepath.FromSlash(strings.ReplaceAll(group, ".", "/")), name, version, name+"-"+version+".jar")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("required cached artifact %s:%s:%s: %v", group, name, version, err)
		}
		return path
	}
	provided := []string{artifact("javax.servlet", "javax.servlet-api", "3.1.0"), artifact("javax.websocket", "javax.websocket-api", "1.1"), artifact("javax.annotation", "javax.annotation-api", "1.3.2"), artifact("org.slf4j", "slf4j-api", "1.7.36")}
	var jetty []string
	err := filepath.WalkDir(filepath.Join(repo, "org", "eclipse", "jetty"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		artifactName := filepath.Base(filepath.Dir(filepath.Dir(path)))
		if artifactName == "apache-jsp" || artifactName == "apache-jstl" || artifactName == "jetty-maven-plugin" {
			return nil
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".jar") && filepath.Base(filepath.Dir(path)) == "9.4.48.v20220622" {
			jetty = append(jetty, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jetty) == 0 {
		t.Fatal("cached Jetty 9.4.48 runtime is required")
	}
	root := t.TempDir()
	fixture := filepath.Join("..", "..", "testdata", "jetty9")
	var sources []string
	err = filepath.WalkDir(filepath.Join(fixture, "src"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(fixture, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		writeTestFile(t, root, relative, string(data))
		if strings.HasSuffix(path, ".java") {
			sources = append(sources, filepath.Join(root, relative))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	classes := filepath.Join(root, "target", "classes")
	if err := os.MkdirAll(classes, 0755); err != nil {
		t.Fatal(err)
	}
	java, javac := "java", "javac"
	if home := os.Getenv("JUPGRADE_JAVA_HOME"); home != "" {
		java = filepath.Join(home, "bin", "java")
		javac = filepath.Join(home, "bin", "javac")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	args := []string{"--release", "21", "-cp", strings.Join(provided, string(os.PathListSeparator)), "-d", classes}
	args = append(args, sources...)
	if output, err := exec.CommandContext(ctx, javac, args...).CombinedOutput(); err != nil {
		t.Fatalf("compile Java 21 fixture: %v\n%s", err, output)
	}
	probe := filepath.Join(fixture, "JettyRuntimeProbe.java")
	run := func(asmVersion string) ([]byte, error) {
		cp := append([]string{}, provided...)
		cp = append(cp, jetty...)
		for _, name := range []string{"asm", "asm-commons", "asm-tree"} {
			cp = append(cp, artifact("org.ow2.asm", name, asmVersion))
		}
		return exec.CommandContext(ctx, java, "-Dorg.eclipse.jetty.util.log.class=org.eclipse.jetty.util.log.StdErrLog", "--class-path", strings.Join(cp, string(os.PathListSeparator)), probe, root).CombinedOutput()
	}
	before, err := run("9.3")
	if err == nil || !strings.Contains(string(before), "Unsupported class file major version 65") {
		t.Fatalf("did not reproduce the reported class-scanning failure: %v\n%s", err, before)
	}
	after, err := run("9.7")
	if err != nil || !strings.Contains(string(after), "JETTY_PROBE_OK") {
		t.Fatalf("newer ASM did not restore the application context and annotated HTTP servlet: %v\n%s", err, after)
	}
}
