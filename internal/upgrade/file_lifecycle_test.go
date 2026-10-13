package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func lifecyclePlan(t *testing.T) Plan {
	t.Helper()
	fakeRecipeCommand(t, `python3 - <<'PY'
from pathlib import Path
old=Path('src/main/java/Old.java')
new=Path('src/main/java/moved/New.java')
new.parent.mkdir(parents=True,exist_ok=True)
if old.exists():
    new.write_text(old.read_text().replace('Old','New'))
    old.unlink()
empty=Path('src/main/resources/new/empty.properties')
empty.parent.mkdir(parents=True,exist_ok=True)
empty.touch()
PY`)
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project/>`)
	writeTestFile(t, root, "src/main/java/Old.java", `class Old {}`)
	if err := os.Chmod(filepath.Join(root, "src/main/java/Old.java"), 0640); err != nil {
		t.Fatal(err)
	}
	policy := DefaultPolicy()
	policy.Recipes = []string{"base64"}
	p, err := CreateRecipePlan(context.Background(), root, policy, VerifyOptions{MavenRepo: fakeRecipeCache(t)})
	if err != nil {
		t.Fatal(err)
	}
	if p.Schema != 5 || p.HasBlockers() {
		t.Fatal(p.Schema, p.Findings)
	}
	if readTestFile(t, root, "src/main/java/Old.java") != `class Old {}` {
		t.Fatal("worker edited original")
	}
	return p
}

func TestRecipeFileLifecycleApplyResumeVerifyInventoryAndRollback(t *testing.T) {
	p := lifecyclePlan(t)
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := WritePlan(path, p); err != nil {
		t.Fatal(err)
	}
	p, err := ReadPlan(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an interruption after one destination has been created.
	for _, file := range p.Files {
		if file.Action == "create" && file.After != "" {
			if err := atomicCreate(p.Root, file.Path, []byte(file.After), file.AfterMode); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.Root, "src/main/java/Old.java")); !os.IsNotExist(err) {
		t.Fatal("source was not deleted", err)
	}
	if _, err := os.Stat(filepath.Join(p.Root, "src/main/resources/new/empty.properties")); err != nil {
		t.Fatal("empty file was not created", err)
	}
	if err := checkApplied(p); err != nil {
		t.Fatal(err)
	}
	if next := testPlan(t, p.Root, p.Policy); len(next.Files) != 0 || next.HasBlockers() {
		t.Fatal("lifecycle did not converge", next.Files, next.Findings)
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, p.Root, "src/main/java/Old.java") != `class Old {}` {
		t.Fatal("rename rollback lost source")
	}
	info, err := os.Stat(filepath.Join(p.Root, "src/main/java/Old.java"))
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatal("lost original permissions", err)
	}
	for _, dir := range p.Policy.RecipeRun.CreatedDirs {
		if _, err := os.Stat(filepath.Join(p.Root, dir)); !os.IsNotExist(err) {
			t.Fatal("created directory not cleaned", dir, err)
		}
	}
	if next := testPlan(t, p.Root, p.Policy); len(next.Files) != len(p.Files) {
		t.Fatal("cannot replay after rollback")
	}
}

func TestRecipeLifecycleRejectsConflictsBeforeAnyWrites(t *testing.T) {
	for _, conflict := range []string{"content", "permissions", "symlink", "inventory"} {
		t.Run(conflict, func(t *testing.T) {
			p := lifecyclePlan(t)
			switch conflict {
			case "content":
				writeTestFile(t, p.Root, "src/main/java/moved/New.java", "user change")
			case "permissions":
				writeTestFile(t, p.Root, "src/main/java/moved/New.java", "class New {}")
				if err := os.Chmod(filepath.Join(p.Root, "src/main/java/moved/New.java"), 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(p.Root, "src/main/java/moved")); err != nil {
					t.Fatal(err)
				}
			case "inventory":
				writeTestFile(t, p.Root, "README.md", "new context")
			}
			if err := Apply(p, false); err == nil {
				t.Fatal("conflicting lifecycle plan applied")
			}
			if readTestFile(t, p.Root, "pom.xml") != `<project/>` || readTestFile(t, p.Root, "src/main/java/Old.java") != `class Old {}` {
				t.Fatal("conflict wrote unrelated files")
			}
		})
	}
}

func TestRecipeLifecycleRejectsOmittedOperationsAndOldSchema(t *testing.T) {
	p := lifecyclePlan(t)
	old := p
	old.Schema = 4
	if err := old.Validate(); err == nil {
		t.Fatal("lifecycle accepted in schema 4")
	}
	for i, file := range p.Files {
		if file.Action == "" {
			continue
		}
		omitted := p
		omitted.Files = append([]File(nil), p.Files[:i]...)
		omitted.Files = append(omitted.Files, p.Files[i+1:]...)
		if err := omitted.Validate(); err == nil {
			t.Fatal("omitted lifecycle action accepted", file.Path)
		}
	}
}
