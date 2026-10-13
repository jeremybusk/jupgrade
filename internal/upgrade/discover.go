package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const effectivePOMPlugin = "org.apache.maven.plugins:maven-help-plugin:3.5.1"

type BuildModule struct {
	Path       string     `json:"path"`
	BuildFile  string     `json:"build_file"`
	Coordinate string     `json:"coordinate"`
	Artifacts  []Artifact `json:"artifacts"`
}

// BuildModel is read-only evidence from an evaluated build. Effective versions
// do not imply a writable declaration owner in an arbitrary executable DSL.
type BuildModel struct {
	Schema      int               `json:"schema"`
	Root        string            `json:"root"`
	Build       string            `json:"build"`
	Engine      string            `json:"engine"`
	ToolVersion string            `json:"tool_version"`
	Java        int               `json:"java"`
	Profiles    string            `json:"profiles,omitempty"`
	Settings    map[string]string `json:"settings_sha256,omitempty"`
	Inputs      map[string]string `json:"inputs"`
	Modules     []BuildModule     `json:"modules"`
	SHA256      string            `json:"sha256"`
}

func modelHash(model BuildModel) string {
	model.SHA256 = ""
	data, _ := json.Marshal(model)
	return hash(data)
}

func (model BuildModel) Validate() error {
	if model.Schema != 1 || (model.Build != "maven" && model.Build != "gradle") || model.SHA256 != modelHash(model) || len(model.Inputs) == 0 || len(model.Modules) == 0 || model.Java < 1 || !numericVersion(model.ToolVersion) {
		return fmt.Errorf("invalid build-model snapshot")
	}
	if model.Build == "maven" && model.Engine != effectivePOMPlugin || model.Build == "gradle" && model.Engine != "gradle-resolution-result-v1" {
		return fmt.Errorf("unsupported model engine")
	}
	root, err := canonicalRoot(model.Root)
	if err != nil {
		return err
	}
	if root != model.Root {
		return fmt.Errorf("model root is not canonical")
	}
	for path, digest := range model.Inputs {
		if !validRelativePath(path) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(digest) {
			return fmt.Errorf("invalid model input %s", path)
		}
	}
	for path, digest := range model.Settings {
		if !filepath.IsAbs(path) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(digest) {
			return fmt.Errorf("invalid settings provenance %s", path)
		}
	}
	seen := map[string]bool{}
	for _, module := range model.Modules {
		if seen[module.Path] || (module.Path != "." && !validRelativePath(module.Path)) {
			return fmt.Errorf("invalid model module %s", module.Path)
		}
		seen[module.Path] = true
		if module.BuildFile != "" && model.Inputs[module.BuildFile] == "" {
			return fmt.Errorf("module build file was not captured: %s", module.BuildFile)
		}
		for _, artifact := range module.Artifacts {
			if artifact.Path != module.BuildFile || artifact.Coordinate == "" {
				return fmt.Errorf("invalid model artifact in %s", module.Path)
			}
		}
	}
	return nil
}

func WriteBuildModel(path string, model BuildModel) error {
	if err := model.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(model, "", "  ")
	if err != nil {
		return err
	}
	return writeNewFile(path, append(data, '\n'))
}

func ReadBuildModel(path string) (BuildModel, error) {
	var model BuildModel
	f, err := os.Open(path)
	if err != nil {
		return model, err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&model); err != nil {
		return model, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return model, fmt.Errorf("model must contain one JSON object")
	}
	return model, model.Validate()
}

