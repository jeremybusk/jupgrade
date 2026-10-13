package upgrade

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJavaTargetsConvergeAndRollback(t *testing.T) {
	for _, java := range []int{17, 21, 25} {
		t.Run(fmt.Sprint(java), func(t *testing.T) {
			root := t.TempDir()
			before := `<project><properties><java.version>8</java.version><maven.compiler.testRelease>8</maven.compiler.testRelease></properties><build><plugins><plugin><artifactId>maven-compiler-plugin</artifactId><version>3.8.1</version><configuration><release>8</release><testTarget>8</testTarget></configuration></plugin></plugins></build><profiles><profile><id>legacy</id><properties><maven.compiler.source>1.8</maven.compiler.source></properties></profile></profiles></project>`
			writeTestFile(t, root, "pom.xml", before)
			policy, err := PolicyForJava(java)
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
			after := readTestFile(t, root, "pom.xml")
			for _, key := range []string{"java.version", "maven.compiler.release", "maven.compiler.testRelease", "maven.compiler.source", "release", "testTarget"} {
				if !strings.Contains(after, fmt.Sprintf("<%s>%d</%s>", key, java, key)) {
					t.Fatalf("missing target on %s: %s", key, after)
				}
			}
			if next := testPlan(t, root, policy); len(next.Files) != 0 || next.HasBlockers() {
				t.Fatal("target did not converge", next)
			}
			if err := Apply(p, true); err != nil {
				t.Fatal(err)
			}
			if readTestFile(t, root, "pom.xml") != before {
				t.Fatal("rollback lost original")
			}
		})
	}
}

func TestRefusesJavaDowngradeIncludingTestCompiler(t *testing.T) {
	for _, before := range []string{
		`<project><properties><java.version>25</java.version></properties></project>`,
		`<project><properties><platform.java>25</platform.java><java.version>${platform.java}</java.version></properties></project>`,
		`<project><properties><maven.compiler.testRelease>25</maven.compiler.testRelease></properties></project>`,
		`<project><build><plugins><plugin><artifactId>maven-compiler-plugin</artifactId><configuration><testTarget>25</testTarget></configuration></plugin></plugins></build></project>`,
	} {
		root := t.TempDir()
		writeTestFile(t, root, "pom.xml", before)
		p := testPlan(t, root, DefaultPolicy())
		if !p.HasBlockers() {
			t.Fatal("downgrade accepted", before)
		}
		if err := Apply(p, false); err == nil {
			t.Fatal("applied downgrade")
		}
		if readTestFile(t, root, "pom.xml") != before {
			t.Fatal("blocked plan wrote POM")
		}
	}
}

func TestCommonPresetOwnsVersionsAndPreservesNewer(t *testing.T) {
	root := t.TempDir()
	before := `<project><properties><lombok.version>1.18.20</lombok.version></properties><dependencies><dependency><groupId>org.projectlombok</groupId><artifactId>lombok</artifactId><version>${lombok.version}</version></dependency><dependency><groupId>junit</groupId><artifactId>junit</artifactId><version>4.12</version></dependency><dependency><groupId>commons-io</groupId><artifactId>commons-io</artifactId><version>2.22.0</version></dependency></dependencies></project>`
	writeTestFile(t, root, "pom.xml", before)
	if p := testPlan(t, root, DefaultPolicy()); !p.HasBlockers() {
		t.Fatal("old Lombok on Java 21 was accepted")
	}
	policy := DefaultPolicy()
	if err := policy.AddPreset("common"); err != nil {
		t.Fatal(err)
	}
	p := testPlan(t, root, policy)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	after := readTestFile(t, root, "pom.xml")
	for _, want := range []string{"<lombok.version>1.18.46</lombok.version>", "<version>${lombok.version}</version>", "<version>4.13.2</version>", "<version>2.22.0</version>"} {
		if !strings.Contains(after, want) {
			t.Fatal(after)
		}
	}
	if next := testPlan(t, root, policy); len(next.Files) != 0 || next.HasBlockers() {
		t.Fatal(next)
	}
}

