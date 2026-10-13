package upgrade

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCachedMavenBOMUpgradePreservesManagementAndReplay(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	before := `<project><dependencyManagement><dependencies><dependency><groupId>external</groupId><artifactId>bom</artifactId><version>1.0</version><type>pom</type><scope>import</scope></dependency></dependencies></dependencyManagement><dependencies><dependency><groupId>org.junit.jupiter</groupId><artifactId>junit-jupiter</artifactId></dependency></dependencies></project>`
	writeTestFile(t, root, "pom.xml", before)
	for _, v := range []string{"1.0", "2.0"} {
		junit := "5.11.4"
		if v == "2.0" {
			junit = "5.12.2"
		}
		pom := `<project><groupId>external</groupId><artifactId>bom</artifactId><version>` + v + `</version><properties><junit.version>` + junit + `</junit.version></properties><dependencyManagement><dependencies><dependency><groupId>org.junit.jupiter</groupId><artifactId>junit-jupiter</artifactId><version>${junit.version}</version></dependency></dependencies></dependencyManagement></project>`
		writeTestFile(t, cache, filepath.Join("external", "bom", v, "bom-"+v+".pom"), pom)
	}
	policy := DefaultPolicy()
	policy.Dependencies["external:bom"] = "2.0"
	policy.Dependencies["org.junit.jupiter:junit-jupiter"] = "5.12.2"
	if p := testPlan(t, root, policy); !p.HasBlockers() {
		t.Fatal("unresolved external BOM was accepted")
	}
	resolved, err := ResolveMaven(root, policy, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.MavenModels) != 2 {
		t.Fatal("did not capture both model versions")
	}
	p := testPlan(t, root, resolved)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	after := readTestFile(t, root, "pom.xml")
	if strings.Contains(after, "<artifactId>junit-jupiter</artifactId><version>") {
		t.Fatal("overrode BOM-owned dependency")
	}
	// Cached originals can change/disappear without affecting saved plan replay.
	writeTestFile(t, cache, "external/bom/2.0/bom-2.0.pom", "invalid cache now")
	if next := testPlan(t, root, resolved); len(next.Files) != 0 || next.HasBlockers() {
		t.Fatal(next)
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, root, "pom.xml") != before {
		t.Fatal("rollback changed original")
	}
}

func TestCachedParentPropertiesAndPluginVersions(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project><parent><groupId>external</groupId><artifactId>parent</artifactId><version>1.0</version><relativePath/></parent><dependencies><dependency><groupId>internal</groupId><artifactId>library</artifactId><version>${library.version}</version></dependency></dependencies></project>`)
	parent := `<project><groupId>external</groupId><artifactId>parent</artifactId><version>1.0</version><properties><library.version>1.0</library.version><compiler.version>3.15.0</compiler.version></properties><build><pluginManagement><plugins><plugin><artifactId>maven-compiler-plugin</artifactId><version>${compiler.version}</version></plugin></plugins></pluginManagement></build></project>`
	writeTestFile(t, cache, "external/parent/1.0/parent-1.0.pom", parent)
	policy := DefaultPolicy()
	policy.Dependencies["internal:library"] = "2.0"
	resolved, err := ResolveMaven(root, policy, cache)
	if err != nil {
		t.Fatal(err)
	}
	p := testPlan(t, root, resolved)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	after := readTestFile(t, root, "pom.xml")
	if !strings.Contains(after, "<version>3.15.0</version>") || !strings.Contains(after, "<artifactId>library</artifactId><version>2.0</version>") {
		t.Fatal(after)
	}
	if readTestFile(t, cache, "external/parent/1.0/parent-1.0.pom") != parent {
		t.Fatal("edited external model")
	}
}

func TestCachedMavenModelsRejectMissingTamperedAndAmbiguousProfiles(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project><parent><groupId>external</groupId><artifactId>parent</artifactId><version>1.0</version><relativePath/></parent><dependencies><dependency><groupId>internal</groupId><artifactId>library</artifactId><version>${library.version}</version></dependency></dependencies></project>`)
	policy := DefaultPolicy()
	policy.Dependencies["internal:library"] = "2.0"
	if _, err := ResolveMaven(root, policy, cache); err == nil {
		t.Fatal("invented missing model")
	}
	parent := `<project><groupId>external</groupId><artifactId>parent</artifactId><version>1.0</version><properties><library.version>1.0</library.version></properties><profiles><profile><id>active</id><properties><library.version>3.0</library.version></properties></profile></profiles></project>`
	writeTestFile(t, cache, "external/parent/1.0/parent-1.0.pom", parent)
	resolved, err := ResolveMaven(root, policy, cache)
	if err != nil {
		t.Fatal(err)
	}
	if p := testPlan(t, root, resolved); !p.HasBlockers() {
		t.Fatal("guessed inherited profile")
	}
	entry := resolved.MavenModels["external:parent:1.0"]
	entry.Content += "<!-- changed -->"
	resolved.MavenModels["external:parent:1.0"] = entry
	if err := resolved.Validate(); err == nil {
		t.Fatal("accepted tampered snapshot")
	}
}

func TestUpgradedExternalParentPreservesNewerManagedPlugin(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project><parent><groupId>external</groupId><artifactId>parent</artifactId><version>1.0</version><relativePath/></parent></project>`)
	for version, compiler := range map[string]string{"1.0": "3.13.0", "2.0": "3.15.0"} {
		writeTestFile(t, cache, "external/parent/"+version+"/parent-"+version+".pom", `<project><groupId>external</groupId><artifactId>parent</artifactId><version>`+version+`</version><build><pluginManagement><plugins><plugin><artifactId>maven-compiler-plugin</artifactId><version>`+compiler+`</version></plugin></plugins></pluginManagement></build></project>`)
	}
	policy := DefaultPolicy()
	policy.Parents["external:parent"] = "2.0"
	policy, err := ResolveMaven(root, policy, cache)
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
	if !strings.Contains(after, `<artifactId>maven-compiler-plugin</artifactId><version>3.15.0</version>`) {
		t.Fatal(after)
	}
	if next := testPlan(t, root, policy); len(next.Files) != 0 || next.HasBlockers() {
		t.Fatal(next)
	}
}
