package upgrade

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const minimalPOM = `<project xmlns="http://maven.apache.org/POM/4.0.0"><modelVersion>4.0.0</modelVersion><groupId>internal.example</groupId><artifactId>app</artifactId><version>1.0</version><properties><maven.compiler.source>1.8</maven.compiler.source><maven.compiler.target>1.8</maven.compiler.target></properties></project>`

func writeTestFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, root, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, relative))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func testPlan(t *testing.T, root string, policy Policy) Plan {
	t.Helper()
	p, err := CreatePlan(root, policy)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlanApplyReapplyRollback(t *testing.T) {
	root := t.TempDir()
	original := strings.ReplaceAll(strings.Replace(minimalPOM, "<properties>", "<!-- keep this comment -->\n<properties>", 1), "\n", "\r\n")
	writeTestFile(t, root, "pom.xml", original)
	p := testPlan(t, root, DefaultPolicy())
	if len(p.Files) != 1 || p.HasBlockers() {
		t.Fatalf("unexpected plan: %+v", p)
	}
	if got := readTestFile(t, root, "pom.xml"); got != original {
		t.Fatal("planning edited the POM")
	}
	planPath := filepath.Join(t.TempDir(), "plan.json")
	if err := WritePlan(planPath, p); err != nil {
		t.Fatal(err)
	}
	if err := WritePlan(planPath, p); err == nil {
		t.Fatal("overwrote an existing plan")
	}
	loaded, err := ReadPlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(loaded, false); err != nil {
		t.Fatal(err)
	}
	after := readTestFile(t, root, "pom.xml")
	if !strings.Contains(after, "<!-- keep this comment -->\r\n") {
		t.Fatal("lost comment or CRLF")
	}
	if strings.Contains(strings.ReplaceAll(after, "\r\n", ""), "\n") {
		t.Fatal("inserted mixed line endings")
	}
	if err := Apply(loaded, false); err != nil {
		t.Fatalf("reapply: %v", err)
	}
	if readTestFile(t, root, "pom.xml") != after {
		t.Fatal("reapply changed the file")
	}
	if next := testPlan(t, root, DefaultPolicy()); len(next.Files) != 0 {
		t.Fatal("second plan produced changes")
	}
	if err := Apply(loaded, true); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, root, "pom.xml") != original {
		t.Fatal("rollback didn't restore exact bytes")
	}
	if err := Apply(loaded, true); err != nil {
		t.Fatalf("repeat rollback: %v", err)
	}
}

func TestSharedPropertyOnlyChangesSelectedDependency(t *testing.T) {
	before := `<project><properties><shared>1.0.0</shared></properties><dependencies><dependency><groupId>internal</groupId><artifactId>chosen</artifactId><version>${shared}</version></dependency><dependency><groupId>internal</groupId><artifactId>other</artifactId><version>${shared}</version></dependency></dependencies></project>`
	policy := DefaultPolicy()
	policy.Dependencies["internal:chosen"] = "2.0.0"
	after, _, _, err := migratePOM([]byte(before), "pom.xml", policy)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "<shared>1.0.0</shared>") || strings.Count(string(after), "<version>${shared}</version>") != 1 || !strings.Contains(string(after), "<artifactId>chosen</artifactId><version>2.0.0</version>") {
		t.Fatalf("shared property damaged: %s", after)
	}
}

func TestProfilesExecutionsNamespacedAndSelfClosing(t *testing.T) {
	before := `<m:project xmlns:m="http://maven.apache.org/POM/4.0.0"><m:properties/><m:build><m:plugins><m:plugin><m:artifactId>maven-compiler-plugin</m:artifactId><m:version>3.8.1</m:version><m:configuration><m:release>8</m:release></m:configuration><m:executions><m:execution><m:configuration><m:source>1.8</m:source></m:configuration></m:execution></m:executions></m:plugin></m:plugins></m:build><m:profiles><m:profile><m:id>legacy</m:id><m:properties><m:java.version>8</m:java.version></m:properties></m:profile></m:profiles></m:project>`
	after, _, findings, err := migratePOM([]byte(before), "pom.xml", DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Severity == "blocker" {
			t.Fatal(f)
		}
	}
	for _, want := range []string{"<m:maven.compiler.release>21</m:maven.compiler.release>", "<m:java.version>21</m:java.version>", "<m:release>21</m:release>", "<m:source>21</m:source>"} {
		if !bytes.Contains(after, []byte(want)) {
			t.Fatalf("missing %s: %s", want, after)
		}
	}
	second, _, _, err := migratePOM(after, "pom.xml", DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, second) {
		t.Fatal("namespaced POM did not converge")
	}
}

