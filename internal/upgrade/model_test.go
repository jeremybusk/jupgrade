package upgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parentFixture(t *testing.T) (string, Policy) {
	t.Helper()
	root := t.TempDir()
	parent := `<project><modelVersion>4.0.0</modelVersion><groupId>internal</groupId><artifactId>parent</artifactId><version>1.0</version><packaging>pom</packaging><properties><lib.version>1.0.0</lib.version></properties><modules><module>app</module></modules></project>`
	child := `<project><parent><groupId>internal</groupId><artifactId>parent</artifactId><version>1.0</version></parent><artifactId>app</artifactId><dependencies><dependency><groupId>internal</groupId><artifactId>lib</artifactId><version>${lib.version}</version></dependency></dependencies></project>`
	writeTestFile(t, root, "pom.xml", parent)
	writeTestFile(t, root, "app/pom.xml", child)
	policy := DefaultPolicy()
	policy.Dependencies["internal:lib"] = "2.0.0"
	return root, policy
}

func TestInheritedPropertyUpgradePreservesReferenceAndRoundTrips(t *testing.T) {
	root, policy := parentFixture(t)
	beforeParent := readTestFile(t, root, "pom.xml")
	beforeChild := readTestFile(t, root, "app/pom.xml")
	p := testPlan(t, root, policy)
	if p.HasBlockers() {
		t.Fatalf("unexpected blockers: %+v", p.Findings)
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := WritePlan(path, p); err != nil {
		t.Fatal(err)
	}
	p, err := ReadPlan(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readTestFile(t, root, "pom.xml"), "<lib.version>2.0.0</lib.version>") {
		t.Fatal("owning parent property was not upgraded")
	}
	if !strings.Contains(readTestFile(t, root, "app/pom.xml"), "<version>${lib.version}</version>") {
		t.Fatal("lost inherited property reference")
	}
	report, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range report.Artifacts {
		if a.Path == "app/pom.xml" && a.Coordinate == "internal:lib" {
			found = true
			if a.Resolved != "2.0.0" || a.Owner != "pom.xml" || a.Property != "lib.version" || a.Problem != "" {
				t.Fatalf("wrong inspection: %+v", a)
			}
		}
	}
	if !found {
		t.Fatal("artifact missing from inspection")
	}
	if next := testPlan(t, root, policy); len(next.Files) != 0 || next.HasBlockers() {
		t.Fatalf("not idempotent: %+v", next)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, root, "pom.xml") != beforeParent || readTestFile(t, root, "app/pom.xml") != beforeChild {
		t.Fatal("rollback lost original bytes")
	}
}

func TestInheritedSharedPropertyPinsOnlySelectedDeclaration(t *testing.T) {
	root, policy := parentFixture(t)
	child := readTestFile(t, root, "app/pom.xml")
	other := `<dependency><groupId>internal</groupId><artifactId>other</artifactId><version>${lib.version}</version></dependency>`
	writeTestFile(t, root, "app/pom.xml", strings.Replace(child, "</dependencies>", other+"</dependencies>", 1))
	p := testPlan(t, root, policy)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readTestFile(t, root, "pom.xml"), "<lib.version>1.0.0</lib.version>") {
		t.Fatal("changed shared property")
	}
	child = readTestFile(t, root, "app/pom.xml")
	if !strings.Contains(child, "<artifactId>lib</artifactId><version>2.0.0</version>") || !strings.Contains(child, "<artifactId>other</artifactId><version>${lib.version}</version>") {
		t.Fatal("changed unrelated consumer")
	}
}

func TestOwnedPropertyWithNonVersionConsumerStaysIntact(t *testing.T) {
	root, policy := parentFixture(t)
	parent := readTestFile(t, root, "pom.xml")
	writeTestFile(t, root, "pom.xml", strings.Replace(parent, "</project>", "<name>Library ${lib.version}</name></project>", 1))
	p := testPlan(t, root, policy)
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readTestFile(t, root, "pom.xml"), "<lib.version>1.0.0</lib.version>") {
		t.Fatal("changed non-version consumer")
	}
}

func TestNestedInheritedPropertyAndCycle(t *testing.T) {
	root, policy := parentFixture(t)
	parent := readTestFile(t, root, "pom.xml")
	writeTestFile(t, root, "pom.xml", strings.Replace(parent, "<lib.version>1.0.0</lib.version>", "<lib.version>${base.version}</lib.version><base.version>1.0.0</base.version>", 1))
	p := testPlan(t, root, policy)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readTestFile(t, root, "pom.xml"), "<base.version>2.0.0</base.version>") {
		t.Fatal("did not update terminal owner")
	}
	root, policy = parentFixture(t)
	parent = readTestFile(t, root, "pom.xml")
	writeTestFile(t, root, "pom.xml", strings.Replace(parent, "<lib.version>1.0.0</lib.version>", "<lib.version>${alias}</lib.version><alias>${lib.version}</alias>", 1))
	if p := testPlan(t, root, policy); !p.HasBlockers() {
		t.Fatal("property cycle was not blocked")
	}
}

