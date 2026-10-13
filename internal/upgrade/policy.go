package upgrade

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// Policy is a reviewed set of target versions, not a request for "latest".
type Policy struct {
	Recipes       []string              `json:"recipes,omitempty"`
	RecipeRun     *RecipeRun            `json:"recipe_run,omitempty"`
	Gradle        string                `json:"gradle,omitempty"`
	GradleSHA256  string                `json:"gradle_sha256,omitempty"`
	GradlePlugins map[string]string     `json:"gradle_plugins,omitempty"`
	MavenModels   map[string]MavenModel `json:"maven_models,omitempty"`
	Java          int                   `json:"java"`
	Maven         string                `json:"maven,omitempty"`
	MavenSHA256   string                `json:"maven_sha256,omitempty"`
	Plugins       map[string]string     `json:"plugins"`
	Dependencies  map[string]string     `json:"dependencies"`
	Parents       map[string]string     `json:"parents"`
}

func DefaultPolicy() Policy {
	return Policy{
		Java: 21,
		Plugins: map[string]string{
			"org.eclipse.jetty:jetty-maven-plugin":            "9.4.58.v20250814",
			"org.apache.maven.plugins:maven-clean-plugin":     "3.4.1",
			"org.apache.maven.plugins:maven-resources-plugin": "3.3.1",
			"org.apache.maven.plugins:maven-compiler-plugin":  "3.13.0",
			"org.apache.maven.plugins:maven-surefire-plugin":  "3.5.6",
			"org.apache.maven.plugins:maven-failsafe-plugin":  "3.5.6",
			"org.apache.maven.plugins:maven-war-plugin":       "3.4.0",
			"org.apache.maven.plugins:maven-jar-plugin":       "3.4.2",
			"org.jacoco:jacoco-maven-plugin":                  "0.8.12",
		},
		Dependencies: map[string]string{}, Parents: map[string]string{},
	}
}

// PolicyForJava returns pinned build defaults. It never resolves "latest".
func PolicyForJava(java int) (Policy, error) {
	p := DefaultPolicy()
	p.Java = java
	if java == 25 {
		p.Plugins["org.jacoco:jacoco-maven-plugin"] = "0.8.14"
	}
	return p, p.Validate()
}

// AddPreset selects existing libraries only; Maven management/ownership rules
// still apply. Framework families are deliberately excluded from this preset.
func (p *Policy) AddPreset(name string) error {
	switch name {
	case "", "build":
		return nil
	case "common":
		if p.Dependencies == nil {
			p.Dependencies = map[string]string{}
		}
		rules := map[string]string{
			"junit:junit":                      "4.13.2",
			"org.projectlombok:lombok":         "1.18.46",
			"org.apache.commons:commons-lang3": "3.20.0",
			"commons-io:commons-io":            "2.20.0",
			"org.junit:junit-bom":              "5.12.2",
		}
		for _, artifact := range []string{"junit-jupiter", "junit-jupiter-api", "junit-jupiter-engine", "junit-jupiter-params"} {
			rules["org.junit.jupiter:"+artifact] = "5.12.2"
		}
		for _, artifact := range []string{"junit-platform-commons", "junit-platform-engine", "junit-platform-launcher"} {
			rules["org.junit.platform:"+artifact] = "1.12.2"
		}
		asm := "9.7.1"
		if p.Java == 25 {
			asm = "9.9.1"
		}
		for _, artifact := range []string{"asm", "asm-commons", "asm-tree", "asm-analysis", "asm-util"} {
			rules["org.ow2.asm:"+artifact] = asm
		}
		for coordinate, version := range rules {
			p.Dependencies[coordinate] = version
		}
		return nil
	default:
		return fmt.Errorf("unknown preset %q; choose build or common", name)
	}
}

var jettyRelease = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)*)(?:\.v([0-9]{8}))?$`)

// Order only formats whose semantics we know. Jetty 9 releases append a build
// date to their numeric release; Maven's arbitrary qualifier ordering remains
// outside this comparator.
func compareArtifactVersions(coordinate, a, b string) (int, bool) {
	if numericVersion(a) && numericVersion(b) {
		return compareVersions(a, b), true
	}
	if !strings.HasPrefix(coordinate, "org.eclipse.jetty:") && !strings.HasPrefix(coordinate, "org.eclipse.jetty.websocket:") {
		return 0, false
	}
	aa, bb := jettyRelease.FindStringSubmatch(a), jettyRelease.FindStringSubmatch(b)
	if len(aa) == 0 || len(bb) == 0 {
		return 0, false
	}
	if order := compareVersions(aa[1], bb[1]); order != 0 {
		return order, true
	}
	if (aa[2] == "") != (bb[2] == "") {
		return 0, false
	}
	if aa[2] < bb[2] {
		return -1, true
	}
	if aa[2] > bb[2] {
		return 1, true
	}
	return 0, true
}

func ReadPolicy(path string) (Policy, error) {
	if path == "" {
		return DefaultPolicy(), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return Policy{}, err
	}
	defer f.Close()
	var p Policy
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return p, fmt.Errorf("policy must contain exactly one JSON object")
	}
	return p, p.Validate()
}

var exactVersion = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*(?:[.-][A-Za-z0-9]+)*$`)
var wildcardVersion = regexp.MustCompile(`(?:^|[.-])[xX](?:$|[.-])`)

