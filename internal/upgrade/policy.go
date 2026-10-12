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
	Java         int               `json:"java"`
	Plugins      map[string]string `json:"plugins"`
	Dependencies map[string]string `json:"dependencies"`
	Parents      map[string]string `json:"parents"`
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

func (p Policy) Validate() error {
	if p.Java != 21 {
		return fmt.Errorf("this version supports target Java 21 only")
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
			if !exactVersion.MatchString(version) || strings.Contains(strings.ToUpper(version), "SNAPSHOT") {
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
