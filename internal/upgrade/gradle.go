package upgrade

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const gradleWrapperProperties = "gradle/wrapper/gradle-wrapper.properties"

func detectBuild(root string) (string, error) {
	has := func(name string) bool { _, err := os.Lstat(filepath.Join(root, name)); return err == nil }
	maven := has("pom.xml")
	gradle := has("build.gradle") || has("build.gradle.kts") || has("settings.gradle") || has("settings.gradle.kts")
	if maven && gradle {
		return "", fmt.Errorf("mixed Maven/Gradle root; select one build root")
	}
	if maven {
		return "maven", nil
	}
	if gradle {
		return "gradle", nil
	}
	return "", fmt.Errorf("no supported build at %s: expected pom.xml or Gradle build/settings", root)
}

func gradleTarget(java int) string {
	if java == 25 {
		return "9.1.0"
	}
	return "8.14.3"
}
func gradleFloor(java int) string { return map[int]string{17: "7.3", 21: "8.5", 25: "9.1.0"}[java] }

func optionalBuildFile(root, path string, contents map[string][]byte) error {
	_, err := os.Lstat(filepath.Join(root, path))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	full, err := safePath(root, path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return err
	}
	contents[path] = data
	return nil
}

func readGradleInputs(root string) (map[string][]byte, error) {
	contents := map[string][]byte{}
	for _, name := range []string{"settings.gradle", "settings.gradle.kts", "build.gradle", "build.gradle.kts", "gradle.properties", "gradlew", "gradlew.bat", gradleWrapperProperties, "gradle/wrapper/gradle-wrapper.jar", "gradle/libs.versions.toml"} {
		if err := optionalBuildFile(root, name, contents); err != nil {
			return nil, err
		}
	}
	for _, kind := range []string{"settings", "build"} {
		if _, a := contents[kind+".gradle"]; a {
			if _, b := contents[kind+".gradle.kts"]; b {
				return nil, fmt.Errorf("ambiguous Gradle %s Groovy/Kotlin files", kind)
			}
		}
	}
	var modules []string
	for _, name := range []string{"settings.gradle", "settings.gradle.kts"} {
		data, ok := contents[name]
		if !ok {
			continue
		}
		tokens, err := lexBuild(data, false)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		pairs, err := matchingTokens(tokens)
		if err != nil {
			return nil, err
		}
		for i, t := range tokens {
			if t.kind != "identifier" {
				continue
			}
			if t.value == "includeBuild" || t.value == "projectDir" {
				return nil, fmt.Errorf("%s: included builds and custom projectDir require a Gradle model worker", name)
			}
			if t.value != "include" {
				continue
			}
			j := i + 1
			end := len(tokens)
			if j < len(tokens) && tokens[j].value == "(" {
				end = pairs[j]
				j++
			}
			found := false
			for j < end {
				v := tokens[j]
				if v.value == "," {
					j++
					continue
				}
				if v.kind != "string" {
					if found && end == len(tokens) {
						break
					}
					return nil, fmt.Errorf("%s: project includes must be literal strings", name)
				}
				path := strings.TrimPrefix(v.value, ":")
				if path == "" || strings.ContainsAny(path, "/\\${}") || strings.Contains(path, "..") {
					return nil, fmt.Errorf("unsafe Gradle include %q", v.value)
				}
				path = strings.ReplaceAll(path, ":", string(filepath.Separator))
				modules = append(modules, path)
				found = true
				j++
				if j < len(tokens) && tokens[j].value != "," && end == len(tokens) {
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("%s: empty/dynamic project include", name)
			}
		}
	}
	sort.Strings(modules)
	for _, module := range modules {
		found := false
		for _, name := range []string{"build.gradle", "build.gradle.kts"} {
			path := filepath.Join(module, name)
			if err := optionalBuildFile(root, path, contents); err != nil {
				return nil, err
			}
			if _, ok := contents[path]; ok {
				if found {
					return nil, fmt.Errorf("ambiguous Gradle module %s", module)
				}
				found = true
			}
		}
		// Empty grouping projects are legal; nested included modules carry the build.
		if !found {
			full, err := safePath(root, module)
			if err != nil {
				return nil, err
			}
			info, err := os.Stat(full)
			if err != nil || !info.IsDir() {
				return nil, fmt.Errorf("Gradle module %s is not a directory", module)
			}
		}
	}
	return contents, nil
}

func applyBuildEdits(data []byte, path string, edits []buildEdit) ([]byte, []Change, error) {
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	limit := len(data)
	var changes []Change
	for _, e := range edits {
		if e.start < 0 || e.end > limit || e.start > e.end {
			return nil, nil, fmt.Errorf("overlapping build edits in %s", path)
		}
		limit = e.start
		if string(data[e.start:e.end]) == e.value {
			continue
		}
		data = replace(data, e.start, e.end, e.value)
		changes = append(changes, Change{path, e.reason})
	}
	return data, changes, nil
}

func buildBlocks(tokens []buildToken, pairs map[int]int, name string) [][2]int {
	var blocks [][2]int
	for i, t := range tokens {
		if t.kind == "identifier" && t.value == name && i+1 < len(tokens) && tokens[i+1].value == "{" {
			blocks = append(blocks, [2]int{i + 1, pairs[i+1]})
		}
	}
	return blocks
}
func insideBlocks(i int, blocks [][2]int) bool {
	for _, b := range blocks {
		if i > b[0] && i < b[1] {
			return true
		}
	}
	return false
}

func gradleVersion(value string, properties map[string]string) (string, bool) {
	if strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}") {
		v, ok := properties[value[2:len(value)-1]]
		return v, ok
	}
	if strings.HasPrefix(value, "$") {
		v, ok := properties[value[1:]]
		return v, ok
	}
	return value, !strings.Contains(value, "$")
}