func InspectBuildModel(root string, model BuildModel) (Inspection, error) {
	if err := model.Validate(); err != nil {
		return Inspection{}, err
	}
	root, err := canonicalRoot(root)
	if err != nil {
		return Inspection{}, err
	}
	if root != model.Root {
		return Inspection{}, fmt.Errorf("model belongs to a different repository")
	}
	inputs, err := readRecipeInputs(root)
	if err != nil {
		return Inspection{}, err
	}
	if len(inputs) != len(model.Inputs) {
		return Inspection{}, fmt.Errorf("model input inventory changed; run discover again")
	}
	for path, data := range inputs {
		if hash(data) != model.Inputs[path] {
			return Inspection{}, fmt.Errorf("stale model: %s changed; run discover again", path)
		}
	}
	for path, digest := range model.Settings {
		data, err := os.ReadFile(path)
		if err != nil {
			return Inspection{}, err
		}
		if hash(data) != digest {
			return Inspection{}, fmt.Errorf("model settings changed: %s", path)
		}
	}
	report := Inspection{Root: root, Build: model.Build, POMs: []string{}, Artifacts: []Artifact{}}
	for _, module := range model.Modules {
		if model.Build == "maven" {
			report.POMs = append(report.POMs, module.BuildFile)
		}
		report.Artifacts = append(report.Artifacts, module.Artifacts...)
	}
	return report, nil
}

