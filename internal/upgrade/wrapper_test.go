package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const wrapperURL = `https\://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/3.8.8/apache-maven-3.8.8-bin.zip`

func TestMavenWrapperPlanRoundTripAndExactRollback(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", minimalPOM)
	before := "# keep this comment\r\ndistributionUrl = " + wrapperURL + "  \r\ndistributionSha256Sum=" + strings.Repeat("a", 64) + "\r\nwrapperUrl=https://example.org/wrapper.jar\r\n"
	writeTestFile(t, root, mavenWrapperProperties, before)
	policy := DefaultPolicy()
	policy.Maven = "3.9.11"
	policy.MavenSHA256 = strings.Repeat("B", 64)
	p := testPlan(t, root, policy)
	if p.Schema != 3 || p.HasBlockers() || len(p.Files) != 2 {
		t.Fatalf("bad wrapper plan: %+v", p)
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
	after := readTestFile(t, root, mavenWrapperProperties)
	want := strings.ReplaceAll(strings.ReplaceAll(before, "3.8.8", "3.9.11"), strings.Repeat("a", 64), strings.Repeat("b", 64))
	if after != want {
		t.Fatalf("wrapper formatting/content: %q", after)
	}
	if next := testPlan(t, root, policy); len(next.Files) != 0 || next.HasBlockers() {
		t.Fatal(next)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, root, mavenWrapperProperties) != before {
		t.Fatal("rollback did not restore wrapper exactly")
	}
}

func TestMavenWrapperChecksumCannotBeSilentlyDropped(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", minimalPOM)
	before := "distributionUrl=" + wrapperURL + "\ndistributionSha256Sum=" + strings.Repeat("a", 64) + "\n"
	writeTestFile(t, root, mavenWrapperProperties, before)
	policy := DefaultPolicy()
	policy.Maven = "3.9.11"
	p := testPlan(t, root, policy)
	if !p.HasBlockers() {
		t.Fatal("accepted stale archive checksum")
	}
	if err := Apply(p, false); err == nil {
		t.Fatal("applied incompatible checksum")
	}
	if readTestFile(t, root, mavenWrapperProperties) != before {
		t.Fatal("blocked upgrade changed wrapper")
	}
}

func TestMavenWrapperRefusesTamperedOrStalePlans(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", minimalPOM)
	before := "distributionUrl=" + wrapperURL + "\n"
	writeTestFile(t, root, mavenWrapperProperties, before)
	policy := DefaultPolicy()
	policy.Maven = "3.9.11"
	p := testPlan(t, root, policy)
	for i := range p.Files {
		if p.Files[i].Path == mavenWrapperProperties {
			p.Files[i].After += "wrapperUrl=https://unrelated.example/wrapper.jar\n"
			p.Files[i].AfterSHA = hash([]byte(p.Files[i].After))
		}
	}
	if err := Apply(p, false); err == nil {
		t.Fatal("accepted unrelated wrapper edit")
	}
	p = testPlan(t, root, policy)
	writeTestFile(t, root, mavenWrapperProperties, before+"# local edit\n")
	if err := Apply(p, false); err == nil {
		t.Fatal("accepted stale wrapper")
	}
	if readTestFile(t, root, "pom.xml") != minimalPOM {
		t.Fatal("partial write before stale check")
	}
}

func TestMavenWrapperMissingUnknownAndDuplicateConfigurationsBlock(t *testing.T) {
	for _, before := range []string{
		"", "distributionUrl=https://example.org/maven.zip\n",
		"distributionUrl=" + wrapperURL + "\ndistributionUrl=" + wrapperURL + "\n",
		"distributionUrl=" + wrapperURL + "\\\n", "distributionUrl=http://example.org/apache-maven-3.8.8-bin.zip\n",
		"distributionUrl=https://example.org/download/apache-maven-3.8.8-bin.zip\n",
		"distributionUrl=" + wrapperURL + "\ndistributionSha256Sum=a\ndistributionSha256Sum=b\n",
	} {
		root := t.TempDir()
		writeTestFile(t, root, "pom.xml", minimalPOM)
		if before != "" {
			writeTestFile(t, root, mavenWrapperProperties, before)
		}
		policy := DefaultPolicy()
		policy.Maven = "3.9.11"
		p := testPlan(t, root, policy)
		if !p.HasBlockers() {
			t.Fatal("accepted unsupported wrapper", before)
		}
	}
}

func TestMavenWrapperNoDowngradeAndEmptyChecksum(t *testing.T) {
	policy := DefaultPolicy()
	policy.Maven = "3.9.11"
	policy.MavenSHA256 = strings.Repeat("b", 64)
	before := "distributionUrl=" + strings.ReplaceAll(wrapperURL, "3.8.8", "3.9.12") + "\ndistributionSha256Sum=" + strings.Repeat("a", 64) + "\n"
	after, changes, _ := migrateMavenWrapper([]byte(before), policy)
	if string(after) != before || len(changes) != 0 {
		t.Fatal("downgraded Maven or applied older archive checksum")
	}
	before = "distributionUrl=" + wrapperURL + "\ndistributionSha256Sum=\n"
	after, _, findings := migrateMavenWrapper([]byte(before), policy)
	if len(findings) != 0 || strings.Count(string(after), "distributionSha256Sum=") != 1 || !strings.Contains(string(after), strings.Repeat("b", 64)) {
		t.Fatal(string(after), findings)
	}
	next, changes, findings := migrateMavenWrapper(after, policy)
	if string(next) != string(after) || len(changes) != 0 || len(findings) != 0 {
		t.Fatal("wrapper did not converge")
	}
}

func TestMavenWrapperApacheMirrorLayout(t *testing.T) {
	policy := DefaultPolicy()
	policy.Maven = "3.9.11"
	before := "distributionUrl=https://mirror.example.org/maven/maven-3/3.8.8/binaries/apache-maven-3.8.8-bin.tar.gz\n"
	after, changes, findings := migrateMavenWrapper([]byte(before), policy)
	if len(changes) != 1 || string(after) != strings.ReplaceAll(before, "3.8.8", "3.9.11") {
		t.Fatal(string(after), findings)
	}
}

func TestVerifyChecksMavenRuntimeTarget(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", minimalPOM)
	writeTestFile(t, root, mavenWrapperProperties, "distributionUrl="+strings.ReplaceAll(wrapperURL, "3.8.8", "3.9.11")+"\n")
	path := filepath.Join(root, "mvnw")
	for _, version := range []string{"3.8.8", "3.9.11", "3.9.12", "4.0.0"} {
		script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'Apache Maven " + version + "'; echo 'Java version: 21'; fi\nexit 0\n"
		if err := os.WriteFile(path, []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
		policy := DefaultPolicy()
		policy.Maven = "3.9.11"
		p := testPlan(t, root, policy)
		if err := Apply(p, false); err != nil {
			t.Fatal(err)
		}
		err := Verify(context.Background(), p, VerifyOptions{})
		if version == "3.9.11" || version == "3.9.12" {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			if err == nil || !strings.Contains(err.Error(), "requires Maven") {
				t.Fatal(err)
			}
		}
	}
}