func TestManagedDependencyUpgradesOwnerAndPreservesVersionlessUse(t *testing.T) {
	root, policy := parentFixture(t)
	parent := readTestFile(t, root, "pom.xml")
	management := `<dependencyManagement><dependencies><dependency><groupId>internal</groupId><artifactId>lib</artifactId><version>${lib.version}</version></dependency></dependencies></dependencyManagement>`
	writeTestFile(t, root, "pom.xml", strings.Replace(parent, "</project>", management+"</project>", 1))
	child := strings.Replace(readTestFile(t, root, "app/pom.xml"), "<version>${lib.version}</version>", "", 1)
	writeTestFile(t, root, "app/pom.xml", child)
	p := testPlan(t, root, policy)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readTestFile(t, root, "pom.xml"), "<lib.version>2.0.0</lib.version>") {
		t.Fatal("did not upgrade management owner")
	}
	after := readTestFile(t, root, "app/pom.xml")
	if strings.Contains(after, "<artifactId>lib</artifactId><version>") {
		t.Fatal("added a leaf override to a managed dependency")
	}
	report, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range report.Artifacts {
		if a.Path == "app/pom.xml" && a.Coordinate == "internal:lib" && (a.Resolved != "2.0.0" || a.Owner != "pom.xml") {
			t.Fatal(a)
		}
	}
}

func TestLocalImportedBOMInspectionAndUpgrade(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project><groupId>internal</groupId><artifactId>root</artifactId><version>1.0</version><packaging>pom</packaging><modules><module>bom</module><module>app</module></modules></project>`)
	writeTestFile(t, root, "bom/pom.xml", `<project><groupId>internal</groupId><artifactId>bom</artifactId><version>1.0</version><packaging>pom</packaging><properties><lib.version>1.0.0</lib.version></properties><dependencyManagement><dependencies><dependency><groupId>internal</groupId><artifactId>lib</artifactId><version>${lib.version}</version></dependency></dependencies></dependencyManagement></project>`)
	writeTestFile(t, root, "app/pom.xml", `<project><groupId>internal</groupId><artifactId>app</artifactId><version>1.0</version><dependencyManagement><dependencies><dependency><groupId>internal</groupId><artifactId>bom</artifactId><version>1.0</version><type>pom</type><scope>import</scope></dependency></dependencies></dependencyManagement><dependencies><dependency><groupId>internal</groupId><artifactId>lib</artifactId></dependency></dependencies></project>`)
	policy := DefaultPolicy()
	policy.Dependencies["internal:lib"] = "2.0.0"
	p := testPlan(t, root, policy)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	report, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range report.Artifacts {
		if a.Path == "app/pom.xml" && a.Coordinate == "internal:lib" && (a.Resolved != "2.0.0" || a.Owner != "bom/pom.xml") {
			t.Fatal(a)
		}
	}
}

func TestExternalBOMRequestIsBlockerNotSilentNoop(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project><dependencyManagement><dependencies><dependency><groupId>external</groupId><artifactId>bom</artifactId><version>1.0</version><type>pom</type><scope>import</scope></dependency></dependencies></dependencyManagement><dependencies><dependency><groupId>internal</groupId><artifactId>lib</artifactId></dependency></dependencies></project>`)
	policy := DefaultPolicy()
	policy.Dependencies["internal:lib"] = "2.0.0"
	p := testPlan(t, root, policy)
	if !p.HasBlockers() {
		t.Fatal("requested upgrade was silently unapplied")
	}
	if err := Apply(p, false); err == nil {
		t.Fatal("applied unresolved request")
	}
}

