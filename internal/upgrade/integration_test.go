package upgrade

import (
	"bytes"
	"context"
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt in because this executes Maven and may resolve artifacts from configured
// repositories. JUPGRADE_MAVEN_REPO allows a writable cache in a sandbox.
func TestJava8To21MavenIntegration(t *testing.T) {
	if os.Getenv("JUPGRADE_INTEGRATION") != "1" {
		t.Skip("set JUPGRADE_INTEGRATION=1 to execute Maven")
	}
	root := t.TempDir()
	fixture := filepath.Join("..", "..", "testdata", "java8")
	err := filepath.WalkDir(fixture, func(path string, entry fs.DirEntry, err error) error {
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
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := DefaultPolicy()
	policy.Dependencies["org.junit.jupiter:junit-jupiter"] = "5.12.2"
	p := testPlan(t, root, policy)
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	options := VerifyOptions{JavaHome: os.Getenv("JUPGRADE_JAVA_HOME"), MavenRepo: os.Getenv("JUPGRADE_MAVEN_REPO"), Offline: os.Getenv("JUPGRADE_OFFLINE") == "1", Timeout: 3 * time.Minute, Stdout: &log, Stderr: &log}
	if err := Verify(context.Background(), p, options); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	class, err := os.ReadFile(filepath.Join(root, "target", "classes", "example", "Greeting.class"))
	if err != nil {
		t.Fatal(err)
	}
	if len(class) < 8 || binary.BigEndian.Uint32(class[:4]) != 0xcafebabe || binary.BigEndian.Uint16(class[6:8]) != 65 {
		t.Fatal("did not emit Java 21 bytecode")
	}
	report, err := os.ReadFile(filepath.Join(root, "target", "surefire-reports", "TEST-example.GreetingTest.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(report, []byte(`tests="1"`)) || !bytes.Contains(report, []byte(`failures="0"`)) {
		t.Fatalf("JUnit test did not pass: %s", report)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if next := testPlan(t, root, policy); len(next.Files) != 0 {
		t.Fatal("migration changed after real Maven build")
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, root, "pom.xml") != p.Files[0].Before {
		t.Fatal("integration rollback failed")
	}
}

func TestReactorInheritanceAndAnnotationProcessorIntegration(t *testing.T) {
	if os.Getenv("JUPGRADE_INTEGRATION") != "1" {
		t.Skip("set JUPGRADE_INTEGRATION=1 to execute Maven")
	}
	root := t.TempDir()
	fixture := filepath.Join("..", "..", "testdata", "reactor")
	originals := map[string]string{}
	err := filepath.WalkDir(fixture, func(path string, entry fs.DirEntry, err error) error {
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
		originals[relative] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := DefaultPolicy()
	policy.Dependencies["org.junit.jupiter:junit-jupiter"] = "5.12.2"
	policy.Dependencies["org.projectlombok:lombok"] = "1.18.46"
	p := testPlan(t, root, policy)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	parent := readTestFile(t, root, "pom.xml")
	if !strings.Contains(parent, "<junit.version>5.12.2</junit.version>") || !strings.Contains(parent, "<lombok.version>1.18.46</lombok.version>") {
		t.Fatal("did not migrate the parent version properties")
	}
	child := readTestFile(t, root, "app/pom.xml")
	if !strings.Contains(child, "<version>${lombok.version}</version>") {
		t.Fatal("did not preserve inherited processor reference")
	}
	var log bytes.Buffer
	options := VerifyOptions{JavaHome: os.Getenv("JUPGRADE_JAVA_HOME"), MavenRepo: os.Getenv("JUPGRADE_MAVEN_REPO"), Offline: os.Getenv("JUPGRADE_OFFLINE") == "1", Timeout: 3 * time.Minute, Stdout: &log, Stderr: &log}
	if err := Verify(context.Background(), p, options); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	class, err := os.ReadFile(filepath.Join(root, "app", "target", "classes", "example", "Person.class"))
	if err != nil {
		t.Fatal(err)
	}
	if len(class) < 8 || binary.BigEndian.Uint16(class[6:8]) != 65 {
		t.Fatal("did not emit Java 21 bytecode")
	}
	if !strings.Contains(log.String(), "Tests run: 1, Failures: 0, Errors: 0, Skipped: 0") {
		t.Fatalf("Lombok/JUnit test did not run successfully:\n%s", log.String())
	}
	if next := testPlan(t, root, policy); len(next.Files) != 0 || next.HasBlockers() {
		t.Fatal("reactor migration did not converge")
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
	for path, before := range originals {
		if readTestFile(t, root, path) != before {
			t.Fatalf("rollback changed %s", path)
		}
	}
}
