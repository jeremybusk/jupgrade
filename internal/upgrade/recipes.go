package upgrade

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const rewriteMavenPlugin = "org.openrewrite.maven:rewrite-maven-plugin:6.46.1"
const rewriteGradlePlugin = "org.openrewrite:plugin:7.41.0"

type RecipeSpec struct {
	Name         string `json:"name"`
	ActiveRecipe string `json:"active_recipe"`
	Artifact     string `json:"artifact"`
	Java         int    `json:"java,omitempty"`
	BootFamily   string `json:"boot_family,omitempty"`
}

func RecipeCatalog() []RecipeSpec {
	return []RecipeSpec{
		{"string-format", "org.openrewrite.java.migrate.lang.StringFormatted", "org.openrewrite.recipe:rewrite-migrate-java:3.42.1", 0, ""},
		{"base64", "org.openrewrite.java.migrate.UseJavaUtilBase64", "org.openrewrite.recipe:rewrite-migrate-java:3.42.1", 0, ""},
		{"java17", "org.openrewrite.java.migrate.UpgradeToJava17", "org.openrewrite.recipe:rewrite-migrate-java:3.42.1", 17, ""},
		{"java21", "org.openrewrite.java.migrate.UpgradeToJava21", "org.openrewrite.recipe:rewrite-migrate-java:3.42.1", 21, ""},
		{"java25", "org.openrewrite.java.migrate.UpgradeToJava25", "org.openrewrite.recipe:rewrite-migrate-java:3.42.1", 25, ""},
		{"boot3.5", "org.openrewrite.java.spring.boot3.UpgradeSpringBoot_3_5", "org.openrewrite.recipe:rewrite-spring:6.37.1", 0, "3.5"},
		{"boot4.0", "org.openrewrite.java.spring.boot4.UpgradeSpringBoot_4_0", "org.openrewrite.recipe:rewrite-spring:6.37.1", 0, "4.0"},
	}
}
func recipeSpecs(names []string) ([]RecipeSpec, error) {
	catalog := map[string]RecipeSpec{}
	for _, spec := range RecipeCatalog() {
		catalog[spec.Name] = spec
	}
	var specs []RecipeSpec
	seen := map[string]bool{}
	for _, name := range names {
		spec, ok := catalog[name]
		if !ok {
			return nil, fmt.Errorf("unknown recipe %q; use jupgrade recipes", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate recipe %s", name)
		}
		seen[name] = true
		specs = append(specs, spec)
	}
	return specs, nil
}

type RecipeArtifact struct {
	Coordinate string `json:"coordinate"`
	SHA256     string `json:"sha256"`
}

// RecipeRun contains the isolated worker's captured edits. Replaying a plan never
// launches a build or needs worker artifacts; source edits stay constrained to
// existing inventoried source/build/configuration paths.
type RecipeRun struct {
	Build        string            `json:"build"`
	PolicySHA256 string            `json:"policy_sha256"`
	BuildFiles   []string          `json:"build_files"`
	Inputs       map[string]string `json:"inputs"`
	Files        []File            `json:"files"`
	Artifacts    []RecipeArtifact  `json:"artifacts"`
	CacheInputs  map[string]string `json:"cache_inputs"`
	SHA256       string            `json:"sha256"`
}

func recipePolicyHash(p Policy) string {
	p.RecipeRun = nil
	data, _ := json.Marshal(p)
	return hash(data)
}
func recipeRunHash(run RecipeRun) string {
	run.SHA256 = ""
	data, _ := json.Marshal(run)
	return hash(data)
}

func sourceRecipePath(path string) bool {
	switch filepath.Ext(path) {
	case ".java", ".kt", ".groovy":
		return true
	}
	normalized := filepath.ToSlash(path)
	if !strings.HasPrefix(normalized, "src/") && !strings.Contains(normalized, "/src/") {
		return false
	}
	switch filepath.Ext(path) {
	case ".properties", ".yaml", ".yml", ".xml", ".json", ".factories", ".imports":
		return true
	}
	return false
}
func validateRecipePolicy(p Policy) error {
	specs, err := recipeSpecs(p.Recipes)
	if err != nil {
		return err
	}
	for _, spec := range specs {
		if spec.Java != 0 && spec.Java != p.Java {
			return fmt.Errorf("recipe %s requires --java %d", spec.Name, spec.Java)
		}
		if spec.BootFamily != "" {
			found := false
			for _, v := range []string{p.Parents["org.springframework.boot:spring-boot-starter-parent"], p.Dependencies["org.springframework.boot:spring-boot-dependencies"], p.GradlePlugins["org.springframework.boot"]} {
				if strings.HasPrefix(v, spec.BootFamily+".") && numericVersion(v) {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("recipe %s requires an exact --spring-boot %s.x target", spec.Name, spec.BootFamily)
			}
		}
	}
	run := p.RecipeRun
	if run == nil {
		return nil
	}
	if len(specs) == 0 {
		return fmt.Errorf("recipe results require selected recipes")
	}
	if run.PolicySHA256 != recipePolicyHash(p) || run.SHA256 != recipeRunHash(*run) {
		return fmt.Errorf("recipe result receipt does not match policy/content")
	}
	if len(run.Inputs) == 0 || len(run.Artifacts) == 0 || len(run.BuildFiles) == 0 || len(run.CacheInputs) == 0 {
		return fmt.Errorf("recipe receipt lacks input/artifact provenance")
	}
	if run.Build != "maven" && run.Build != "gradle" {
		return fmt.Errorf("recipe receipt requires a supported build")
	}
	expectedArtifacts := map[string]bool{}
	plugin := rewriteMavenPlugin
	if run.Build == "gradle" {
		plugin = rewriteGradlePlugin
	}
	expectedArtifacts[plugin] = true
	for _, spec := range specs {
		expectedArtifacts[spec.Artifact] = true
	}
	for _, artifact := range run.Artifacts {
		if !expectedArtifacts[artifact.Coordinate] || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(artifact.SHA256) {
			return fmt.Errorf("invalid or unexpected recipe artifact %s", artifact.Coordinate)
		}
		delete(expectedArtifacts, artifact.Coordinate)
	}
	if len(expectedArtifacts) != 0 {
		return fmt.Errorf("recipe receipt is missing pinned artifacts")
	}
	buildFiles := map[string]bool{}
	for _, path := range run.BuildFiles {
		if buildFiles[path] || run.Inputs[path] == "" {
			return fmt.Errorf("invalid recorded recipe build input %s", path)
		}
		buildFiles[path] = true
	}
	seen := map[string]bool{}
	for _, file := range run.Files {
		if seen[file.Path] {
			return fmt.Errorf("duplicate recipe result %s", file.Path)
		}
		seen[file.Path] = true
		if !sourceRecipePath(file.Path) && !editableBuildPath(file.Path, "maven", p) && !editableBuildPath(file.Path, "gradle", p) && !recipeBuildPath(file.Path) {
			return fmt.Errorf("unsupported source recipe output %s", file.Path)
		}
		if hash([]byte(file.Before)) != file.BeforeSHA || hash([]byte(file.After)) != file.AfterSHA || run.Inputs[file.Path] != file.BeforeSHA {
			return fmt.Errorf("recipe result content/hash mismatch: %s", file.Path)
		}
	}
	return nil
}

func replayRecipes(originals map[string][]byte, p Policy) (map[string][]byte, error) {
	result := map[string][]byte{}
	for path, data := range originals {
		result[path] = data
	}
	if p.RecipeRun == nil {
		return result, nil
	}
	outputs := map[string]File{}
	for _, file := range p.RecipeRun.Files {
		outputs[file.Path] = file
	}
	if len(originals) != len(p.RecipeRun.Inputs) {
		return nil, fmt.Errorf("source recipe input inventory changed; rerun recipe planning")
	}
	for path, expected := range p.RecipeRun.Inputs {
		data, ok := originals[path]
		if !ok {
			return nil, fmt.Errorf("source recipe input disappeared: %s", path)
		}
		actual := hash(data)
		if file, edited := outputs[path]; edited {
			if actual != file.BeforeSHA && actual != file.AfterSHA {
				return nil, fmt.Errorf("source recipe input changed: %s", path)
			}
			result[path] = []byte(file.After)
		} else if actual != expected {
			return nil, fmt.Errorf("source recipe context changed: %s", path)
		}
	}
	return result, nil
}

func recipeBuildPath(path string) bool {
	name := filepath.Base(path)
	return name == "pom.xml" || name == "build.gradle" || name == "build.gradle.kts" || name == "settings.gradle" || name == "settings.gradle.kts" || path == mavenWrapperProperties || path == gradleWrapperProperties || path == "gradle/libs.versions.toml"
}