func TestAnnotationProcessorSharesOwnedVersionWithDependency(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project><properties><lombok.version>1.18.20</lombok.version></properties><dependencies><dependency><groupId>org.projectlombok</groupId><artifactId>lombok</artifactId><version>${lombok.version}</version></dependency></dependencies><build><plugins><plugin><artifactId>maven-compiler-plugin</artifactId><version>3.13.0</version><configuration><annotationProcessorPaths><path><groupId>org.projectlombok</groupId><artifactId>lombok</artifactId><version>${lombok.version}</version></path></annotationProcessorPaths></configuration></plugin></plugins></build></project>`)
	policy := DefaultPolicy()
	policy.Dependencies["org.projectlombok:lombok"] = "1.18.36"
	p := testPlan(t, root, policy)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	after := readTestFile(t, root, "pom.xml")
	if !strings.Contains(after, "<lombok.version>1.18.36</lombok.version>") || strings.Count(after, "<version>${lombok.version}</version>") != 2 {
		t.Fatal("processor and dependency became inconsistent")
	}
}

func TestLocalParentOutsideReactorIsInventoried(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project><parent><groupId>internal</groupId><artifactId>parent</artifactId><version>1.0</version><relativePath>parents/pom.xml</relativePath></parent><artifactId>app</artifactId><dependencies><dependency><groupId>internal</groupId><artifactId>lib</artifactId><version>${lib.version}</version></dependency></dependencies></project>`)
	writeTestFile(t, root, "parents/pom.xml", `<project><groupId>internal</groupId><artifactId>parent</artifactId><version>1.0</version><properties><lib.version>1.0</lib.version></properties></project>`)
	policy := DefaultPolicy()
	policy.Dependencies["internal:lib"] = "2.0"
	p := testPlan(t, root, policy)
	if len(p.Files) != 2 || p.HasBlockers() {
		t.Fatal(p)
	}
	writeTestFile(t, root, "parents/pom.xml", readTestFile(t, root, "parents/pom.xml")+"<!-- changed -->")
	if err := Apply(p, false); err == nil {
		t.Fatal("did not protect parent input")
	}
}

func TestIncompleteRepositoryPlanRejected(t *testing.T) {
	root, policy := parentFixture(t)
	p := testPlan(t, root, policy)
	p.Files = p.Files[:1]
	if err := p.Validate(); err == nil {
		t.Fatal("accepted missing required edits")
	}
}

func TestOwnedPropertyPlanSurvivesInterruptedApply(t *testing.T) {
	root, policy := parentFixture(t)
	p := testPlan(t, root, policy)
	for _, f := range p.Files {
		if f.Path == "pom.xml" {
			writeTestFile(t, root, f.Path, f.After)
		}
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
}

func TestInspectJSONIsDeterministic(t *testing.T) {
	root, _ := parentFixture(t)
	a, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if string(aa) != string(bb) {
		t.Fatal("inspection output varies")
	}
	if _, err := os.Stat(filepath.Join(root, ".jupgrade.lock")); !os.IsNotExist(err) {
		t.Fatal("inspection created a lock")
	}
}

func TestInheritedNewerPluginPreserved(t *testing.T) {
	root, policy := parentFixture(t)
	parent := readTestFile(t, root, "pom.xml")
	build := `<build><pluginManagement><plugins><plugin><artifactId>maven-compiler-plugin</artifactId><version>3.99.0</version></plugin></plugins></pluginManagement></build>`
	writeTestFile(t, root, "pom.xml", strings.Replace(parent, "</project>", build+"</project>", 1))
	p := testPlan(t, root, policy)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readTestFile(t, root, "app/pom.xml"), "<version>3.99.0</version>") {
		t.Fatal("downgraded inherited compiler version")
	}
}

func TestManagementMatchesTypeAndClassifier(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project><dependencyManagement><dependencies><dependency><groupId>internal</groupId><artifactId>lib</artifactId><version>1.0</version><classifier>tests</classifier><type>test-jar</type></dependency></dependencies></dependencyManagement><dependencies><dependency><groupId>internal</groupId><artifactId>lib</artifactId></dependency></dependencies></project>`)
	policy := DefaultPolicy()
	policy.Dependencies["internal:lib"] = "2.0"
	if p := testPlan(t, root, policy); !p.HasBlockers() {
		t.Fatal("incorrectly used test-jar management for a regular JAR")
	}
}

func TestReactorParentWithEmptyRelativePath(t *testing.T) {
	root, policy := parentFixture(t)
	child := readTestFile(t, root, "app/pom.xml")
	writeTestFile(t, root, "app/pom.xml", strings.Replace(child, "</parent>", "<relativePath/></parent>", 1))
	p := testPlan(t, root, policy)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
}

func TestAmbiguousParentProfileBlocksInheritedVersion(t *testing.T) {
	root, policy := parentFixture(t)
	parent := readTestFile(t, root, "pom.xml")
	profiles := `<profiles><profile><id>special</id><properties><lib.version>9.0.0</lib.version></properties></profile></profiles>`
	writeTestFile(t, root, "pom.xml", strings.Replace(parent, "</project>", profiles+"</project>", 1))
	if p := testPlan(t, root, policy); !p.HasBlockers() {
		t.Fatal("guessed inherited profile activation")
	}
}
