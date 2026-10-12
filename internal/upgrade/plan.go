package upgrade

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type File struct {
	Path      string `json:"path"`
	BeforeSHA string `json:"before_sha256"`
	AfterSHA  string `json:"after_sha256"`
	Before    string `json:"before"`
	After     string `json:"after"`
}

type Plan struct {
	Schema   int               `json:"schema"`
	Root     string            `json:"root"`
	Policy   Policy            `json:"policy"`
	Files    []File            `json:"files"`
	Inputs   map[string]string `json:"inputs"`
	Changes  []Change          `json:"changes"`
	Findings []Finding         `json:"findings"`
}

func projectCoordinate(data []byte, root *element) string {
	g := textAt(data, root, "groupId")
	if g == "" {
		if parent := root.child("parent"); parent != nil {
			g = textAt(data, parent, "groupId")
		}
	}
	return g + ":" + textAt(data, root, "artifactId")
}

func projectVersion(data []byte, root *element) string {
	v := textAt(data, root, "version")
	if v == "" {
		if parent := root.child("parent"); parent != nil {
			v = textAt(data, parent, "version")
		}
	}
	return v
}

func parentMatches(data []byte, root *element, parentData []byte, parentRoot *element) bool {
	n := root.child("parent")
	if n == nil {
		return false
	}
	return coordinate(data, n) == projectCoordinate(parentData, parentRoot) && textAt(data, n, "version") == projectVersion(parentData, parentRoot)
}

func parentCandidate(path string, data []byte, root *element) (string, bool) {
	n := root.child("parent")
	if n == nil {
		return "", false
	}
	relative := "../pom.xml"
	if r := n.child("relativePath"); r != nil {
		relative = scalar(data, r)
		if relative == "" {
			return "", false
		}
	}
	if filepath.IsAbs(relative) || strings.Contains(relative, "${") {
		return "", false
	}
	p := filepath.Clean(filepath.Join(filepath.Dir(path), relative))
	if filepath.Ext(p) != ".xml" {
		p = filepath.Join(p, "pom.xml")
	}
	if p == ".." || strings.HasPrefix(p, ".."+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}

func hash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func canonicalRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository root is not a directory")
	}
	return abs, nil
}

func safePath(root, relative string) (string, error) {
	if filepath.IsAbs(relative) || relative == "." || filepath.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe repository path: %q", relative)
	}
	path := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink is not a migration input: %s", relative)
		}
	}
	return path, nil
}