func TestCompatibilityFloorsAndManagedVersions(t *testing.T) {
	for _, java := range []int{17, 21, 25} {
		t.Run(fmt.Sprint(java), func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "pom.xml", `<project><dependencies><dependency><groupId>org.projectlombok</groupId><artifactId>lombok</artifactId><version>1.18.20</version></dependency><dependency><groupId>org.ow2.asm</groupId><artifactId>asm</artifactId><version>8.0</version></dependency></dependencies><build><plugins><plugin><groupId>org.jacoco</groupId><artifactId>jacoco-maven-plugin</artifactId><version>0.8.7</version></plugin></plugins></build></project>`)
			policy, _ := PolicyForJava(java)
			p := testPlan(t, root, policy)
			if !p.HasBlockers() {
				t.Fatal("incompatible libraries were accepted")
			}
			if err := policy.AddPreset("common"); err != nil {
				t.Fatal(err)
			}
			if next := testPlan(t, root, policy); next.HasBlockers() {
				t.Fatal(next.Findings)
			}
		})
	}
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project><parent><groupId>external</groupId><artifactId>parent</artifactId><version>1.0</version><relativePath/></parent><dependencies><dependency><groupId>org.projectlombok</groupId><artifactId>lombok</artifactId></dependency></dependencies></project>`)
	policy := DefaultPolicy()
	_ = policy.AddPreset("common")
	if p := testPlan(t, root, policy); !p.HasBlockers() {
		t.Fatal("guessed external management")
	}
}

func TestJettyCompatibilityFollowsTarget(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project><build><plugins><plugin><groupId>org.eclipse.jetty</groupId><artifactId>jetty-maven-plugin</artifactId><version>9.4.48.v20220622</version></plugin></plugins></build></project>`)
	policy, _ := PolicyForJava(17)
	delete(policy.Plugins, "org.eclipse.jetty:jetty-maven-plugin")
	if p := testPlan(t, root, policy); p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	policy, _ = PolicyForJava(25)
	if p := testPlan(t, root, policy); !p.HasBlockers() {
		t.Fatal("Java 25 accepted Jetty's old ASM")
	}
	writeTestFile(t, root, "pom.xml", `<project><build><plugins><plugin><groupId>org.eclipse.jetty</groupId><artifactId>jetty-maven-plugin</artifactId><version>9.4.58.v20250814</version><dependencies><dependency><groupId>org.ow2.asm</groupId><artifactId>asm</artifactId><version>9.9.1</version></dependency><dependency><groupId>org.ow2.asm</groupId><artifactId>asm-commons</artifactId><version>9.3</version></dependency></dependencies></plugin></plugins></build></project>`)
	if p := testPlan(t, root, policy); !p.HasBlockers() {
		t.Fatal("incompatible ASM companion was accepted")
	}
}

func TestSpringBootMajorRequiresSourceRecipe(t *testing.T) {
	for _, coord := range []string{"spring-boot-starter-parent", "spring-boot-dependencies", "spring-boot-maven-plugin"} {
		t.Run(coord, func(t *testing.T) {
			declaration := `<groupId>org.springframework.boot</groupId><artifactId>` + coord + `</artifactId><version>${boot.version}</version>`
			body := `<parent>` + declaration + `<relativePath/></parent>`
			if coord == "spring-boot-dependencies" {
				body = `<dependencyManagement><dependencies><dependency>` + declaration + `<type>pom</type><scope>import</scope></dependency></dependencies></dependencyManagement>`
			}
			if coord == "spring-boot-maven-plugin" {
				body = `<build><plugins><plugin>` + declaration + `</plugin></plugins></build>`
			}
			before := `<project>` + body + `<properties><boot.version>2.7.18</boot.version></properties></project>`
			root := t.TempDir()
			writeTestFile(t, root, "pom.xml", before)
			policy := DefaultPolicy()
			policy.Parents["org.springframework.boot:"+coord] = "3.5.10"
			policy.Dependencies["org.springframework.boot:"+coord] = "3.5.10"
			policy.Plugins["org.springframework.boot:"+coord] = "3.5.10"
			p := testPlan(t, root, policy)
			if !p.HasBlockers() {
				t.Fatal("major Boot bump accepted", p.Findings)
			}
			if err := Apply(p, false); err == nil {
				t.Fatal("major bump applied")
			}
			if readTestFile(t, root, "pom.xml") != before {
				t.Fatal("blocked upgrade changed input")
			}
			writeTestFile(t, root, "pom.xml", strings.Replace(before, "2.7.18", "3.5.9", 1))
			next := testPlan(t, root, policy)
			if next.HasBlockers() {
				t.Fatal(next.Findings)
			}
			if err := Apply(next, false); err != nil {
				t.Fatal(err)
			}
			if final := testPlan(t, root, policy); len(final.Files) != 0 {
				t.Fatal("Boot rule did not converge")
			}
		})
	}
}