func copyWorktree(root, destination string, inputs map[string][]byte) error {
	if err := os.MkdirAll(destination, 0755); err != nil {
		return err
	}
	for _, path := range sortedContentPaths(inputs) {
		info, err := os.Stat(filepath.Join(root, path))
		if err != nil {
			return err
		}
		output := filepath.Join(destination, path)
		if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(output, inputs[path], info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func DiscoverBuildModel(ctx context.Context, root string, options VerifyOptions) (BuildModel, error) {
	root, err := canonicalRoot(root)
	if err != nil {
		return BuildModel{}, err
	}
	build, err := detectBuild(root)
	if err != nil {
		return BuildModel{}, err
	}
	if build == "gradle" && (options.Profiles != "" || options.MavenSettings != "") {
		return BuildModel{}, fmt.Errorf("Maven profiles/settings do not apply to Gradle discovery")
	}
	inputs, err := readRecipeInputs(root)
	if err != nil {
		return BuildModel{}, err
	}
	scratch, err := os.MkdirTemp("", "jupgrade-model-")
	if err != nil {
		return BuildModel{}, err
	}
	defer os.RemoveAll(scratch)
	copyRoot := filepath.Join(scratch, "repo")
	if err := copyWorktree(root, copyRoot, inputs); err != nil {
		return BuildModel{}, err
	}
	env, err := workerEnvironment(options)
	if err != nil {
		return BuildModel{}, err
	}
	if options.Timeout <= 0 {
		options.Timeout = 20 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	executable := "mvn"
	wrapper := "mvnw"
	if build == "gradle" {
		executable = "gradle"
		wrapper = "gradlew"
		if options.GradleCommand != "" {
			executable = options.GradleCommand
			wrapper = ""
		}
	}
	if wrapper != "" {
		if info, err := os.Stat(filepath.Join(copyRoot, wrapper)); err == nil {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				return BuildModel{}, fmt.Errorf("%s must be a regular executable", wrapper)
			}
			executable = filepath.Join(copyRoot, wrapper)
		}
	}
	run := func(args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, executable, args...)
		command.Dir, command.Env = copyRoot, env
		var output bytes.Buffer
		command.Stdout, command.Stderr = &output, &output
		if err := command.Run(); err != nil {
			log := output.String()
			if len(log) > 20000 {
				log = log[len(log)-20000:]
			}
			return nil, fmt.Errorf("isolated model worker: %w\n%s", err, log)
		}
		if options.Stderr != nil {
			_, _ = options.Stderr.Write(output.Bytes())
		}
		return output.Bytes(), nil
	}
	version, err := run("--version")
	if err != nil {
		return BuildModel{}, err
	}
	version = regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAll(version, nil)
	toolRE, javaRE := regexp.MustCompile(`Apache Maven\s+([0-9]+(?:\.[0-9]+)+)`), regexp.MustCompile(`Java version:\s*(?:1\.)?([0-9]+)`)
	if build == "gradle" {
		toolRE = regexp.MustCompile(`(?m)^Gradle\s+([0-9]+(?:\.[0-9]+)+)`)
		javaRE = regexp.MustCompile(`(?:Launcher JVM|JVM):\s*(?:1\.)?([0-9]+)`)
	}
	tool, jdk := toolRE.FindSubmatch(version), javaRE.FindSubmatch(version)
	if len(tool) != 2 || len(jdk) != 2 {
		return BuildModel{}, fmt.Errorf("cannot establish model worker runtime:\n%s", version)
	}
	java, _ := strconv.Atoi(string(jdk[1]))
	model := BuildModel{Schema: 1, Root: root, Build: build, ToolVersion: string(tool[1]), Java: java, Profiles: options.Profiles, Inputs: map[string]string{}, Settings: map[string]string{}}
	for path, data := range inputs {
		model.Inputs[path] = hash(data)
	}
	output := filepath.Join(scratch, "model-output")
	var args []string
	if build == "maven" {
		model.Engine = effectivePOMPlugin
		args = []string{"-B", "-ntp", effectivePOMPlugin + ":effective-pom", "-Doutput=" + output, "-Dverbose=false"}
		if options.Profiles != "" {
			args = append(args, "-P"+options.Profiles)
		}
		if options.MavenRepo != "" {
			repo, err := filepath.Abs(options.MavenRepo)
			if err != nil {
				return BuildModel{}, err
			}
			args = append(args, "-Dmaven.repo.local="+repo)
		}
		settings := []string{}
		if home, err := os.UserHomeDir(); err == nil && options.MavenSettings == "" {
			settings = append(settings, filepath.Join(home, ".m2", "settings.xml"))
		}
		if home := regexp.MustCompile(`Maven home:\s*([^\r\n]+)`).FindSubmatch(version); len(home) == 2 {
			settings = append(settings, filepath.Join(strings.TrimSpace(string(home[1])), "conf", "settings.xml"))
		}
		if options.MavenSettings != "" {
			path, err := filepath.Abs(options.MavenSettings)
			if err != nil {
				return BuildModel{}, err
			}
			args = append(args, "--settings", path)
			settings = append(settings, path)
			options.MavenSettings = path
		}
		for _, path := range settings {
			data, err := os.ReadFile(path)
			if os.IsNotExist(err) && path != options.MavenSettings {
				continue
			}
			if err != nil {
				return BuildModel{}, err
			}
			model.Settings[path] = hash(data)
		}
		if options.Offline {
			args = append(args, "-o")
		}
	} else {
		model.Engine = "gradle-resolution-result-v1"
		init := filepath.Join(scratch, "discover.init.gradle")
		if err := os.WriteFile(init, []byte(gradleDiscoveryScript), 0600); err != nil {
			return BuildModel{}, err
		}
		args = []string{"--no-daemon", "--console", "plain", "--init-script", init, "-Djupgrade.model.output=" + output, "jupgradeDiscoverModel"}
		if options.Offline {
			args = append(args, "--offline")
		}
	}
	var normalized []byte
	for attempt := 0; attempt < 2; attempt++ {
		if err := os.Remove(output); err != nil && !os.IsNotExist(err) {
			return BuildModel{}, err
		}
		if _, err := run(args...); err != nil {
			return BuildModel{}, err
		}
		data, err := os.ReadFile(output)
		if err != nil {
			return BuildModel{}, err
		}
		if build == "maven" {
			model.Modules, err = effectiveMavenModules(data, inputs)
		} else {
			err = json.Unmarshal(data, &model.Modules)
		}
		if err != nil {
			return BuildModel{}, err
		}
		sort.Slice(model.Modules, func(i, j int) bool { return model.Modules[i].Path < model.Modules[j].Path })
		for i := range model.Modules {
			sort.Slice(model.Modules[i].Artifacts, func(a, b int) bool {
				x, y := model.Modules[i].Artifacts[a], model.Modules[i].Artifacts[b]
				return x.Kind+":"+x.Coordinate+":"+x.Resolved < y.Kind+":"+y.Coordinate+":"+y.Resolved
			})
		}
		current, _ := json.Marshal(model.Modules)
		if attempt > 0 && !bytes.Equal(current, normalized) {
			return BuildModel{}, fmt.Errorf("effective build model did not converge")
		}
		normalized = current
		contextNow, err := readRecipeInputs(copyRoot)
		if err != nil {
			return BuildModel{}, err
		}
		if len(diffFiles(inputs, contextNow)) != 0 {
			return BuildModel{}, fmt.Errorf("model worker modified repository inputs")
		}
	}
	current, err := readRecipeInputs(root)
	if err != nil {
		return BuildModel{}, err
	}
	if len(diffFiles(inputs, current)) != 0 {
		return BuildModel{}, fmt.Errorf("repository changed during discovery")
	}
	model.SHA256 = modelHash(model)
	return model, model.Validate()
}

func effectiveMavenModules(data []byte, inputs map[string][]byte) ([]BuildModule, error) {
	root, err := parseXMLDocument(data)
	if err != nil {
		return nil, err
	}
	projects := []*element{}
	if root.Name == "project" {
		projects = append(projects, root)
	} else if root.Name == "projects" {
		for _, node := range root.Children {
			if node.Name == "project" {
				projects = append(projects, node)
			}
		}
	}
	modules := []BuildModule{}
	for _, project := range projects {
		matches := []string{}
		for path, pom := range inputs {
			if filepath.Base(path) != "pom.xml" {
				continue
			}
			n, err := parseXML(pom)
			if err != nil {
				continue
			}
			if textAt(pom, n, "artifactId") == textAt(data, project, "artifactId") {
				group := strings.Split(projectCoordinate(pom, n), ":")[0]
				if group == "" || strings.Contains(group, "${") || group == strings.Split(projectCoordinate(data, project), ":")[0] {
					matches = append(matches, path)
				}
			}
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("cannot uniquely map effective project %s to an inventoried POM", projectCoordinate(data, project))
		}
		module := BuildModule{Path: filepath.Dir(matches[0]), BuildFile: matches[0], Coordinate: projectCoordinate(data, project) + ":" + projectVersion(data, project), Artifacts: []Artifact{}}
		walk(project, func(n *element) {
			if profileOf(n) != nil {
				return
			}
			if kind := artifactKind(n); kind != "" {
				module.Artifacts = append(module.Artifacts, Artifact{Path: matches[0], Kind: kind, Coordinate: coordinate(data, n), Resolved: textAt(data, n, "version")})
			}
		})
		modules = append(modules, module)
	}
	if len(modules) == 0 {
		return nil, fmt.Errorf("effective POM contains no projects")
	}
	return modules, nil
}

const gradleDiscoveryScript = `import groovy.json.JsonOutput
import org.gradle.api.artifacts.result.UnresolvedDependencyResult
import org.gradle.api.artifacts.component.ProjectComponentIdentifier
gradle.projectsEvaluated {
    rootProject.tasks.register('jupgradeDiscoverModel') {
        doLast {
            if (!gradle.includedBuilds.empty) throw new GradleException('Composite builds require separate discovery roots')
            def root = rootProject.projectDir.canonicalFile.toPath()
            def modules = []
            rootProject.allprojects.sort { it.path }.each { p ->
                def directory = p.projectDir.canonicalFile.toPath()
                if (!directory.startsWith(root)) throw new GradleException('Project directory escapes repository: ' + p.path)
                def buildFile = p.buildFile.canonicalFile.toPath()
                if (!buildFile.startsWith(root)) throw new GradleException('Build file escapes repository: ' + p.path)
                def file = p.buildFile.exists() ? root.relativize(buildFile).toString() : ''
                def artifacts = []
                p.configurations.findAll { it.canBeResolved }.sort { it.name }.each { c ->
                    def result = c.incoming.resolutionResult
                    result.allDependencies.each { dependency ->
                        if (dependency instanceof UnresolvedDependencyResult) throw new GradleException('Unresolved dependency in ' + p.path + '/' + c.name, dependency.failure)
                    }
                    result.allComponents.each { component ->
                        def version = component.moduleVersion
                        if (version != null && !(component.id instanceof ProjectComponentIdentifier)) {
                            artifacts.add([path: file, kind: 'configuration:' + c.name, coordinate: version.group + ':' + version.name, resolved_version: version.version])
                        }
                    }
                }
                def relative = root.relativize(directory).toString()
                modules.add([path: relative ? relative : '.', build_file: file, coordinate: p.group.toString() + ':' + p.name + ':' + p.version.toString(), artifacts: artifacts])
            }
            new File(System.getProperty('jupgrade.model.output')).text = JsonOutput.toJson(modules)
        }
    }
}
`
