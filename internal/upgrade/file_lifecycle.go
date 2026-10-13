package upgrade

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

func validRelativePath(path string) bool {
	return path != "" && !filepath.IsAbs(path) && path != "." && filepath.Clean(path) == path && path != ".." && !strings.HasPrefix(path, ".."+string(filepath.Separator))
}

// Unlike safePath, absent leaves and ancestors are permitted. Existing symlinks
// and non-directory ancestors are rejected before any filesystem mutation.
func optionalPath(root, relative string) (string, error) {
	if !validRelativePath(relative) {
		return "", fmt.Errorf("unsafe repository path: %q", relative)
	}
	path := root
	parts := strings.Split(relative, string(filepath.Separator))
	for i, part := range parts {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink is not a migration path: %s", relative)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "", fmt.Errorf("non-directory migration ancestor: %s", relative)
		}
	}
	return path, nil
}

func sameFile(before, after map[string][]byte, path string) bool {
	a, aOK := before[path]
	b, bOK := after[path]
	return aOK == bOK && bytes.Equal(a, b)
}

func diffFiles(before, after map[string][]byte) []File {
	paths := map[string][]byte{}
	for path := range before {
		paths[path] = nil
	}
	for path := range after {
		paths[path] = nil
	}
	files := []File{}
	for _, path := range sortedContentPaths(paths) {
		if sameFile(before, after, path) {
			continue
		}
		a, aOK := before[path]
		b, bOK := after[path]
		file := File{Path: path, Before: string(a), After: string(b)}
		if aOK {
			file.BeforeSHA = hash(a)
		} else {
			file.Action = "create"
		}
		if bOK {
			file.AfterSHA = hash(b)
		} else {
			file.Action = "delete"
		}
		files = append(files, file)
	}
	return files
}

func recipeLifecycle(policy Policy) bool {
	if policy.RecipeRun == nil {
		return false
	}
	for _, file := range policy.RecipeRun.Files {
		if file.Action != "" {
			return true
		}
	}
	return false
}

func validateFileRecord(file File, inputs map[string]string) error {
	if !validRelativePath(file.Path) {
		return fmt.Errorf("unsafe planned path %s", file.Path)
	}
	if file.BeforeMode > 0777 || file.AfterMode > 0777 {
		return fmt.Errorf("unsupported file permissions: %s", file.Path)
	}
	_, input := inputs[file.Path]
	switch file.Action {
	case "":
		if !input || inputs[file.Path] != file.BeforeSHA || hash([]byte(file.Before)) != file.BeforeSHA || hash([]byte(file.After)) != file.AfterSHA {
			return fmt.Errorf("content/hash mismatch: %s", file.Path)
		}
	case "create":
		if input || file.BeforeSHA != "" || file.Before != "" || file.BeforeMode != 0 || file.AfterMode == 0 || hash([]byte(file.After)) != file.AfterSHA {
			return fmt.Errorf("invalid file creation: %s", file.Path)
		}
	case "delete":
		if !input || inputs[file.Path] != file.BeforeSHA || hash([]byte(file.Before)) != file.BeforeSHA || file.AfterSHA != "" || file.After != "" || file.AfterMode != 0 || file.BeforeMode == 0 {
			return fmt.Errorf("invalid file deletion: %s", file.Path)
		}
	default:
		return fmt.Errorf("unknown file action %q", file.Action)
	}
	return nil
}

func fileState(root string, file File) (string, error) {
	path, err := optionalPath(root, file.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("migration path is not a regular file: %s", file.Path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	actual := hash(data)
	mode := uint32(info.Mode().Perm())
	if actual == file.BeforeSHA && file.BeforeMode != 0 && mode != file.BeforeMode || actual == file.AfterSHA && file.AfterMode != 0 && mode != file.AfterMode {
		return "", fmt.Errorf("file permissions changed: %s", file.Path)
	}
	return actual, nil
}

// New files use an atomic no-clobber link from a fully written temporary file.
func atomicCreate(root, relative string, data []byte, mode uint32) error {
	path, err := optionalPath(root, relative)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if _, err := optionalPath(root, relative); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".jupgrade-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(os.FileMode(mode)); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Link(f.Name(), path)
}

func applyLifecycle(p Plan, rollback bool) error {
	current, err := CreatePlan(p.Root, p.Policy)
	if err != nil {
		return err
	}
	if !rollback && (p.HasBlockers() || current.HasBlockers()) {
		return fmt.Errorf("plan contains blockers")
	}
	// Receipt replay validates all context and every before/after file state;
	// preflight permissions for every operation before writing anything.
	for _, file := range p.Files {
		actual, err := fileState(p.Root, file)
		if err != nil {
			return err
		}
		if actual != file.BeforeSHA && actual != file.AfterSHA {
			return fmt.Errorf("stale plan: %s changed; no files written", file.Path)
		}
	}
	files := append([]File(nil), p.Files...)
	// Restore/create destinations before removing sources, including a renamed
	// build script. Interruptions cannot leave the root without its build file.
	sort.SliceStable(files, func(i, j int) bool {
		absent := func(file File) bool {
			if rollback {
				return file.BeforeSHA == ""
			}
			return file.AfterSHA == ""
		}
		return !absent(files[i]) && absent(files[j])
	})
	for _, file := range files {
		from, to, content, mode := file.BeforeSHA, file.AfterSHA, file.After, file.AfterMode
		if rollback {
			from, to, content, mode = file.AfterSHA, file.BeforeSHA, file.Before, file.BeforeMode
		}
		actual, err := fileState(p.Root, file)
		if err != nil {
			return err
		}
		if actual == to {
			continue
		}
		if actual != from {
			return fmt.Errorf("%s changed during apply; completed files remain resumable", file.Path)
		}
		path, err := optionalPath(p.Root, file.Path)
		if err != nil {
			return err
		}
		if to == "" {
			err = os.Remove(path)
		} else if from == "" {
			err = atomicCreate(p.Root, file.Path, []byte(content), mode)
		} else {
			err = atomicWrite(path, []byte(content))
		}
		if err != nil {
			return fmt.Errorf("write %s (resume apply or rollback): %w", file.Path, err)
		}
	}
	if rollback {
		directories := append([]string(nil), p.Policy.RecipeRun.CreatedDirs...)
		sort.Slice(directories, func(i, j int) bool { return len(directories[i]) > len(directories[j]) })
		for _, directory := range directories {
			path, err := optionalPath(p.Root, directory)
			if err != nil {
				return err
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) && !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
				return err
			}
		}
	}
	return nil
}
