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

func TestTypeAwareRecipeMavenIntegration(t *testing.T) {
	if os.Getenv("JUPGRADE_SOURCE_INTEGRATION") != "1" {
		t.Skip("set JUPGRADE_SOURCE_INTEGRATION=1 for the cached OpenRewrite worker")
	}
	root := t.TempDir()
	pom := `<project><modelVersion>4.0.0</modelVersion><groupId>example</groupId><artifactId>source-recipe</artifactId><version>1.0</version><properties><maven.compiler.release>17</maven.compiler.release></properties></project>`
	source := `package example; public class Greeting { public static String hello(String name) { return String.format("Hello, %s", name); } }`
	writeTestFile(t, root, "pom.xml", pom)
	writeTestFile(t, root, "src/main/java/example/Greeting.java", source)
	policy := DefaultPolicy()
	policy.Recipes = []string{"string-format"}
	var log bytes.Buffer
	options := VerifyOptions{MavenRepo: os.Getenv("JUPGRADE_MAVEN_REPO"), JavaHome: os.Getenv("JUPGRADE_JAVA_HOME"), Offline: true, Timeout: 3 * time.Minute, Stdout: &log, Stderr: &log}
	p, err := CreateRecipePlan(context.Background(), root, policy, options)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	after := readTestFile(t, root, "src/main/java/example/Greeting.java")
	if !strings.Contains(after, `.formatted(name)`) {
		t.Fatalf("type-aware rewrite did not change source: %s\n%s", after, log.String())
	}
	if err := Verify(context.Background(), p, options); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	class, err := os.ReadFile(filepath.Join(root, "target/classes/example/Greeting.class"))
	if err != nil {
		t.Fatal(err)
	}
	if len(class) < 8 || binary.BigEndian.Uint16(class[6:8]) != 65 {
		t.Fatal("recipe result did not compile as Java 21")
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, root, "src/main/java/example/Greeting.java") != source || readTestFile(t, root, "pom.xml") != pom {
		t.Fatal("real recipe rollback changed originals")
	}
}

func TestExternalJUnitBOMMavenIntegration(t *testing.T) {
	if os.Getenv("JUPGRADE_INTEGRATION") != "1" {
		t.Skip("set JUPGRADE_INTEGRATION=1 for external BOM build")
	}
	root := t.TempDir()
	pom := `<project><modelVersion>4.0.0</modelVersion><groupId>example</groupId><artifactId>bom-example</artifactId><version>1.0</version><dependencyManagement><dependencies><dependency><groupId>org.junit</groupId><artifactId>junit-bom</artifactId><version>5.11.4</version><type>pom</type><scope>import</scope></dependency></dependencies></dependencyManagement><dependencies><dependency><groupId>org.junit.jupiter</groupId><artifactId>junit-jupiter</artifactId><scope>test</scope></dependency></dependencies></project>`
	writeTestFile(t, root, "pom.xml", pom)
	writeTestFile(t, root, "src/test/java/example/SmokeTest.java", `package example; import org.junit.jupiter.api.Test; import static org.junit.jupiter.api.Assertions.*; class SmokeTest { @Test void test() { assertEquals(2,1+1); } }`)
	policy := DefaultPolicy()
	policy.Dependencies["org.junit:junit-bom"] = "5.12.2"
	policy.Dependencies["org.junit.jupiter:junit-jupiter"] = "5.12.2"
	policy, err := ResolveMaven(root, policy, os.Getenv("JUPGRADE_MAVEN_REPO"))
	if err != nil {
		t.Fatal(err)
	}
	p := testPlan(t, root, policy)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	if err := Verify(context.Background(), p, VerifyOptions{MavenRepo: os.Getenv("JUPGRADE_MAVEN_REPO"), Offline: true, Timeout: 2 * time.Minute, Stdout: &log, Stderr: &log}); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if !strings.Contains(log.String(), "Tests run: 1, Failures: 0") {
		t.Fatal(log.String())
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, root, "pom.xml") != pom {
		t.Fatal("BOM rollback changed original")
	}
}

func TestBootMajorSourceAndContextMavenIntegration(t *testing.T) {
	if os.Getenv("JUPGRADE_BOOT_SOURCE_INTEGRATION") != "1" {
		t.Skip("set JUPGRADE_BOOT_SOURCE_INTEGRATION=1 for cached Boot/OpenRewrite migration")
	}
	root := t.TempDir()
	fixture := filepath.Join("..", "..", "testdata", "boot-context")
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
		originals[relative] = string(data)
		writeTestFile(t, root, relative, string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := DefaultPolicy()
	policy.Parents["org.springframework.boot:spring-boot-starter-parent"] = "4.0.8"
	policy.Dependencies["org.springframework.boot:spring-boot-dependencies"] = "4.0.8"
	policy.Recipes = []string{"boot4.0", "java21"}
	policy, err = ResolveMaven(root, policy, os.Getenv("JUPGRADE_MAVEN_REPO"))
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	options := VerifyOptions{MavenRepo: os.Getenv("JUPGRADE_MAVEN_REPO"), JavaHome: os.Getenv("JUPGRADE_JAVA_HOME"), Offline: true, Timeout: 3 * time.Minute, Stdout: &log, Stderr: &log}
	p, err := CreateRecipePlan(context.Background(), root, policy, options)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	for path, before := range originals {
		if readTestFile(t, root, path) != before {
			t.Fatal("recipe planning edited original", path)
		}
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if err := Verify(context.Background(), p, options); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if !strings.Contains(log.String(), "BOOT_CONTEXT_VERSION=4.0.8") || !strings.Contains(log.String(), "Tests run: 1, Failures: 0") {
		t.Fatal("Boot 4 context test did not execute", log.String())
	}
	class, err := os.ReadFile(filepath.Join(root, "target/test-classes/example/ContextTest.class"))
	if err != nil {
		t.Fatal(err)
	}
	if len(class) < 8 || binary.BigEndian.Uint16(class[6:8]) != 65 {
		t.Fatal("Boot test did not emit Java 21")
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
	for path, before := range originals {
		if readTestFile(t, root, path) != before {
			t.Fatal("Boot rollback changed original", path)
		}
	}
}
