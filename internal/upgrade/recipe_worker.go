package upgrade

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func ignoredWorkDirectory(name string) bool {
	switch name {
	case ".git", "target", "build", ".gradle", ".jupgrade", ".idea", "node_modules", "out", ".rewrite":
		return true
	}
	return false
}
func readRecipeInputs(root string) (map[string][]byte, error) {
	inputs := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && ignoredWorkDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == ".jupgrade.lock" {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("source worker input may not be a symlink: %s", relative)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("source worker input is not regular: %s", relative)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		inputs[relative] = data
		return nil
	})
	return inputs, err
}

func workerEnvironment(options VerifyOptions) ([]string, error) {
	env := os.Environ()
	set := func(key, value string) {
		filtered := make([]string, 0, len(env))
		for _, old := range env {
			if !strings.HasPrefix(old, key+"=") {
				filtered = append(filtered, old)
			}
		}
		env = append(filtered, key+"="+value)
	}
	if options.JavaHome != "" {
		home, err := filepath.Abs(options.JavaHome)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(home, "bin", "java")); err != nil {
			return nil, err
		}
		set("JAVA_HOME", home)
		set("PATH", filepath.Join(home, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if options.GradleUserHome != "" {
		home, err := filepath.Abs(options.GradleUserHome)
		if err != nil {
			return nil, err
		}
		set("GRADLE_USER_HOME", home)
	}
	return env, nil
}
func artifactEvidence(cache, gav string) (RecipeArtifact, error) {
	parts := strings.Split(gav, ":")
	if len(parts) != 3 {
		return RecipeArtifact{}, fmt.Errorf("invalid worker artifact %s", gav)
	}
	path := filepath.Join(cache, filepath.FromSlash(strings.ReplaceAll(parts[0], ".", "/")), parts[1], parts[2], parts[1]+"-"+parts[2]+".jar")
	data, err := os.ReadFile(path)
	if err != nil {
		return RecipeArtifact{}, fmt.Errorf("worker artifact %s must be cached before planning: %w", gav, err)
	}
	return RecipeArtifact{gav, hash(data)}, nil
}
func cacheEvidence(cache string) (map[string]string, error) {
	result := map[string]string{}
	err := filepath.WalkDir(cache, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		extension := filepath.Ext(path)
		if extension != ".pom" && extension != ".jar" && extension != ".xml" {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("recipe cache artifact may not be a symlink: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(cache, path)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = hash(data)
		return nil
	})
	return result, err
}

// CreateRecipePlan runs pinned recipes on a copy and freezes the result into an
// ordinary plan. Unsupported paths and nonconvergent recipes fail
// without changing the repository.
func CreateRecipePlan(ctx context.Context, root string, policy Policy, options VerifyOptions) (Plan, error) {
	if len(policy.Recipes) == 0 || policy.RecipeRun != nil {
		return CreatePlan(root, policy)
	}
	if err := policy.Validate(); err != nil {
		return Plan{}, err
	}
	root, err := canonicalRoot(root)
	if err != nil {
		return Plan{}, err
	}
	build, err := detectBuild(root)
	if err != nil {
		return Plan{}, err
	}
	if build == "gradle" && policy.Gradle == "" {
		policy.Gradle = gradleTarget(policy.Java)
	}
	baseBefore := policy
	baseBefore.Recipes = nil
	baseBefore.RecipeRun = nil
	beforePlan, err := CreatePlan(root, baseBefore)
	if err != nil {
		return Plan{}, err
	}
	for _, finding := range beforePlan.Findings {
		if finding.Severity == "blocker" && (strings.Contains(finding.Message, "already exceeds Java") || strings.Contains(finding.Message, "exceeds requested Java")) {
			return Plan{}, fmt.Errorf("source recipes cannot downgrade Java: %s", finding.Message)
		}
	}
	inputs, err := readRecipeInputs(root)
	if err != nil {
		return Plan{}, err
	}
	cache := options.MavenRepo
	if cache == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Plan{}, err
		}
		cache = filepath.Join(home, ".m2", "repository")
	}
	cache, err = canonicalRoot(cache)
	if err != nil {
		return Plan{}, err
	}
	specs, err := recipeSpecs(policy.Recipes)
	if err != nil {
		return Plan{}, err
	}
	gavs := map[string]bool{}
	plugin := rewriteMavenPlugin
	if build == "gradle" {
		plugin = rewriteGradlePlugin
	}
	gavs[plugin] = true
	for _, spec := range specs {
		gavs[spec.Artifact] = true
	}
	keys := make([]string, 0, len(gavs))
	for gav := range gavs {
		keys = append(keys, gav)
	}
	sort.Strings(keys)
	var artifacts []RecipeArtifact
	for _, gav := range keys {
		evidence, err := artifactEvidence(cache, gav)
		if err != nil {
			return Plan{}, err
		}
		artifacts = append(artifacts, evidence)
	}
	scratch, err := os.MkdirTemp("", "jupgrade-recipe-")
	if err != nil {
		return Plan{}, err
	}
	defer os.RemoveAll(scratch)
	copyRoot := filepath.Join(scratch, "repo")
	if err := os.Mkdir(copyRoot, 0755); err != nil {
		return Plan{}, err
	}
	if err := copyWorktree(root, copyRoot, inputs); err != nil {
		return Plan{}, err
	}
	if options.Timeout <= 0 {
		options.Timeout = 20 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	env, err := workerEnvironment(options)
	if err != nil {
		return Plan{}, err
	}
	execute := func() error { return executeRecipes(ctx, copyRoot, scratch, build, specs, options, env, cache) }
	if err := execute(); err != nil {
		return Plan{}, err
	}
	first, err := readRecipeInputs(copyRoot)
	if err != nil {
		return Plan{}, err
	}
	firstModes, err := recipeModes(copyRoot, first)
	if err != nil {
		return Plan{}, err
	}
	if err := execute(); err != nil {
		return Plan{}, err
	}
	second, err := readRecipeInputs(copyRoot)
	if err != nil {
		return Plan{}, err
	}
	if len(second) != len(first) {
		return Plan{}, fmt.Errorf("recipe changed file inventory on its convergence run")
	}
	for path, data := range first {
		if other, exists := second[path]; !exists || !bytes.Equal(other, data) {
			return Plan{}, fmt.Errorf("recipe did not converge: %s", path)
		}
	}
	secondModes, err := recipeModes(copyRoot, second)
	if err != nil {
		return Plan{}, err
	}
	for path, mode := range firstModes {
		if secondModes[path] != mode {
			return Plan{}, fmt.Errorf("recipe permissions did not converge: %s", path)
		}
		if _, existing := inputs[path]; existing {
			info, err := os.Stat(filepath.Join(root, path))
			if err != nil {
				return Plan{}, err
			}
			if info.Mode().Perm() != mode {
				return Plan{}, fmt.Errorf("recipe changed existing file permissions: %s", path)
			}
		}
	}
	basePolicy := policy
	basePolicy.Recipes = nil
	basePolicy.RecipeRun = nil
	native, err := CreatePlan(copyRoot, basePolicy)
	if err != nil {
		return Plan{}, err
	}
	final := map[string][]byte{}
	for path, data := range second {
		final[path] = data
	}
	for _, file := range native.Files {
		final[file.Path] = []byte(file.After)
	}
	nativeInputs, err := readBuildInputs(copyRoot, basePolicy, build)
	if err != nil {
		return Plan{}, err
	}
	if err := validateSourceVersionPins(inputs, final, policy, build); err != nil {
		return Plan{}, err
	}
	run := &RecipeRun{Build: build, BuildFiles: sortedContentPaths(nativeInputs), PolicySHA256: recipePolicyHash(policy), Inputs: map[string]string{}, Files: []File{}, Artifacts: artifacts}
	for path, before := range inputs {
		run.Inputs[path] = hash(before)
	}
	run.Files = diffFiles(inputs, final)
	directories := map[string]bool{}
	for i := range run.Files {
		file := &run.Files[i]
		if !sourceRecipePath(file.Path) && !editableBuildPath(file.Path, build, policy) && !recipeBuildPath(file.Path) {
			return Plan{}, fmt.Errorf("recipe changed unsupported path %s", file.Path)
		}
		if file.Action == "delete" {
			info, err := os.Stat(filepath.Join(root, file.Path))
			if err != nil {
				return Plan{}, err
			}
			file.BeforeMode = uint32(info.Mode().Perm())
		}
		if file.Action == "create" {
			info, err := os.Stat(filepath.Join(copyRoot, file.Path))
			if err != nil {
				return Plan{}, err
			}
			file.AfterMode = uint32(info.Mode().Perm())
			for parent := filepath.Dir(file.Path); parent != "."; parent = filepath.Dir(parent) {
				if _, err := os.Stat(filepath.Join(root, parent)); os.IsNotExist(err) {
					directories[parent] = true
				} else if err != nil {
					return Plan{}, err
				}
			}
		}
	}
	for directory := range directories {
		run.CreatedDirs = append(run.CreatedDirs, directory)
	}
	sort.Strings(run.CreatedDirs)
	run.CacheInputs, err = cacheEvidence(cache)
	if err != nil {
		return Plan{}, err
	}
	run.SHA256 = recipeRunHash(*run)
	policy.RecipeRun = run
	plan, err := CreatePlan(root, policy)
	if err != nil {
		return Plan{}, err
	}
	// Copy-generated native blockers, including unsupported framework behavior,
	// are carried forward rather than hidden by frozen results.
	for _, finding := range native.Findings {
		if finding.Severity == "blocker" {
			plan.Findings = append(plan.Findings, finding)
		}
	}
	return plan, nil
}

func recipeModes(root string, inputs map[string][]byte) (map[string]os.FileMode, error) {
	modes := map[string]os.FileMode{}
	for path := range inputs {
		info, err := os.Stat(filepath.Join(root, path))
		if err != nil {
			return nil, err
		}
		modes[path] = info.Mode().Perm()
	}
	return modes, nil
}

func executeRecipes(ctx context.Context, root, scratch, build string, specs []RecipeSpec, options VerifyOptions, env []string, cache string) error {
	active := []string{}
	artifacts := []string{}
	seen := map[string]bool{}
	for _, spec := range specs {
		active = append(active, spec.ActiveRecipe)
		if !seen[spec.Artifact] {
			artifacts = append(artifacts, spec.Artifact)
			seen[spec.Artifact] = true
		}
	}
	executable := "mvn"
	var args []string
	if build == "maven" {
		if options.MavenSettings != "" {
			settings, err := filepath.Abs(options.MavenSettings)
			if err != nil {
				return err
			}
			args = append(args, "--settings", settings)
		}
		if info, err := os.Lstat(filepath.Join(root, "mvnw")); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0111 == 0 {
				return fmt.Errorf("source worker mvnw must be a regular executable")
			}
			executable = filepath.Join(root, "mvnw")
		}
		args = append(args, "-B", "-ntp", rewriteMavenPlugin+":runNoFork", "-Dmaven.repo.local="+cache, "-Drewrite.activeRecipes="+strings.Join(active, ","), "-Drewrite.recipeArtifactCoordinates="+strings.Join(artifacts, ","), "-Drewrite.failOnInvalidActiveRecipes=true", "-Drewrite.pomCacheEnabled=false", "-Drewrite.exportDatatables=false", "-Drewrite.skip=false", "-DskipMavenParsing=false")
		if options.Offline {
			args = append(args, "-o")
		}
		if options.Profiles != "" {
			args = append(args, "-P"+options.Profiles)
		}
	} else {
		executable = options.GradleCommand
		if executable == "" {
			executable = "gradle"
			if _, err := os.Stat(filepath.Join(root, "gradlew")); err == nil {
				executable = filepath.Join(root, "gradlew")
			}
		}
		var script strings.Builder
		script.WriteString("initscript { repositories { maven { url = uri('")
		script.WriteString(strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\r", `\r`, "\n", `\n`).Replace(filepath.ToSlash(cache)))
		script.WriteString("') }; mavenCentral() }; dependencies { classpath '")
		script.WriteString(rewriteGradlePlugin)
		script.WriteString("' } }\nallprojects { plugins.apply(org.openrewrite.gradle.RewritePlugin)\n")
		for _, artifact := range artifacts {
			script.WriteString("dependencies { add('rewrite', '")
			script.WriteString(artifact)
			script.WriteString("') }\n")
		}
		script.WriteString("rewrite { activeRecipe(")
		for i, name := range active {
			if i > 0 {
				script.WriteString(",")
			}
			script.WriteString("'")
			script.WriteString(name)
			script.WriteString("'")
		}
		script.WriteString(") }\n}\n")
		init := filepath.Join(scratch, "rewrite.init.gradle")
		if err := os.WriteFile(init, []byte(script.String()), 0600); err != nil {
			return err
		}
		args = []string{"--no-daemon", "--console", "plain", "--init-script", init, "rewriteRun"}
		if options.Offline {
			args = append(args, "--offline")
		}
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir, command.Env = root, env
	var output bytes.Buffer
	sink := io.Writer(&output)
	if options.Stderr != nil {
		sink = io.MultiWriter(&output, options.Stderr)
	}
	command.Stdout, command.Stderr = sink, sink
	if err := command.Run(); err != nil {
		log := output.String()
		if len(log) > 20000 {
			log = log[len(log)-20000:]
		}
		return fmt.Errorf("isolated OpenRewrite worker failed: %w\n%s", err, log)
	}
	log := strings.ToLower(output.String())
	for _, failure := range []string{"failed to parse", "error while parsing", "error while visiting", "parseexception"} {
		if strings.Contains(log, failure) {
			return fmt.Errorf("isolated OpenRewrite worker reported %s; no migration plan produced", failure)
		}
	}
	return nil
}