func TestGradleDetection(t *testing.T) {
	for _, file := range []string{"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts"} {
		root := t.TempDir()
		writeTestFile(t, root, file, "plugins {}")
		p, err := CreatePlan(root, DefaultPolicy())
		if err != nil || p.Build != "gradle" || p.Schema != 4 {
			t.Fatal(p, err)
		}
		report, err := Inspect(root)
		if err != nil || report.Build != "gradle" {
			t.Fatal(report, err)
		}
		writeTestFile(t, root, "pom.xml", minimalPOM)
		if _, err := CreatePlan(root, DefaultPolicy()); err == nil {
			t.Fatal("accepted ambiguous mixed build")
		}
	}
}

func TestRuntimePinsFollowJavaTarget(t *testing.T) {
	for _, java := range []int{17, 21, 25} {
		root := t.TempDir()
		writeTestFile(t, root, "pom.xml", minimalPOM)
		writeTestFile(t, root, ".java-version", fmt.Sprint(java))
		writeTestFile(t, root, "Dockerfile", fmt.Sprintf("FROM eclipse-temurin:%d-jre", java))
		policy, _ := PolicyForJava(java)
		p := testPlan(t, root, policy)
		for _, f := range p.Findings {
			if f.Path == ".java-version" || f.Path == "Dockerfile" {
				t.Fatal("target pin flagged", f)
			}
		}
		writeTestFile(t, root, ".java-version", "11")
		writeTestFile(t, root, "Dockerfile", "FROM eclipse-temurin:11-jre")
		p = testPlan(t, root, policy)
		count := 0
		for _, f := range p.Findings {
			if f.Path == ".java-version" || f.Path == "Dockerfile" {
				count++
			}
		}
		if count != 2 {
			t.Fatal("missing mismatched pins", p.Findings)
		}
	}
}

func TestVerifyUsesPlanTargetAndMavenVersion(t *testing.T) {
	for _, java := range []int{17, 21, 25} {
		root := t.TempDir()
		writeTestFile(t, root, "pom.xml", minimalPOM)
		policy, _ := PolicyForJava(java)
		p := testPlan(t, root, policy)
		if err := Apply(p, false); err != nil {
			t.Fatal(err)
		}
		wrapper := filepath.Join(root, "mvnw")
		script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'Apache Maven 3.9.11'; echo 'Java version: %d'; exit 0; fi\nprintf '%%s\\n' \"$@\"\n", java)
		if err := os.WriteFile(wrapper, []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
		// Include the executable in the inventory before creating the saved plan.
		p = testPlan(t, root, policy)
		var log bytes.Buffer
		if err := Verify(context.Background(), p, VerifyOptions{Stdout: &log, Stderr: &log}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(log.String(), "clean\nverify") {
			t.Fatal("did not run build", log.String())
		}
		mismatch := strings.Replace(script, fmt.Sprintf("Java version: %d", java), "Java version: 11", 1)
		if err := os.WriteFile(wrapper, []byte(mismatch), 0755); err != nil {
			t.Fatal(err)
		}
		p = testPlan(t, root, policy)
		if err := Verify(context.Background(), p, VerifyOptions{}); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("requires Java %d", java)) {
			t.Fatal(err)
		}
	}
}