func TestNestedPluginDependenciesOffsets(t *testing.T) {
	before := `<project><build><plugins><plugin><artifactId>maven-compiler-plugin</artifactId><dependencies><dependency><groupId>internal</groupId><artifactId>worker</artifactId><version>1.0</version></dependency></dependencies><version>3.8.1</version></plugin></plugins></build></project>`
	p := DefaultPolicy()
	p.Dependencies["internal:worker"] = "123.0.0"
	after, _, _, err := migratePOM([]byte(before), "pom.xml", p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(after, []byte("<version>123.0.0</version>")) || !bytes.Contains(after, []byte("<version>3.13.0</version>")) {
		t.Fatalf("nested ranges were corrupted: %s", after)
	}
}

func TestDoesNotDowngradeDependenciesOrManagedPlugins(t *testing.T) {
	before := `<project><build><pluginManagement><plugins><plugin><artifactId>maven-compiler-plugin</artifactId><version>3.99.0</version></plugin></plugins></pluginManagement></build><dependencies><dependency><groupId>internal</groupId><artifactId>lib</artifactId><version>99.0</version></dependency></dependencies></project>`
	p := DefaultPolicy()
	p.Dependencies["internal:lib"] = "2.0"
	after, _, _, err := migratePOM([]byte(before), "pom.xml", p)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(after, []byte("<version>3.99.0</version>")) != 2 || !bytes.Contains(after, []byte("<version>99.0</version>")) {
		t.Fatalf("downgraded a visible version: %s", after)
	}
}

func TestWarUsesModernLifecyclePlugin(t *testing.T) {
	before := `<project><packaging>war</packaging></project>`
	after, _, _, err := migratePOM([]byte(before), "pom.xml", DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(after, []byte("maven-war-plugin")) || bytes.Contains(after, []byte("maven-jar-plugin")) {
		t.Fatalf("wrong lifecycle plugins: %s", after)
	}
}

func TestBlockersPreventWrites(t *testing.T) {
	cases := map[string]string{
		"unresolved": `<project><build><plugins><plugin><artifactId>maven-compiler-plugin</artifactId><version>${external.version}</version></plugin></plugins></build></project>`,
		"qualified":  `<project><build><plugins><plugin><artifactId>maven-compiler-plugin</artifactId><version>3.13.0-beta-1</version></plugin></plugins></build></project>`,
		"duplicate":  `<project><properties><java.version>8</java.version><java.version>11</java.version></properties></project>`,
		"toolchain":  `<project><build><plugins><plugin><artifactId>maven-toolchains-plugin</artifactId></plugin></plugins></build></project>`,
		"newerJava":  `<project><properties><maven.compiler.release>25</maven.compiler.release></properties></project>`,
	}
	for name, original := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "pom.xml", original)
			p := testPlan(t, root, DefaultPolicy())
			if !p.HasBlockers() {
				t.Fatal("missing blocker")
			}
			if err := Apply(p, false); err == nil {
				t.Fatal("applied blocked plan")
			}
			if readTestFile(t, root, "pom.xml") != original {
				t.Fatal("blocked apply wrote a file")
			}
		})
	}
}

func TestStalePlanPreflightsAllFiles(t *testing.T) {
	root := t.TempDir()
	parent := `<project><packaging>pom</packaging><modules><module>child</module></modules></project>`
	writeTestFile(t, root, "pom.xml", parent)
	writeTestFile(t, root, "child/pom.xml", minimalPOM)
	p := testPlan(t, root, DefaultPolicy())
	writeTestFile(t, root, "pom.xml", parent+"\n<!-- concurrent edit -->")
	if err := Apply(p, false); err == nil {
		t.Fatal("applied stale plan")
	}
	if readTestFile(t, root, "child/pom.xml") != minimalPOM {
		t.Fatal("wrote another POM before detecting stale input")
	}
}

func TestChangedSourceAndNewSourceRejectApply(t *testing.T) {
	for _, newFile := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed", true: "added"}[newFile], func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "pom.xml", minimalPOM)
			writeTestFile(t, root, "src/main/java/Main.java", "class Main {}")
			p := testPlan(t, root, DefaultPolicy())
			path := "src/main/java/Main.java"
			if newFile {
				path = "src/main/java/Other.java"
			}
			writeTestFile(t, root, path, "class Other {}")
			if err := Apply(p, false); err == nil {
				t.Fatal("accepted changed inventory")
			}
			if readTestFile(t, root, "pom.xml") != minimalPOM {
				t.Fatal("wrote POM")
			}
		})
	}
}

