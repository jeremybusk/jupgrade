package upgrade

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBatchMavenIntegration(t *testing.T) {
	if os.Getenv("JUPGRADE_BATCH_INTEGRATION") != "1" {
		t.Skip("set JUPGRADE_BATCH_INTEGRATION=1 to run real Maven batch upgrades")
	}
	manifest := BatchManifest{Schema: 1, Repositories: []BatchEntry{}}
	for _, name := range []string{"java8", "maven-bom"} {
		root := t.TempDir()
		copyIntegrationFixture(t, name, root)
		manifest.Repositories = append(manifest.Repositories, BatchEntry{Name: name, Repo: root})
	}
	policy := DefaultPolicy()
	_ = policy.AddPreset("common")
	opts := BatchOptions{Policy: policy, ResolveMaven: true, Verify: VerifyOptions{MavenRepo: os.Getenv("JUPGRADE_MAVEN_REPO"), JavaHome: os.Getenv("JUPGRADE_JAVA_HOME"), Offline: true}}
	output := filepath.Join(t.TempDir(), "batch")
	report, err := RunBatch(context.Background(), manifest, output, opts)
	if err != nil {
		for _, row := range report.Results {
			t.Log(row.Error)
		}
		t.Fatal(err)
	}
	class, err := os.ReadFile(filepath.Join(manifest.Repositories[0].Repo, "target/classes/example/Greeting.class"))
	if err != nil || len(class) < 8 || binary.BigEndian.Uint16(class[6:8]) != 65 {
		t.Fatal("batch did not compile Java 21", err)
	}
	opts.Resume = true
	if _, err := RunBatch(context.Background(), manifest, output, opts); err != nil {
		t.Fatal(err)
	}
	for _, row := range report.Results {
		if row.Status != "verified" {
			t.Fatal(row)
		}
		p, err := ReadPlan(row.Plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := Apply(p, true); err != nil {
			t.Fatal(err)
		}
	}
}

func batchFixture(t *testing.T) (BatchManifest, string) {
	t.Helper()
	manifest := BatchManifest{Schema: 1, Repositories: []BatchEntry{}}
	for _, name := range []string{"one", "two"} {
		root := t.TempDir()
		writeTestFile(t, root, "pom.xml", `<project/>`)
		manifest.Repositories = append(manifest.Repositories, BatchEntry{Name: name, Repo: root})
	}
	return manifest, filepath.Join(t.TempDir(), "results")
}

func fakeBatchMaven(t *testing.T, fail bool) {
	t.Helper()
	body := `if [ "$1" = "--version" ]; then echo 'Apache Maven 3.9.11'; echo 'Java version: 21'; exit 0; fi
echo 'build executed'`
	if fail {
		body += "\nexit 1"
	}
	fakeRecipeCommand(t, body)
}

func TestBatchPreviewResumeCapturedPolicyAndRejectStaleVerifiedInputs(t *testing.T) {
	manifest, output := batchFixture(t)
	policy := DefaultPolicy()
	report, err := RunBatch(context.Background(), manifest, output, BatchOptions{Policy: policy, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range report.Results {
		if row.Status != "planned" || readTestFile(t, row.Repo, "pom.xml") != `<project/>` {
			t.Fatal("preview changed a repository", row)
		}
	}
	fakeBatchMaven(t, false)
	other, _ := PolicyForJava(25)
	report, err = RunBatch(context.Background(), manifest, output, BatchOptions{Policy: other, Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range report.Results {
		if row.Status != "verified" || row.Java != 21 || !strings.Contains(readTestFile(t, row.Repo, "pom.xml"), ">21<") {
			t.Fatal("resume used new policy", row)
		}
	}
	fakeBatchMaven(t, true)
	if _, err := RunBatch(context.Background(), manifest, output, BatchOptions{Resume: true}); err != nil {
		t.Fatal("verified resume reran build", err)
	}
	writeTestFile(t, manifest.Repositories[0].Repo, "src/main/java/New.java", "invalid new source")
	report, err = RunBatch(context.Background(), manifest, output, BatchOptions{Resume: true})
	if err == nil || report.Results[0].Status != "failed" || report.Results[1].Status != "verified" {
		t.Fatal("stale verified inventory accepted", report, err)
	}
}

func TestBatchKeepsRecoveryPlansOnBuildFailureAndResumes(t *testing.T) {
	manifest, output := batchFixture(t)
	fakeBatchMaven(t, true)
	report, err := RunBatch(context.Background(), manifest, output, BatchOptions{Policy: DefaultPolicy()})
	if err == nil {
		t.Fatal("failed builds were successful")
	}
	for _, row := range report.Results {
		if row.Status != "failed" || row.Stage != "verify" {
			t.Fatal(row)
		}
		if _, err := ReadPlan(row.Plan); err != nil {
			t.Fatal("lost recovery plan", err)
		}
		if _, err := os.Stat(row.Log); err != nil {
			t.Fatal(err)
		}
	}
	fakeBatchMaven(t, false)
	report, err = RunBatch(context.Background(), manifest, output, BatchOptions{Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range report.Results {
		if row.Status != "verified" {
			t.Fatal(row)
		}
	}
}

func TestBatchRefusesDuplicateManifestAndOutputInsideInputs(t *testing.T) {
	manifest, output := batchFixture(t)
	manifest.Repositories[1].Repo = manifest.Repositories[0].Repo
	if _, err := RunBatch(context.Background(), manifest, output, BatchOptions{Policy: DefaultPolicy()}); err == nil {
		t.Fatal("duplicate repository accepted")
	}
	manifest, _ = batchFixture(t)
	if _, err := RunBatch(context.Background(), manifest, filepath.Join(manifest.Repositories[0].Repo, "reports"), BatchOptions{Policy: DefaultPolicy()}); err == nil {
		t.Fatal("batch output included in source inputs")
	}
}

func TestMixedBuildRootExplicitSelection(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project/>`)
	writeTestFile(t, root, "build.gradle", `plugins { id 'java' }`)
	if _, err := CreatePlan(root, DefaultPolicy()); err == nil {
		t.Fatal("mixed root was guessed")
	}
	for _, build := range []string{"maven", "gradle"} {
		policy := DefaultPolicy()
		policy.Build = build
		p := testPlan(t, root, policy)
		if p.Build != build || p.HasBlockers() {
			t.Fatal(p)
		}
		if err := Apply(p, false); err != nil {
			t.Fatal(err)
		}
		if err := Apply(p, true); err != nil {
			t.Fatal(err)
		}
	}
}