func migrateGradleScript(data []byte, path string, policy Policy, properties map[string]string) ([]byte, []Change, []Finding, error) {
	tokens, err := lexBuild(data, false)
	if err != nil {
		return data, nil, []Finding{{"blocker", path, err.Error()}}, nil
	}
	pairs, err := matchingTokens(tokens)
	if err != nil {
		return data, nil, []Finding{{"blocker", path, err.Error()}}, nil
	}
	plugins := buildBlocks(tokens, pairs, "plugins")
	dependencies := buildBlocks(tokens, pairs, "dependencies")
	var edits []buildEdit
	var findings []Finding
	block := func(message string) { findings = append(findings, Finding{"blocker", path, message}) }
	requested := func(coord, current, target string, t buildToken) {
		if target == "" {
			return
		}
		v, ok := gradleVersion(current, properties)
		if !ok {
			block("cannot resolve version for " + coord + ": " + current)
			return
		}
		order, comparable := compareArtifactVersions(coord, v, target)
		if v == target || comparable && order >= 0 {
			return
		}
		if !comparable {
			block("cannot order Gradle versions for " + coord + ": " + v + " -> " + target)
			return
		}
		value := t.value
		if strings.Contains(value, ":") {
			parts := strings.Split(value, ":")
			parts[2] = target
			value = strings.Join(parts, ":")
		} else {
			value = target
		}
		edits = append(edits, tokenEdit(t, value, "pin "+coord+" to "+target))
	}
	javaPlugin := false
	javaPins := 0
	floors := compatibilityFloors(policy.Java)
	jacoco := buildBlocks(tokens, pairs, "jacoco")
	jacocoPlugin := false
	jacocoPins := 0
	braceDepth := 0
	configurations := map[string]bool{"api": true, "implementation": true, "compileOnly": true, "runtimeOnly": true, "testImplementation": true, "testCompileOnly": true, "testRuntimeOnly": true, "annotationProcessor": true, "testAnnotationProcessor": true, "classpath": true, "platform": true, "enforcedPlatform": true, "compile": true, "testCompile": true}
	for i, t := range tokens {
		if t.kind == "symbol" {
			if t.value == "{" {
				braceDepth++
			} else if t.value == "}" {
				braceDepth--
			}
		}
		if t.kind == "identifier" && (t.value == "apply" || t.value == "applyFrom") && !(insideBlocks(i, plugins) && i+1 < len(tokens) && tokens[i+1].value == "false") {
			block("applied build scripts/plugins require Gradle model discovery")
		}
		if t.kind == "identifier" && t.value == "kotlin" && i+1 < len(tokens) && tokens[i+1].value == "(" {
			block("Kotlin language/compiler target migration requires a Kotlin recipe; Kotlin DSL Java builds are supported")
		}
		if insideBlocks(i, plugins) {
			if t.kind == "identifier" && (t.value == "java" || t.value == "java-library") {
				javaPlugin = true
			}
			if t.kind == "string" && t.value == "jacoco" {
				jacocoPlugin = true
			}
			if t.kind == "identifier" && t.value == "jacoco" {
				jacocoPlugin = true
			}
			if t.kind == "string" && (t.value == "java" || t.value == "java-library") {
				javaPlugin = true
			}
			if t.kind == "string" && i > 0 && (tokens[i-1].value == "id" || tokens[i-1].value == "(" && i > 1 && tokens[i-2].value == "id") {
				j := i + 1
				if j < len(tokens) && tokens[j].value == ")" {
					j++
				}
				if j < len(tokens) && tokens[j].value == "version" {
					j++
					if j >= len(tokens) || tokens[j].kind != "string" {
						block("plugin " + t.value + " has a nonliteral version; use a version catalog")
						continue
					}
					target := policy.GradlePlugins[t.value]
					if t.value == "org.springframework.boot" {
						planned := tokens[j].value
						if target != "" && numericVersion(planned) && numericVersion(target) && compareVersions(planned, target) < 0 {
							planned = target
						}
						if message := bootGradleCompatibility(planned, policy.Gradle); message != "" {
							block(message)
						}
					}
					if t.value == "org.springframework.boot" && target != "" {
						current := tokens[j].value
						if numericVersion(current) && numericVersion(target) && compareVersions(current, target) < 0 && strings.Split(current, ".")[0] != strings.Split(target, ".")[0] {
							block("Spring Boot major upgrade requires a source recipe")
							continue
						}
					}
					requested("gradle-plugin:"+t.value, tokens[j].value, target, tokens[j])
				}
			}
		}
		if t.kind == "string" && insideBlocks(i, dependencies) {
			caller := ""
			if i > 0 {
				caller = tokens[i-1].value
			}
			if caller == "(" && i > 1 {
				caller = tokens[i-2].value
			}
			if !configurations[caller] {
				continue
			}
			parts := strings.Split(t.value, ":")
			if len(parts) < 2 || len(parts) > 4 {
				continue
			}
			coord := parts[0] + ":" + parts[1]
			target := policy.Dependencies[coord]
			if len(parts) > 2 {
				if message := gradleLibraryCompatibility(coord, parts[2], target, floors, properties); message != "" {
					block(message)
				}
			}
			if target == "" {
				continue
			}
			if len(parts) == 2 {
				block(coord + " is versionless/managed; update its platform/BOM or use model-aware discovery")
				continue
			}
			requested(coord, parts[2], target, t)
		}
		if t.kind == "identifier" && t.value == "group" && insideBlocks(i, dependencies) && i+2 < len(tokens) && (tokens[i+1].value == ":" || tokens[i+1].value == "=") && tokens[i+2].kind == "string" {
			group := tokens[i+2].value
			artifact := ""
			versionIndex := -1
			for j := i + 3; j+2 < len(tokens); j++ {
				if tokens[j].value == "}" || tokens[j].value == ";" || configurations[tokens[j].value] {
					break
				}
				if (tokens[j+1].value == ":" || tokens[j+1].value == "=") && tokens[j].value == "name" && tokens[j+2].kind == "string" {
					artifact = tokens[j+2].value
				}
				if (tokens[j+1].value == ":" || tokens[j+1].value == "=") && tokens[j].value == "version" {
					versionIndex = j + 2
				}
			}
			coord := group + ":" + artifact
			target := policy.Dependencies[coord]
			if target != "" || floors[coord] != "" {
				if versionIndex < 0 {
					if target != "" {
						block(coord + " uses versionless map notation; update its owning platform")
					}
				} else {
					v := tokens[versionIndex]
					current := v.value
					if v.kind == "identifier" {
						if value, ok := properties[current]; ok {
							current = value
						} else {
							block("unresolved map dependency version for " + coord)
							continue
						}
					}
					if message := gradleLibraryCompatibility(coord, current, target, floors, properties); message != "" {
						block(message)
					}
					if v.kind == "string" {
						requested(coord, current, target, v)
					} else if target != "" {
						order, known := compareArtifactVersions(coord, current, target)
						if !known {
							block("cannot order map dependency version for " + coord)
						} else if order < 0 {
							edits = append(edits, buildEdit{v.start, v.end, "\"" + target + "\"", "pin " + coord + " to " + target})
						}
					}
				}
			}
		}
		if t.kind == "identifier" && t.value == "toolVersion" && insideBlocks(i, jacoco) {
			jacocoPins++
			j := i + 1
			if j < len(tokens) && tokens[j].value == "=" {
				j++
			}
			if j >= len(tokens) || tokens[j].kind != "string" {
				block("JaCoCo toolVersion must be a literal version")
			} else {
				if message := gradleLibraryCompatibility("org.jacoco:jacoco-maven-plugin", tokens[j].value, policy.Plugins["org.jacoco:jacoco-maven-plugin"], floors, properties); message != "" {
					block(message)
				}
				requested("org.jacoco:jacoco-maven-plugin", tokens[j].value, policy.Plugins["org.jacoco:jacoco-maven-plugin"], tokens[j])
			}
		}
		// java.toolchain and legacy source/target/release declarations.
		if t.kind != "identifier" {
			continue
		}
		scalar := -1
		legacy := false
		if t.value == "JavaLanguageVersion" && i+4 < len(tokens) && tokens[i+1].value == "." && tokens[i+2].value == "of" && tokens[i+3].value == "(" {
			scalar = i + 4
		}
		if t.value == "sourceCompatibility" || t.value == "targetCompatibility" {
			if i > 0 && (tokens[i-1].value == "def" || tokens[i-1].value == "val" || tokens[i-1].value == "var") {
				block("local Java compatibility variables require model-aware discovery")
				continue
			}
			// Gradle 9 removed the Java convention properties exposed on
			// Project. Java extension and JavaCompile task properties remain.
			if compareVersions(policy.Gradle, "9.0") >= 0 && braceDepth == 0 && (i == 0 || tokens[i-1].value != ".") {
				property := "java." + t.value
				if i+1 < len(tokens) && tokens[i+1].value != "=" {
					property += " ="
				}
				edits = append(edits, tokenEdit(t, property, "move removed Gradle project property "+t.value+" to the Java extension"))
			}
			j := i + 1
			if j < len(tokens) && tokens[j].value == "=" {
				j++
			}
			if j < len(tokens) && tokens[j].value == "JavaVersion" && j+2 < len(tokens) && tokens[j+1].value == "." {
				scalar = j + 2
				legacy = true
			} else {
				scalar = j
			}
		}
		if t.value == "release" && i > 1 && tokens[i-1].value == "." && tokens[i-2].value == "options" {
			j := i + 1
			if j < len(tokens) && tokens[j].value == "=" {
				scalar = j + 1
			} else if j+2 < len(tokens) && tokens[j].value == "." && tokens[j+1].value == "set" && tokens[j+2].value == "(" {
				scalar = j + 3
			} else {
				block("unsupported Gradle compiler release expression")
			}
		}
		if scalar < 0 {
			continue
		}
		javaPins++
		if scalar >= len(tokens) {
			block("missing Gradle Java target")
			continue
		}
		v := tokens[scalar]
		raw := v.value
		if legacy {
			raw = strings.TrimPrefix(raw, "VERSION_")
			raw = strings.ReplaceAll(raw, "_", ".")
		}
		raw, ok := gradleVersion(raw, properties)
		if !ok {
			block("unresolved Gradle Java target " + v.value)
			continue
		}
		if strings.HasPrefix(raw, "1.") {
			raw = strings.TrimPrefix(raw, "1.")
		}
		major, err := strconv.Atoi(raw)
		if err != nil || major < 1 {
			block("unsupported Gradle Java target " + v.value)
			continue
		}
		if major > policy.Java {
			block(fmt.Sprintf("Gradle Java target %d exceeds requested Java %d; refusing downgrade", major, policy.Java))
			continue
		}
		target := strconv.Itoa(policy.Java)
		if legacy {
			target = "VERSION_" + target
		}
		if v.value != target {
			edits = append(edits, tokenEdit(v, target, fmt.Sprintf("set Gradle Java target to %d", policy.Java)))
		}
	}
	after, changes, err := applyBuildEdits(data, path, edits)
	if err != nil {
		return nil, nil, nil, err
	}
	if javaPins == 0 && javaPlugin {
		newline := "\n"
		if bytes.Contains(after, []byte("\r\n")) {
			newline = "\r\n"
		}
		if len(after) > 0 && after[len(after)-1] != '\n' {
			after = append(after, newline...)
		}
		stanza := fmt.Sprintf("%sjava {%s    toolchain {%s        languageVersion = JavaLanguageVersion.of(%d)%s    }%s}%s", newline, newline, newline, policy.Java, newline, newline, newline)
		after = append(after, stanza...)
		changes = append(changes, Change{path, fmt.Sprintf("configure Gradle Java %d toolchain", policy.Java)})
	}
	if jacocoPlugin && jacocoPins == 0 {
		if target := policy.Plugins["org.jacoco:jacoco-maven-plugin"]; target != "" {
			if message := gradleLibraryCompatibility("org.jacoco:jacoco-maven-plugin", target, target, floors, properties); message != "" {
				block(message)
			}
			newline := "\n"
			if bytes.Contains(after, []byte("\r\n")) {
				newline = "\r\n"
			}
			if len(after) > 0 && after[len(after)-1] != '\n' {
				after = append(after, newline...)
			}
			after = append(after, []byte(newline+"jacoco { toolVersion = \""+target+"\" }"+newline)...)
			changes = append(changes, Change{path, "pin Gradle JaCoCo to " + target})
		} else {
			block("JaCoCo default version is unresolved; supply an explicit compatible toolVersion or policy pin")
		}
	}
	return after, changes, findings, nil
}