func TestResumeInterruptedApply(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project><modules><module>child</module></modules></project>`)
	writeTestFile(t, root, "child/pom.xml", minimalPOM)
	p := testPlan(t, root, DefaultPolicy())
	writeTestFile(t, root, p.Files[0].Path, p.Files[0].After)
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	for _, f := range p.Files {
		if readTestFile(t, root, f.Path) != f.After {
			t.Fatal("failed to resume")
		}
	}
}

func TestRollbackProtectsSubsequentEdits(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", minimalPOM)
	p := testPlan(t, root, DefaultPolicy())
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	changed := readTestFile(t, root, "pom.xml") + "\n<!-- user edit -->"
	writeTestFile(t, root, "pom.xml", changed)
	if err := Apply(p, true); err == nil {
		t.Fatal("rollback overwrote user edits")
	}
	if readTestFile(t, root, "pom.xml") != changed {
		t.Fatal("lost user edits")
	}
}

func TestDeclaredModulesOnlyIncludingProfiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project><modules><module>app</module></modules><profiles><profile><id>extra</id><modules><module>extra</module></modules></profile></profiles></project>`)
	for _, path := range []string{"app/pom.xml", "extra/pom.xml", "unrelated/pom.xml"} {
		writeTestFile(t, root, path, minimalPOM)
	}
	p := testPlan(t, root, DefaultPolicy())
	if len(p.Files) != 3 {
		t.Fatalf("wrong module count %d", len(p.Files))
	}
	for _, f := range p.Files {
		if f.Path == "unrelated/pom.xml" {
			t.Fatal("included unrelated nested POM")
		}
	}
}

func TestEscapingAndSymlinkModulesRejected(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project><modules><module>../outside</module></modules></project>`)
	if _, err := CreatePlan(root, DefaultPolicy()); err == nil {
		t.Fatal("accepted escaping module")
	}
	outside := t.TempDir()
	writeTestFile(t, outside, "pom.xml", minimalPOM)
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "pom.xml", `<project><modules><module>linked</module></modules></project>`)
	if _, err := CreatePlan(root, DefaultPolicy()); err == nil {
		t.Fatal("accepted symlink module")
	}
}

func TestLockPreventsConcurrentApply(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", minimalPOM)
	p := testPlan(t, root, DefaultPolicy())
	release, err := lock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := Apply(p, false); err == nil {
		t.Fatal("ignored migration lock")
	}
}

func TestSourceFindingsLeaveJavaUntouched(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", minimalPOM)
	source := "import sun.misc.BASE64Encoder; class Main {}"
	writeTestFile(t, root, "src/main/java/Main.java", source)
	p := testPlan(t, root, DefaultPolicy())
	if len(p.Findings) < 2 {
		t.Fatal("missing removed/internal API findings")
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, root, "src/main/java/Main.java") != source {
		t.Fatal("rewrote Java without semantic analysis")
	}
}

func TestPlanTamperingRejected(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", minimalPOM)
	p := testPlan(t, root, DefaultPolicy())
	p.Files[0].After += "<!-- unwanted -->"
	p.Files[0].AfterSHA = hash([]byte(p.Files[0].After))
	if err := Apply(p, false); err == nil {
		t.Fatal("accepted content unrelated to policy")
	}
}

func TestVerifyChecksJDKBuildResultAndConvergence(t *testing.T) {
	cases := []struct {
		name, script string
		success      bool
	}{
		{"success", "if [ \"$1\" = --version ]; then echo 'Java version: 21.0.1'; exit 0; fi\ncase \"$*\" in *'-Dmaven.test.skip=false'*'clean verify'*) exit 0;; *) exit 7;; esac\n", true},
		{"wrongJDK", "echo 'Java version: 17.0.1'\n", false},
		{"buildFailure", "if [ \"$1\" = --version ]; then echo 'Java version: 21.0.1'; exit 0; fi\nexit 3\n", false},
		{"sourceMutation", "if [ \"$1\" = --version ]; then echo 'Java version: 21.0.1'; exit 0; fi\necho '// generated' >> src/main/java/Main.java\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "pom.xml", minimalPOM)
			writeTestFile(t, root, "src/main/java/Main.java", "class Main {}")
			writeTestFile(t, root, "mvnw", "#!/bin/sh\n"+tc.script)
			if err := os.Chmod(filepath.Join(root, "mvnw"), 0755); err != nil {
				t.Fatal(err)
			}
			p := testPlan(t, root, DefaultPolicy())
			if err := Apply(p, false); err != nil {
				t.Fatal(err)
			}
			err := Verify(context.Background(), p, VerifyOptions{Timeout: 10 * time.Second})
			if tc.success && err != nil {
				t.Fatal(err)
			}
			if !tc.success && err == nil {
				t.Fatal("verification incorrectly succeeded")
			}
		})
	}
}

func TestPolicyRejectsFloatingVersionsAndInvalidCompiler(t *testing.T) {
	for _, version := range []string{"LATEST", "RELEASE", "1.0-SNAPSHOT", "[1,2)", "${version}", "4.0.0", "3.1.0", "5.0.0"} {
		p := DefaultPolicy()
		p.Plugins["org.apache.maven.plugins:maven-compiler-plugin"] = version
		if err := p.Validate(); err == nil {
			t.Errorf("accepted compiler version %q", version)
		}
	}
}
