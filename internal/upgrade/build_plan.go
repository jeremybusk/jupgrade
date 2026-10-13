package upgrade

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func readBuildInputs(root string, policy Policy, build string) (map[string][]byte, error) {
	if len(policy.Recipes) > 0 {
		return readRecipeInputs(root)
	}
	if build == "gradle" {
		return readGradleInputs(root)
	}
	return readRepositoryInputs(root, policy)
}
func transformBuild(originals map[string][]byte, policy Policy, build string) (map[string][]byte, []Change, []Finding, error) {
	replayed, err := replayRecipes(originals, policy)
	if err != nil {
		return nil, nil, nil, err
	}
	buildInputs := replayed
	if policy.RecipeRun != nil {
		buildInputs = map[string][]byte{}
		for _, path := range policy.RecipeRun.BuildFiles {
			if data, ok := replayed[path]; ok {
				buildInputs[path] = data
			} else {
				return nil, nil, nil, fmt.Errorf("recorded build input disappeared: %s", path)
			}
		}
	}
	var results map[string][]byte
	var changes []Change
	var findings []Finding
	if build == "gradle" {
		results, changes, findings, err = transformGradle(buildInputs, policy)
	} else {
		results, changes, findings, err = transformRepository(buildInputs, policy)
	}
	if err != nil {
		return nil, nil, nil, err
	}
	for path, data := range replayed {
		if _, ok := results[path]; !ok {
			results[path] = data
		}
	}
	if len(policy.Recipes) > 0 && policy.RecipeRun == nil {
		findings = append(findings, Finding{"blocker", "", "selected source recipes require isolated worker planning"})
	}
	if policy.RecipeRun != nil {
		for _, file := range policy.RecipeRun.Files {
			if !bytes.Equal(originals[file.Path], results[file.Path]) {
				changes = append(changes, Change{file.Path, "replay pinned source recipe result"})
			}
		}
	}
	return results, changes, findings, nil
}

func editableBuildPath(path, build string, policy Policy) bool {
	if build == "maven" {
		return filepath.Base(path) == "pom.xml" || policy.Maven != "" && path == mavenWrapperProperties
	}
	if build == "gradle" {
		return filepath.Base(path) == "build.gradle" || filepath.Base(path) == "build.gradle.kts" || path == "gradle/libs.versions.toml" || path == gradleWrapperProperties
	}
	return false
}
func validateModernPlan(p Plan) error {
	if err := p.Policy.Validate(); err != nil {
		return err
	}
	if p.Policy.RecipeRun != nil && p.Policy.RecipeRun.Build != p.Build {
		return fmt.Errorf("recipe receipt build does not match plan")
	}
	if p.Build != "maven" && p.Build != "gradle" {
		return fmt.Errorf("schema 4 requires a Maven or Gradle build")
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
			return fmt.Errorf("duplicate planned file %s", file.Path)
		}
		seen[file.Path] = true
		if !editableBuildPath(file.Path, p.Build, p.Policy) && !(p.Policy.RecipeRun != nil && (sourceRecipePath(file.Path) || recipeBuildPath(file.Path))) {
			return fmt.Errorf("unsupported planned file %s", file.Path)
		}
		if _, err := safePath(root, file.Path); err != nil {
			return err
		}
		if hash([]byte(file.Before)) != file.BeforeSHA || hash([]byte(file.After)) != file.AfterSHA || p.Inputs[file.Path] != file.BeforeSHA {
			return fmt.Errorf("content/hash mismatch: %s", file.Path)
		}
		originals[file.Path] = []byte(file.Before)
	}
	for path, expected := range p.Inputs {
		if seen[path] {
			continue
		}
		full, err := safePath(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		if hash(data) != expected {
			return fmt.Errorf("stale plan: %s changed after planning", path)
		}
		if len(p.Policy.Recipes) > 0 || editableBuildPath(path, p.Build, p.Policy) || p.Build == "gradle" && (strings.HasPrefix(filepath.Base(path), "settings.gradle") || path == "gradle.properties") {
			originals[path] = data
		}
	}
	transformed, _, _, err := transformBuild(originals, p.Policy, p.Build)
	if err != nil {
		return err
	}
	for _, file := range p.Files {
		if !bytes.Equal(transformed[file.Path], []byte(file.After)) {
			return fmt.Errorf("plan content does not match policy: %s", file.Path)
		}
	}
	for path, data := range transformed {
		if !seen[path] && !bytes.Equal(data, originals[path]) {
			return fmt.Errorf("plan omits required changes to %s", path)
		}
	}
	return nil
}

func inspectGradle(root string, policy Policy) (Inspection, error) {
	contents, err := readGradleInputs(root)
	if err != nil {
		return Inspection{}, err
	}
	report := Inspection{Root: root, Build: "gradle", POMs: []string{}, Artifacts: []Artifact{}}
	// Inspection includes concrete dependency/plugin strings without executing DSL.
	for _, path := range sortedContentPaths(contents) {
		data := contents[path]
		if !strings.HasSuffix(path, ".gradle") && !strings.HasSuffix(path, ".gradle.kts") {
			continue
		}
		tokens, err := lexBuild(data, false)
		if err != nil {
			return report, fmt.Errorf("%s: %w", path, err)
		}
		pairs, err := matchingTokens(tokens)
		if err != nil {
			return report, err
		}
		blocks := buildBlocks(tokens, pairs, "dependencies")
		for i, t := range tokens {
			if t.kind != "string" || !insideBlocks(i, blocks) {
				continue
			}
			parts := strings.Split(t.value, ":")
			if len(parts) < 2 || len(parts) > 4 {
				continue
			}
			a := Artifact{Path: path, Kind: "dependency", Coordinate: parts[0] + ":" + parts[1]}
			if len(parts) > 2 {
				a.Declared = parts[2]
				if !strings.Contains(a.Declared, "$") {
					a.Resolved = a.Declared
				} else {
					a.Problem = "property expression requires Gradle model resolution"
				}
			} else {
				a.Problem = "versionless/managed dependency"
			}
			report.Artifacts = append(report.Artifacts, a)
		}
	}
	return report, nil
}

func sortedContentPaths(contents map[string][]byte) []string {
	paths := make([]string, 0, len(contents))
	for path := range contents {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
