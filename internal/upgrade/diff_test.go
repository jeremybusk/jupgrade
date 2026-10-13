package upgrade

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPlanDiffCanBeAppliedAndReversedIncludingEmptyLifecycleFiles(t *testing.T) {
	p := lifecyclePlan(t)
	var patch bytes.Buffer
	if err := WritePlanDiff(&patch, p); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(patch.Bytes(), []byte("new file mode")) || !bytes.Contains(patch.Bytes(), []byte("No newline at end of file")) {
		t.Fatal(patch.String())
	}
	command := exec.Command("git", "apply", "--check", "-")
	command.Dir = p.Root
	command.Stdin = bytes.NewReader(patch.Bytes())
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("patch is invalid: %v\n%s\n%s", err, out, patch.String())
	}
	command = exec.Command("git", "apply", "-")
	command.Dir = p.Root
	command.Stdin = bytes.NewReader(patch.Bytes())
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, file := range p.Files {
		if file.Action == "delete" {
			if _, err := os.Stat(filepath.Join(p.Root, file.Path)); !os.IsNotExist(err) {
				t.Fatal("patch did not delete source")
			}
		} else {
			if readTestFile(t, p.Root, file.Path) != file.After {
				t.Fatal("patch content differs from plan", file.Path)
			}
		}
	}
	command = exec.Command("git", "apply", "--reverse", "-")
	command.Dir = p.Root
	command.Stdin = bytes.NewReader(patch.Bytes())
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("reverse patch: %v\n%s", err, out)
	}
	if readTestFile(t, p.Root, "src/main/java/Old.java") != `class Old {}` {
		t.Fatal("reverse patch lost original")
	}
}

func TestPlanDiffReviewDoesNotNeedOriginalCheckout(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project/>`)
	p := testPlan(t, root, DefaultPolicy())
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := WritePlan(path, p); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPlan(path); err == nil {
		t.Fatal("live validation accepted absent checkout")
	}
	if _, err := ReadPlanForReview(path); err != nil {
		t.Fatal("review required live checkout", err)
	}
}

func TestLineDiffHandlesSeparatedEditsAndBoundsGeneratedFileMemory(t *testing.T) {
	before, after := "", ""
	for i := 0; i < 3000; i++ {
		before += "keep\n"
		after += "keep\n"
	}
	before = "old\n" + before + "end-old\n"
	after = "new\n" + after + "end-new\n"
	ops := lineDiff(before, after)
	var a, b bytes.Buffer
	for _, line := range ops {
		if line.kind != '+' {
			a.WriteString(line.text)
		}
		if line.kind != '-' {
			b.WriteString(line.text)
		}
	}
	if a.String() != before || b.String() != after {
		t.Fatal("large diff did not reconstruct both sides")
	}
}