func transformGradle(originals map[string][]byte, policy Policy) (map[string][]byte, []Change, []Finding, error) {
	contents := map[string][]byte{}
	paths := make([]string, 0, len(originals))
	for path, data := range originals {
		contents[path] = data
		paths = append(paths, path)
	}
	sort.Strings(paths)
	props := map[string]string{}
	for _, line := range strings.Split(string(originals["gradle.properties"]), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && !strings.HasPrefix(strings.TrimSpace(key), "#") {
			props[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	var changes []Change
	var findings []Finding
	compatibilityPolicy := policy
	if current := gradleWrapperVersion(originals[gradleWrapperProperties]); current != "" && compareVersions(current, policy.Gradle) > 0 {
		compatibilityPolicy.Gradle = current
	}
	if compareVersions(policy.Gradle, gradleFloor(policy.Java)) < 0 {
		findings = append(findings, Finding{"blocker", gradleWrapperProperties, fmt.Sprintf("Gradle %s cannot run on Java %d; requires >= %s", policy.Gradle, policy.Java, gradleFloor(policy.Java))})
	}
	for _, path := range paths {
		var after []byte
		var actions []Change
		var issues []Finding
		var err error
		switch {
		case strings.HasSuffix(path, "build.gradle") || strings.HasSuffix(path, "build.gradle.kts"):
			after, actions, issues, err = migrateGradleScript(originals[path], path, compatibilityPolicy, props)
		case path == "gradle/libs.versions.toml":
			after, actions, issues, err = migrateVersionCatalog(originals[path], path, compatibilityPolicy)
		case path == gradleWrapperProperties:
			after, actions, issues = migrateGradleWrapper(originals[path], policy)
		default:
			continue
		}
		if err != nil {
			return nil, nil, nil, err
		}
		contents[path] = after
		changes = append(changes, actions...)
		findings = append(findings, issues...)
	}
	if _, ok := originals[gradleWrapperProperties]; !ok {
		findings = append(findings, Finding{"warning", gradleWrapperProperties, "no Gradle wrapper; verification will require an installed Gradle meeting the saved target " + policy.Gradle})
	}
	if policy.Maven != "" {
		findings = append(findings, Finding{"blocker", "build.gradle", "Maven wrapper target does not apply to a Gradle repository"})
	}
	return contents, changes, findings, nil
}

func bootGradleCompatibility(version, gradle string) string {
	if !numericVersion(version) {
		return ""
	}
	major := strings.Split(version, ".")[0]
	if (major == "1" || major == "2") && compareVersions(gradle, "8.0") >= 0 {
		return "Spring Boot " + version + " does not declare support for Gradle " + gradle + "; choose a supported framework/tool pair or a Boot source recipe"
	}
	if major == "3" && compareVersions(gradle, "9.0") >= 0 {
		return "Spring Boot " + version + " does not declare Gradle 9 support; select Java 21/Gradle 8 or a Boot 4 source recipe"
	}
	return ""
}
func gradleLibraryCompatibility(coord, current, target string, floors, properties map[string]string) string {
	floor := floors[coord]
	if floor == "" {
		return ""
	}
	current, ok := gradleVersion(current, properties)
	if !ok {
		return ""
	}
	if numericVersion(current) && numericVersion(target) && compareVersions(current, target) < 0 {
		current = target
	}
	if numericVersion(current) && compareVersions(current, floor) < 0 {
		return coord + " " + current + " is below the target Java compatibility floor " + floor + "; use --preset common or an explicit dependency rule"
	}
	return ""
}
