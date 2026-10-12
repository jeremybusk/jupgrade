package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
)

func lock(root string) (func(), error) {
	path := filepath.Join(root, ".jupgrade.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("acquire migration lock (remove a stale .jupgrade.lock after checking no process is active): %w", err)
	}
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
}

func atomicWrite(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".jupgrade-write-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(info.Mode().Perm()); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Apply validates every input before any write. Each file is replaced atomically;
// interrupted multi-file runs can be resumed or explicitly rolled back.
func Apply(p Plan, rollback bool) error {
	if err := p.Validate(); err != nil {
		return err
	}
	unlock, err := lock(p.Root)
	if err != nil {
		return err
	}
	defer unlock()
	files := map[string]File{}
	for _, f := range p.Files {
		files[f.Path] = f
	}
	current, err := CreatePlan(p.Root, p.Policy)
	if err != nil {
		return err
	}
	if !rollback {
		if p.HasBlockers() || current.HasBlockers() {
			return fmt.Errorf("plan contains blockers; resolve them and create a new plan")
		}
		for _, f := range current.Files {
			planned, ok := files[f.Path]
			if !ok || planned.After != f.After {
				return fmt.Errorf("migration inventory changed: %s; create a new plan", f.Path)
			}
		}
	}
	if len(current.Inputs) != len(p.Inputs) {
		return fmt.Errorf("repository input inventory changed; create a new plan")
	}
	for relative, expected := range p.Inputs {
		actual, ok := current.Inputs[relative]
		if !ok {
			return fmt.Errorf("input disappeared: %s", relative)
		}
		if actual != expected {
			f, edited := files[relative]
			if !edited || actual != f.AfterSHA {
				return fmt.Errorf("stale plan: %s changed after planning; no files written", relative)
			}
		}
	}
	for _, f := range p.Files {
		path, err := safePath(p.Root, f.Path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		from, to, content := f.BeforeSHA, f.AfterSHA, f.After
		if rollback {
			from, to, content = f.AfterSHA, f.BeforeSHA, f.Before
		}
		actual := hash(data)
		if actual == to {
			continue
		}
		if actual != from {
			return fmt.Errorf("%s changed during apply; completed files remain resumable", f.Path)
		}
		if err := atomicWrite(path, []byte(content)); err != nil {
			return fmt.Errorf("write %s (run apply again to resume, or rollback): %w", f.Path, err)
		}
	}
	return nil
}