// Follow Maven's declared reactor instead of treating every nested POM as a
// module. Include profile modules; verify reports which profiles Maven tests.
func discoverPOMs(root string) ([]string, error) {
	seen := map[string]bool{}
	var visit func(string) error
	visit = func(relative string) error {
		if seen[relative] {
			return nil
		}
		seen[relative] = true
		path, err := safePath(root, relative)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		n, err := parseXML(data)
		if err != nil {
			return fmt.Errorf("%s: %w", relative, err)
		}
		var modules []string
		walk(n, func(c *element) {
			if c.Name == "module" && c.Parent != nil && c.Parent.Name == "modules" && c.Parent.Parent != nil && (c.Parent.Parent == n || c.Parent.Parent.Name == "profile") {
				modules = append(modules, scalar(data, c))
			}
		})
		for _, module := range modules {
			if module == "" || strings.Contains(module, "${") || filepath.IsAbs(module) {
				return fmt.Errorf("%s: unsupported module path %q", relative, module)
			}
			p := filepath.Clean(filepath.Join(filepath.Dir(relative), module))
			if filepath.Ext(p) != ".xml" {
				p = filepath.Join(p, "pom.xml")
			}
			if filepath.Base(p) != "pom.xml" {
				return fmt.Errorf("%s: module POM %q must be named pom.xml", relative, p)
			}
			if err := visit(p); err != nil {
				return err
			}
		}
		if candidate, enabled := parentCandidate(relative, data, n); enabled {
			parentPath, pathErr := safePath(root, candidate)
			if pathErr != nil && !os.IsNotExist(pathErr) {
				return pathErr
			}
			if pathErr == nil {
				parentData, err := os.ReadFile(parentPath)
				if err != nil {
					return err
				}
				parentRoot, err := parseXML(parentData)
				if err != nil {
					return err
				}
				if parentMatches(data, n, parentData, parentRoot) {
					if err := visit(candidate); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	if err := visit("pom.xml"); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

func readRepositoryPOMs(root string) (map[string][]byte, error) {
	paths, err := discoverPOMs(root)
	if err != nil {
		return nil, err
	}
	contents := map[string][]byte{}
	for _, relative := range paths {
		path, err := safePath(root, relative)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		contents[relative] = data
	}
	return contents, nil
}

func transformRepository(originals map[string][]byte, policy Policy) (map[string][]byte, []Change, []Finding, error) {
	contents := map[string][]byte{}
	for path, data := range originals {
		if filepath.Base(path) != "pom.xml" {
			continue
		}
		contents[path] = append([]byte(nil), data...)
	}
	changes, err := updateOwnedProperties(contents, policy)
	if err != nil {
		return nil, nil, nil, err
	}
	model, err := newRepositoryModel(contents)
	if err != nil {
		return nil, nil, nil, err
	}
	findings, err := springBootMigrationFindings(contentsForPOMs(originals), policy)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, path := range model.paths {
		after, actions, issues, err := migratePOMWithModel(contents[path], path, policy, model)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		contents[path] = after
		changes = append(changes, actions...)
		findings = append(findings, issues...)
	}
	afterModel, err := newRepositoryModel(contents)
	if err != nil {
		return nil, nil, nil, err
	}
	findings = append(findings, unresolvedPolicyFindings(afterModel, policy)...)
	findings = append(findings, jettyRuntimeFindings(afterModel, policy.Java)...)
	findings = append(findings, libraryRuntimeFindings(afterModel, policy.Java)...)
	if policy.Maven != "" {
		if original, ok := originals[mavenWrapperProperties]; ok {
			after, actions, issues := migrateMavenWrapper(original, policy)
			contents[mavenWrapperProperties] = after
			changes = append(changes, actions...)
			findings = append(findings, issues...)
		} else {
			findings = append(findings, Finding{"blocker", mavenWrapperProperties, "Maven target requires an existing .mvn/wrapper/maven-wrapper.properties; installing Maven or generating a wrapper is not automated"})
		}
	}
	return contents, changes, findings, nil
}

func CreatePlan(root string, policy Policy) (Plan, error) {
	if err := policy.Validate(); err != nil {
		return Plan{}, err
	}
	root, err := canonicalRoot(root)
	if err != nil {
		return Plan{}, err
	}
	if err := requireMaven(root); err != nil {
		return Plan{}, err
	}
	schema := 2
	if policy.Maven != "" {
		schema = 3
	}
	p := Plan{Schema: schema, Root: root, Policy: policy, Inputs: map[string]string{}, Files: []File{}, Changes: []Change{}, Findings: []Finding{}}
	originals, err := readRepositoryInputs(root, policy)
	if err != nil {
		return p, err
	}
	transformed, changes, findings, err := transformRepository(originals, policy)
	if err != nil {
		return p, err
	}
	p.Changes = append(p.Changes, changes...)
	p.Findings = append(p.Findings, findings...)
	var paths []string
	for path := range originals {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, relative := range paths {
		before := originals[relative]
		p.Inputs[relative] = hash(before)
		after := transformed[relative]
		if !bytes.Equal(before, after) {
			p.Files = append(p.Files, File{relative, hash(before), hash(after), string(before), string(after)})
		}
	}
	if err := scanFindings(&p); err != nil {
		return p, err
	}
	return p, nil
}

func runtimeJDKMismatch(pattern *regexp.Regexp, data []byte, target int) bool {
	for _, match := range pattern.FindAllSubmatch(data, -1) {
		if string(match[2]) != strconv.Itoa(target) {
			return true
		}
	}
	return false
}

var sourceRisks = []struct{ pattern, message string }{
	{`\bjavax\.(xml\.bind|xml\.ws|activation)\b`, "JDK-removed Java EE API: provide compatible external libraries or a framework migration"},
	{`\bsun\.misc\.(BASE64Encoder|BASE64Decoder)\b`, "removed Base64 API: migrate to java.util.Base64"},
	{`\b(sun|com\.sun)\.`, "internal JDK API: review target JDK access and availability"},
	{`\.setAccessible\s*\(\s*true\s*\)`, "reflective access: test against target JDK strong encapsulation"},
}

func scanFindings(p *Plan) error {
	compiled := make([]*regexp.Regexp, len(sourceRisks))
	for i, risk := range sourceRisks {
		compiled[i] = regexp.MustCompile(risk.pattern)
	}
	runtimePin := regexp.MustCompile(`(?i)(java-version|jdk|openjdk|temurin|corretto|JAVA_HOME)[^\r\n]{0,60}\b(1\.8|8|11|17|21|25)\b`)
	return filepath.WalkDir(p.Root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && path != p.Root {
			switch entry.Name() {
			case ".git", "target", ".jupgrade", ".idea", ".gradle", "build", "node_modules":
				return filepath.SkipDir
			}
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		relative, err := filepath.Rel(p.Root, path)
		if err != nil {
			return err
		}
		isJava := strings.HasSuffix(path, ".java")
		isRuntime := entry.Name() == "Dockerfile" || strings.HasPrefix(entry.Name(), "Dockerfile.") || entry.Name() == ".java-version" || entry.Name() == ".sdkmanrc" || entry.Name() == "Jenkinsfile" || strings.HasPrefix(relative, ".github"+string(filepath.Separator)) || strings.HasPrefix(relative, ".mvn"+string(filepath.Separator)) || entry.Name() == "mvnw" || entry.Name() == "mvnw.cmd" || entry.Name() == "toolchains.xml"
		if !isJava && !isRuntime {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		p.Inputs[relative] = hash(data)
		if isJava {
			for i, re := range compiled {
				if re.Match(data) {
					p.Findings = append(p.Findings, Finding{"warning", relative, sourceRisks[i].message})
				}
			}
		} else if runtimeJDKMismatch(runtimePin, data, p.Policy.Java) || (entry.Name() == ".java-version" && strings.TrimSpace(string(data)) != strconv.Itoa(p.Policy.Java)) {
			p.Findings = append(p.Findings, Finding{"warning", relative, fmt.Sprintf("review runtime/CI JDK pin for Java %d; this file is not edited", p.Policy.Java)})
		}
		return nil
	})
}

func WritePlan(path string, p Plan) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeNewFile(path, data)
}

func writeNewFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return writeErr
	}
	return closeErr
}

func ReadPlan(path string) (Plan, error) {
	f, err := os.Open(path)
	if err != nil {
		return Plan{}, err
	}
	defer f.Close()
	var p Plan
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return p, fmt.Errorf("plan must contain one JSON object")
	}
	return p, p.Validate()
}

func (p Plan) Validate() error {
	if p.Schema != 1 && p.Schema != 2 && p.Schema != 3 {
		return fmt.Errorf("unsupported plan schema %d", p.Schema)
	}
	if p.Policy.Maven != "" && p.Schema < 3 {
		return fmt.Errorf("Maven wrapper policies require plan schema 3")
	}
	if err := p.Policy.Validate(); err != nil {
		return err
	}
	root, err := canonicalRoot(p.Root)
	if err != nil {
		return err
	}
	if root != p.Root {
		return fmt.Errorf("plan root is not canonical")
	}
	if len(p.Inputs) == 0 {
		return fmt.Errorf("plan has no input inventory")
	}
	seen := map[string]bool{}
	originals := map[string][]byte{}
	for _, file := range p.Files {
		if seen[file.Path] {
			return fmt.Errorf("duplicate file in plan: %s", file.Path)
		}
		seen[file.Path] = true
		if _, err := safePath(root, file.Path); err != nil {
			return err
		}
		if hash([]byte(file.Before)) != file.BeforeSHA || hash([]byte(file.After)) != file.AfterSHA || p.Inputs[file.Path] != file.BeforeSHA {
			return fmt.Errorf("content/hash mismatch: %s", file.Path)
		}
		if filepath.Base(file.Path) != "pom.xml" && !(p.Schema == 3 && p.Policy.Maven != "" && file.Path == mavenWrapperProperties) {
			return fmt.Errorf("plan may edit only POMs and an explicitly targeted Maven wrapper")
		}
		// A plan is reviewable data, but should not become a generic arbitrary
		// file writer when modified by hand.
		originals[file.Path] = []byte(file.Before)
		if p.Schema == 1 {
			expected, _, _, err := migratePOM([]byte(file.Before), file.Path, p.Policy)
			if err != nil {
				return err
			}
			if !bytes.Equal(expected, []byte(file.After)) {
				return fmt.Errorf("plan content does not match policy: %s", file.Path)
			}
		}
	}
	if p.Schema >= 2 {
		for relative, h := range p.Inputs {
			if seen[relative] || (filepath.Base(relative) != "pom.xml" && !(p.Schema == 3 && p.Policy.Maven != "" && relative == mavenWrapperProperties)) {
				continue
			}
			path, err := safePath(root, relative)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if hash(data) != h {
				return fmt.Errorf("stale plan: %s changed after planning", relative)
			}
			originals[relative] = data
		}
		expected, _, _, err := transformRepository(originals, p.Policy)
		if err != nil {
			return err
		}
		for _, f := range p.Files {
			if !bytes.Equal(expected[f.Path], []byte(f.After)) {
				return fmt.Errorf("plan content does not match repository policy: %s", f.Path)
			}
		}
		for path, data := range expected {
			if !seen[path] && !bytes.Equal(data, originals[path]) {
				return fmt.Errorf("plan omits required changes to %s", path)
			}
		}
	}
	return nil
}

func (p Plan) HasBlockers() bool {
	for _, finding := range p.Findings {
		if finding.Severity == "blocker" {
			return true
		}
	}
	return false
}
