package upgrade

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type Finding struct {
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}

type Change struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

func coordinate(data []byte, n *element) string {
	g := textAt(data, n, "groupId")
	if g == "" && n.Name == "plugin" {
		g = "org.apache.maven.plugins"
	}
	return g + ":" + textAt(data, n, "artifactId")
}

func pluginNode(n *element) bool {
	return n.Name == "plugin" && n.Parent != nil && n.Parent.Name == "plugins" && n.Parent.Parent != nil && (n.Parent.Parent.Name == "build" || n.Parent.Parent.Name == "pluginManagement")
}

func dependencyNode(n *element) bool {
	return n.Name == "dependency" && n.Parent != nil && n.Parent.Name == "dependencies"
}

func migratePOM(original []byte, path string, policy Policy) ([]byte, []Change, []Finding, error) {
	return migratePOMWithModel(original, path, policy, nil)
}

func migratePOMWithModel(original []byte, path string, policy Policy, model *repositoryModel) ([]byte, []Change, []Finding, error) {
	data := append([]byte(nil), original...)
	java := strconv.Itoa(policy.Java)
	var changes []Change
	var findings []Finding
	record := func(before []byte, reason string) {
		if !bytes.Equal(before, data) {
			changes = append(changes, Change{path, reason})
		}
	}
	root, err := parseXML(data)
	if err != nil {
		return nil, nil, nil, err
	}
	javaScalar := func(n *element, key string) string {
		value := textAt(data, n, key)
		if model != nil && strings.Contains(value, "${") {
			resolved := model.resolve(model.poms[path], n, value)
			if resolved.err == nil {
				return resolved.value
			}
		}
		return value
	}
	// Duplicate declarations are ambiguous and commonly rejected by Maven.
	walk(root, func(n *element) {
		if n.Name == "properties" {
			for _, key := range []string{"maven.compiler.release", "maven.compiler.source", "maven.compiler.target", "maven.compiler.testRelease", "maven.compiler.testSource", "maven.compiler.testTarget", "java.version"} {
				v := javaScalar(n, key)
				if numericVersion(v) && compareVersions(v, java) > 0 {
					findings = append(findings, Finding{"blocker", path, key + " already exceeds Java " + java + "; this tool does not downgrade Java"})
				}
			}
		}
		if n.Name == "properties" {
			seen := map[string]bool{}
			for _, c := range n.Children {
				if seen[c.Name] {
					findings = append(findings, Finding{"blocker", path, "duplicate property: " + c.Name})
				}
				seen[c.Name] = true
			}
		}
		if pluginNode(n) && coordinate(data, n) == "org.apache.maven.plugins:maven-compiler-plugin" {
			walk(n, func(c *element) {
				if c.Name == "configuration" {
					for _, key := range []string{"release", "source", "target", "testRelease", "testSource", "testTarget"} {
						v := javaScalar(c, key)
						if numericVersion(v) && compareVersions(v, java) > 0 {
							findings = append(findings, Finding{"blocker", path, "compiler " + key + " already exceeds Java " + java})
						}
					}
				}
				if c.Name == "executable" || c.Name == "jdkToolchain" || c.Name == "compilerId" {
					findings = append(findings, Finding{"blocker", path, "compiler uses " + c.Name + "; configure a Java " + java + " compiler explicitly"})
				}
			})
		}
		if pluginNode(n) && coordinate(data, n) == "org.apache.maven.plugins:maven-toolchains-plugin" {
			findings = append(findings, Finding{"blocker", path, "Maven toolchain configuration requires review for Java " + java})
		}
	})
	// Target properties in every profile as well as the base project. Insert a
	// release property at the project root even if none was previously declared.
	if root.child("properties") == nil {
		before := data
		data = insert(data, root, tag(root, "properties", tag(root, "maven.compiler.release", java)))
		record(before, "set Java compiler release to "+java)
	}
	for {
		root, err = parseXML(data)
		if err != nil {
			return nil, nil, nil, err
		}
		var parent *element
		name := ""
		walk(root, func(n *element) {
			if parent != nil || n.Name != "properties" {
				return
			}
			for _, key := range []string{"maven.compiler.release", "maven.compiler.source", "maven.compiler.target", "maven.compiler.testRelease", "maven.compiler.testSource", "maven.compiler.testTarget", "java.version"} {
				if c := n.child(key); c != nil && scalar(data, c) != java {
					parent, name = n, key
					return
				}
			}
			if n.Parent == root && n.child("maven.compiler.release") == nil {
				parent, name = n, "maven.compiler.release"
			}
		})
		if parent == nil {
			break
		}
		before := data
		data, err = setScalar(data, parent, name, java)
		if err != nil {
			return nil, nil, nil, err
		}
		record(before, "set "+name+" to "+java)
	}
	// Set explicit compiler configuration too; it can override the properties.
	for {
		root, err = parseXML(data)
		if err != nil {
			return nil, nil, nil, err
		}
		var parent *element
		name := ""
		walk(root, func(n *element) {
			if parent != nil || !pluginNode(n) || coordinate(data, n) != "org.apache.maven.plugins:maven-compiler-plugin" {
				return
			}
			walk(n, func(c *element) {
				if parent != nil || c.Name != "configuration" {
					return
				}
				for _, key := range []string{"release", "source", "target", "testRelease", "testSource", "testTarget"} {
					if v := c.child(key); v != nil && scalar(data, v) != java {
						parent, name = c, key
						return
					}
				}
			})
		})
		if parent == nil {
			break
		}
		before := data
		data, err = setScalar(data, parent, name, java)
		if err != nil {
			return nil, nil, nil, err
		}
		record(before, "set explicit compiler "+name+" to "+java)
	}
	// Apply each declaration once. Collect byte offsets, then change backwards
	// so offsets for earlier declarations remain valid.
	root, err = parseXML(data)
	if err != nil {
		return nil, nil, nil, err
	}
	var declarations []*element
	walk(root, func(n *element) {
		if pluginNode(n) || dependencyNode(n) || processorNode(n) || (n.Name == "parent" && n.Parent == root) {
			declarations = append(declarations, n)
		}
	})
	sort.Slice(declarations, func(i, j int) bool { return declarations[i].Start > declarations[j].Start })
	for _, n := range declarations {
		// Earlier edits may have changed offsets inside an enclosing plugin.
		// Re-find this declaration by its structural path before each edit.
		var indexes []int
		for c := n; c.Parent != nil; c = c.Parent {
			for i, sibling := range c.Parent.Children {
				if sibling == c {
					indexes = append(indexes, i)
					break
				}
			}
		}
		currentRoot, parseErr := parseXML(data)
		if parseErr != nil {
			return nil, nil, nil, parseErr
		}
		n = currentRoot
		for i := len(indexes) - 1; i >= 0; i-- {
			n = n.Children[indexes[i]]
		}
		coord := coordinate(data, n)
		var target string
		switch {
		case pluginNode(n):
			target = policy.Plugins[coord]
		case dependencyNode(n) || processorNode(n):
			target = policy.Dependencies[coord]
		default:
			target = policy.Parents[coord]
		}
		if target == "" {
			continue
		}
		version := n.child("version")
		if version == nil && dependencyNode(n) {
			if model == nil {
				findings = append(findings, Finding{"info", path, coord + " is managed; update its owning BOM/parent or dependencyManagement declaration"})
			}
			continue
		}
		current := scalar(data, version)
		resolved := current
		if model != nil {
			p := &pomModel{path: path, data: data, root: currentRoot, parent: model.poms[path].parent}
			v := model.version(p, n)
			if v.err == nil {
				resolved = v.value
			}
		}
		if strings.HasPrefix(current, "${") && strings.HasSuffix(current, "}") {
			key := current[2 : len(current)-1]
			// Profile properties take precedence for a declaration in that profile.
			var property *element
			for owner := n.Parent; owner != nil; owner = owner.Parent {
				if props := owner.child("properties"); props != nil {
					if property = props.child(key); property != nil {
						break
					}
				}
			}
			if model == nil {
				resolved = scalar(data, property)
			}
			if resolved == current {
				resolved = ""
			}
			if resolved == "" {
				findings = append(findings, Finding{"blocker", path, coord + " references unresolved property " + current + "; pin it locally or update the owning parent"})
				continue
			}
		}
		if resolved == target {
			continue
		}
		order, comparable := compareArtifactVersions(coord, resolved, target)
		if current != "" && !comparable {
			findings = append(findings, Finding{"blocker", path, "cannot order qualified versions for " + coord + ": " + resolved + " -> " + target + "; pin the desired version manually"})
			continue
		}
		if current != "" && order > 0 {
			continue
		}
		if current == "" && resolved != "" && comparable && order >= 0 {
			continue
		}
		before := data
		data, err = setScalar(data, n, "version", target)
		if err != nil {
			return nil, nil, nil, err
		}
		record(before, "pin "+coord+" to "+target)
		if current == "" && pluginNode(n) {
			findings = append(findings, Finding{"warning", path, coord + " receives a local version pin; review any inherited plugin version"})
		}
	}
	// Activate lifecycle plugins even if the project relied on old defaults.
	root, err = parseXML(data)
	if err != nil {
		return nil, nil, nil, err
	}
	packaging := textAt(data, root, "packaging")
	activate := []string{"maven-clean-plugin", "maven-resources-plugin", "maven-compiler-plugin", "maven-surefire-plugin"}
	if packaging == "" || packaging == "jar" {
		activate = append(activate, "maven-jar-plugin")
	}
	if packaging == "war" {
		activate = append(activate, "maven-war-plugin")
	}
	for _, artifact := range activate {
		coord := "org.apache.maven.plugins:" + artifact
		target := policy.Plugins[coord]
		if target == "" {
			continue
		}
		root, _ = parseXML(data)
		build := root.child("build")
		if build == nil {
			data = insert(data, root, tag(root, "build", tag(root, "plugins", "")))
			root, _ = parseXML(data)
			build = root.child("build")
		}
		plugins := build.child("plugins")
		if plugins == nil {
			data = insert(data, build, tag(build, "plugins", ""))
			root, _ = parseXML(data)
			build = root.child("build")
			plugins = build.child("plugins")
		}
		has := false
		for _, n := range plugins.Children {
			if n.Name == "plugin" && coordinate(data, n) == coord {
				has = true
			}
		}
		if has {
			continue
		}
		// Keep newer versions that are visible in local plugin management.
		walk(root, func(n *element) {
			if pluginNode(n) && coordinate(data, n) == coord {
				v := textAt(data, n, "version")
				if numericVersion(v) && numericVersion(target) && compareVersions(v, target) > 0 {
					target = v
				}
			}
		})
		if model != nil {
			// Include locally inherited plugin versions when activating a plugin.
			for owner := model.poms[path].parent; owner != nil; owner = owner.parent {
				walk(owner.root, func(n *element) {
					if !pluginNode(n) || profileOf(n) != nil || coordinate(owner.data, n) != coord {
						return
					}
					v := model.resolve(model.poms[path], model.poms[path].root, textAt(owner.data, n, "version"))
					if v.err == nil && numericVersion(v.value) && numericVersion(target) && compareVersions(v.value, target) > 0 {
						target = v.value
					}
				})
			}
		}
		before := data
		data = insert(data, plugins, tag(plugins, "plugin", tag(plugins, "artifactId", artifact)+tag(plugins, "version", target)))
		record(before, "activate "+coord+" at "+target)
		if root.child("parent") != nil && (model == nil || model.poms[path].parent == nil) {
			findings = append(findings, Finding{"warning", path, coord + " receives a local version pin; review external parent plugin management"})
		}
	}
	if _, err := parseXML(data); err != nil {
		return nil, nil, nil, fmt.Errorf("transformed %s: %w", path, err)
	}
	return data, changes, findings, nil
}
