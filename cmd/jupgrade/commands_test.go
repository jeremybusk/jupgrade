package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"jupgrade/internal/upgrade"
)

func TestUpgradeDryRunCentralFlagsAndBuildPreference(t *testing.T) {
	cleanConfigEnv(t)
	root := t.TempDir()
	before := `<project/>`
	if err := os.WriteFile(filepath.Join(root, "pom.xml"), []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "build.gradle"), []byte(`plugins { id 'java' }`), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := run([]string{"upgrade", "--repo", root, "--build", "maven", "--out", path, "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "pom.xml"))
	if string(data) != before {
		t.Fatal("dry-run applied changes")
	}
	p, err := upgrade.ReadPlan(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Build != "maven" || p.Policy.Build != "maven" {
		t.Fatal("build choice was not captured")
	}
	if err := run([]string{"diff", "--plan", path, "--format", "summary"}); err != nil {
		t.Fatal(err)
	}
}

func TestBatchCLIUsesSavedPolicyOnResume(t *testing.T) {
	cleanConfigEnv(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pom.xml"), []byte(`<project/>`), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	data, _ := json.Marshal(upgrade.BatchManifest{Schema: 1, Repositories: []upgrade.BatchEntry{{Name: "service", Repo: root}}})
	if err := os.WriteFile(manifest, data, 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "batch")
	if err := run([]string{"batch", "--manifest", manifest, "--out", output, "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JUPGRADE_JAVA", "invalid")
	if err := run([]string{"batch", "--manifest", manifest, "--out", output, "--resume", "--dry-run"}); err != nil {
		t.Fatal("resume parsed changed policy env", err)
	}
	data, _ = os.ReadFile(filepath.Join(root, "pom.xml"))
	if string(data) != `<project/>` {
		t.Fatal("batch preview wrote source")
	}
}

func TestBashCompletionOffersTargetsAndPreservesPathsWithSpaces(t *testing.T) {
	cleanConfigEnv(t)
	script, err := completionScript("bash")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", "-n")
	command.Stdin = strings.NewReader(script)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	command = exec.Command("bash", "-c", script+"\nCOMP_WORDS=(jupgrade plan --java 2); COMP_CWORD=3; _jupgrade_complete; printf '%s\\n' \"${COMPREPLY[@]}\"")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "21\n25\n" {
		t.Fatal(string(out))
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "repo with spaces"), 0755); err != nil {
		t.Fatal(err)
	}
	command = exec.Command("bash", "-c", script+"\nCOMP_WORDS=(jupgrade plan --repo 'repo w'); COMP_CWORD=3; _jupgrade_complete; printf '%s\\n' \"${COMPREPLY[@]}\"")
	command.Dir = root
	out, err = command.CombinedOutput()
	if err != nil || string(out) != "repo with spaces\n" {
		t.Fatal("completion split a path", string(out), err)
	}
	for _, shell := range []string{"zsh", "fish"} {
		script, err := completionScript(shell)
		if err != nil || !strings.Contains(script, "maven-settings") || !strings.Contains(script, "batch") {
			t.Fatal(shell, err)
		}
	}
}