func (p Policy) Validate() error {
	if err := validateRecipePolicy(p); err != nil {
		return err
	}
	if err := validateMavenModels(p.MavenModels); err != nil {
		return err
	}
	if p.Gradle != "" && (!numericVersion(p.Gradle) || compareVersions(p.Gradle, "7.3") < 0) {
		return fmt.Errorf("Gradle target must be an exact version >= 7.3")
	}
	if p.GradleSHA256 != "" && (p.Gradle == "" || !regexp.MustCompile(`^[a-fA-F0-9]{64}$`).MatchString(p.GradleSHA256)) {
		return fmt.Errorf("gradle_sha256 requires a Gradle target and a 64-character digest")
	}
	for id, version := range p.GradlePlugins {
		if !mavenName.MatchString(id) || !exactVersion.MatchString(version) || wildcardVersion.MatchString(version) || strings.Contains(strings.ToUpper(version), "SNAPSHOT") {
			return fmt.Errorf("Gradle plugin %s must use an exact release version", id)
		}
	}
	if p.Java != 17 && p.Java != 21 && p.Java != 25 {
		return fmt.Errorf("supported Java targets are 17, 21, and 25; got %d", p.Java)
	}
	if p.Maven != "" && (!numericVersion(p.Maven) || compareVersions(p.Maven, "3.6.3") < 0 || compareVersions(p.Maven, "4.0.0") >= 0) {
		return fmt.Errorf("Maven target must be an exact Maven 3 version >= 3.6.3 and < 4.0.0")
	}
	if p.MavenSHA256 != "" && (p.Maven == "" || !regexp.MustCompile(`^[a-fA-F0-9]{64}$`).MatchString(p.MavenSHA256)) {
		return fmt.Errorf("maven_sha256 requires a Maven target and a 64-character SHA-256 digest")
	}
	if p.Plugins["org.apache.maven.plugins:maven-compiler-plugin"] == "" {
		return fmt.Errorf("policy must pin org.apache.maven.plugins:maven-compiler-plugin")
	}
	for _, rules := range []map[string]string{p.Plugins, p.Dependencies, p.Parents} {
		for coordinate, version := range rules {
			parts := strings.Split(coordinate, ":")
			if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
				return fmt.Errorf("invalid groupId:artifactId: %q", coordinate)
			}
			if !exactVersion.MatchString(version) || wildcardVersion.MatchString(version) || strings.Contains(strings.ToUpper(version), "SNAPSHOT") {
				return fmt.Errorf("%s must have an exact release version, got %q", coordinate, version)
			}
		}
	}
	compiler := p.Plugins["org.apache.maven.plugins:maven-compiler-plugin"]
	if !numericVersion(compiler) || compareVersions(compiler, "3.11.0") < 0 || compareVersions(compiler, "4.0.0") >= 0 {
		return fmt.Errorf("compiler plugin must be a numeric Maven 3 plugin version >= 3.11.0 and < 4.0.0")
	}
	return nil
}

func numericVersion(v string) bool {
	for _, part := range strings.Split(v, ".") {
		if part == "" {
			return false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return v != ""
}

// compareVersions is deliberately limited to numeric versions. Maven qualifier
// ordering is not approximated; callers report those versions for review.
func compareVersions(a, b string) int {
	aa, bb := strings.Split(a, "."), strings.Split(b, ".")
	n := len(aa)
	if len(bb) > n {
		n = len(bb)
	}
	for i := 0; i < n; i++ {
		x, y := "", ""
		if i < len(aa) {
			x = strings.TrimLeft(aa[i], "0")
		}
		if i < len(bb) {
			y = strings.TrimLeft(bb[i], "0")
		}
		if len(x) < len(y) {
			return -1
		}
		if len(x) > len(y) {
			return 1
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}

func WritePolicy(path string, p Policy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return writeNewFile(path, append(data, '\n'))
}
