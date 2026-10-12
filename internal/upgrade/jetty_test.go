package upgrade

import (
	"strings"
	"testing"
)

func TestLegacyJettyPluginUpgradedForJava21(t *testing.T) {
	root := t.TempDir()
	before := `<project><packaging>war</packaging><build><plugins><plugin><groupId>org.eclipse.jetty</groupId><artifactId>jetty-maven-plugin</artifactId><version>9.4.48.v20220622</version><configuration><webApp><contextPath>/demo</contextPath></webApp></configuration></plugin></plugins></build><dependencies><dependency><groupId>javax.servlet</groupId><artifactId>javax.servlet-api</artifactId><version>3.1.0</version><scope>provided</scope></dependency></dependencies></project>`
	writeTestFile(t, root, "pom.xml", before)
	p := testPlan(t, root, DefaultPolicy())
	if p.HasBlockers() {
		t.Fatalf("unexpected blockers: %+v", p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	after := readTestFile(t, root, "pom.xml")
	if !strings.Contains(after, "<version>9.4.58.v20250814</version>") {
		t.Fatal("left the legacy Jetty annotation scanner on Java 21")
	}
	if !strings.Contains(after, "<contextPath>/demo</contextPath>") || !strings.Contains(after, "<artifactId>javax.servlet-api</artifactId><version>3.1.0</version>") {
		t.Fatal("changed unrelated web configuration")
	}
	if next := testPlan(t, root, DefaultPolicy()); len(next.Files) != 0 || next.HasBlockers() {
		t.Fatal("Jetty migration did not converge")
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, root, "pom.xml") != before {
		t.Fatal("Jetty rollback lost original POM content")
	}
}

func TestJettyVersionOrderingAndNoDowngrades(t *testing.T) {
	for _, current := range []string{"9.4.59.v20260101", "10.0.26", "11.0.26"} {
		t.Run(current, func(t *testing.T) {
			root := t.TempDir()
			before := `<project><build><plugins><plugin><groupId>org.eclipse.jetty</groupId><artifactId>jetty-maven-plugin</artifactId><version>` + current + `</version></plugin></plugins></build></project>`
			writeTestFile(t, root, "pom.xml", before)
			p := testPlan(t, root, DefaultPolicy())
			if p.HasBlockers() {
				t.Fatal(p.Findings)
			}
			if err := Apply(p, false); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(readTestFile(t, root, "pom.xml"), "<version>"+current+"</version>") {
				t.Fatal("downgraded newer Jetty release")
			}
		})
	}
	if _, ok := compareArtifactVersions("org.eclipse.jetty:jetty-maven-plugin", "9.4.58-beta-1", "9.4.58.v20250814"); ok {
		t.Fatal("guessed beta ordering")
	}
	if _, ok := compareArtifactVersions("internal:lib", "9.4.48.v20220622", "9.4.58.v20250814"); ok {
		t.Fatal("applied Jetty ordering to unrelated artifacts")
	}
}

func TestJettyInheritedVersionPropertyUpgrade(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project><groupId>internal</groupId><artifactId>parent</artifactId><version>1.0</version><packaging>pom</packaging><modules><module>app</module></modules><properties><jetty.version>9.4.48.v20220622</jetty.version></properties></project>`)
	writeTestFile(t, root, "app/pom.xml", `<project><parent><groupId>internal</groupId><artifactId>parent</artifactId><version>1.0</version></parent><artifactId>app</artifactId><build><plugins><plugin><groupId>org.eclipse.jetty</groupId><artifactId>jetty-maven-plugin</artifactId><version>${jetty.version}</version></plugin></plugins></build></project>`)
	p := testPlan(t, root, DefaultPolicy())
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readTestFile(t, root, "pom.xml"), "<jetty.version>9.4.58.v20250814</jetty.version>") || !strings.Contains(readTestFile(t, root, "app/pom.xml"), "<version>${jetty.version}</version>") {
		t.Fatal("lost Jetty version property ownership")
	}
}

func TestCustomPolicyCannotSilentlyLeaveKnownBrokenJetty(t *testing.T) {
	root := t.TempDir()
	before := `<project><build><plugins><plugin><groupId>org.eclipse.jetty</groupId><artifactId>jetty-maven-plugin</artifactId><version>9.4.48.v20220622</version></plugin></plugins></build></project>`
	writeTestFile(t, root, "pom.xml", before)
	policy := DefaultPolicy()
	delete(policy.Plugins, "org.eclipse.jetty:jetty-maven-plugin")
	p := testPlan(t, root, policy)
	if !p.HasBlockers() {
		t.Fatal("silently left an incompatible server annotation scanner")
	}
	if err := Apply(p, false); err == nil {
		t.Fatal("applied incomplete runtime migration")
	}
	if readTestFile(t, root, "pom.xml") != before {
		t.Fatal("blocked migration edited the repository")
	}
}

func TestOldExplicitJettyASMOverrideStillBlocksAfterPluginUpgrade(t *testing.T) {
	root := t.TempDir()
	before := `<project><build><plugins><plugin><groupId>org.eclipse.jetty</groupId><artifactId>jetty-maven-plugin</artifactId><version>9.4.48.v20220622</version><dependencies><dependency><groupId>org.ow2.asm</groupId><artifactId>asm</artifactId><version>9.3</version></dependency></dependencies></plugin></plugins></build></project>`
	writeTestFile(t, root, "pom.xml", before)
	p := testPlan(t, root, DefaultPolicy())
	if !p.HasBlockers() {
		t.Fatal("old explicit ASM overrides the upgraded plugin's parser")
	}
	policy := DefaultPolicy()
	policy.Dependencies["org.ow2.asm:asm"] = "9.7.1"
	p = testPlan(t, root, policy)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
}
