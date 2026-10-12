package upgrade

import (
	"fmt"
	"regexp"
	"sort"
)

// repositoryModel describes the statically resolvable Maven model. It never
// guesses remote BOM content, activated profiles, or user/settings properties.
type repositoryModel struct {
	poms  map[string]*pomModel
	paths []string
}

type pomModel struct {
	path   string
	data   []byte
	root   *element
	parent *pomModel
}

type propertyOwner struct{ path, name string }

type resolvedVersion struct {
	value       string
	owner       *propertyOwner
	declaration string
	err         error
}

type Artifact struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Coordinate string `json:"coordinate"`
	Declared   string `json:"declared_version,omitempty"`
	Resolved   string `json:"resolved_version,omitempty"`
	Owner      string `json:"version_owner,omitempty"`
	Property   string `json:"property,omitempty"`
	Profile    string `json:"profile,omitempty"`
	Problem    string `json:"problem,omitempty"`
}

type Inspection struct {
	Root      string     `json:"root"`
	POMs      []string   `json:"poms"`
	Artifacts []Artifact `json:"artifacts"`
}

var propertyReference = regexp.MustCompile(`\$\{([^{}]+)\}`)

func newRepositoryModel(contents map[string][]byte) (*repositoryModel, error) {
	m := &repositoryModel{poms: map[string]*pomModel{}}
	for path, data := range contents {
		n, err := parseXML(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		m.poms[path] = &pomModel{path: path, data: data, root: n}
		m.paths = append(m.paths, path)
	}
	sort.Strings(m.paths)
	// parentCandidate follows relativePath only inside the declared inventory.
	for _, path := range m.paths {
		p := m.poms[path]
		candidate, enabled := parentCandidate(path, p.data, p.root)
		if parent := m.poms[candidate]; enabled && parent != nil && parentMatches(p.data, p.root, parent.data, parent.root) {
			p.parent = parent
		}
		if p.parent == nil {
			if n := p.root.child("parent"); n != nil {
				p.parent = m.localArtifact(coordinate(p.data, n), textAt(p.data, n, "version"))
			}
		}
	}
	for _, path := range m.paths {
		seen := map[string]bool{}
		for p := m.poms[path]; p != nil; p = p.parent {
			if seen[p.path] {
				return nil, fmt.Errorf("local parent cycle involving %s", p.path)
			}
			seen[p.path] = true
		}
	}
	return m, nil
}

func profileOf(n *element) *element {
	for c := n; c != nil; c = c.Parent {
		if c.Name == "profile" && c.Parent != nil && c.Parent.Name == "profiles" {
			return c
		}
	}
	return nil
}

func artifactKind(n *element) string {
	if pluginNode(n) {
		return "plugin"
	}
	if n.Name == "parent" && n.Parent != nil && n.Parent.Name == "project" {
		return "parent"
	}
	if processorNode(n) {
		return "processor"
	}
	if dependencyNode(n) {
		if n.Parent.Parent != nil && n.Parent.Parent.Name == "dependencyManagement" {
			return "managed-dependency"
		}
		for c := n.Parent; c != nil; c = c.Parent {
			if c.Name == "plugin" {
				return "plugin-dependency"
			}
		}
		return "dependency"
	}
	return ""
}

func processorNode(n *element) bool {
	if n.Name != "path" || n.Parent == nil || n.Parent.Name != "annotationProcessorPaths" {
		return false
	}
	for c := n.Parent; c != nil; c = c.Parent {
		if pluginNode(c) {
			return true
		}
	}
	return false
}

func (m *repositoryModel) property(p *pomModel, context *element, key string, seen map[string]bool) resolvedVersion {
	id := p.path + ":" + key
	if seen[id] {
		return resolvedVersion{err: fmt.Errorf("property cycle at ${%s}", key)}
	}
	seen[id] = true
	defer delete(seen, id)
	var node *element
	owner := p
	if profile := profileOf(context); profile != nil {
		if props := profile.child("properties"); props != nil {
			node = props.child(key)
		}
	}
	for node == nil && owner != nil {
		if props := owner.root.child("properties"); props != nil {
			node = props.child(key)
		}
		if node == nil {
			owner = owner.parent
		}
	}
	if node == nil {
		return resolvedVersion{err: fmt.Errorf("unresolved property ${%s} (external parent, profile, or settings)", key)}
	}
	// Parent profile activation cannot be inferred statically in a child.
	for ancestor := p.parent; ancestor != nil; ancestor = ancestor.parent {
		if profiles := ancestor.root.child("profiles"); profiles != nil {
			for _, profile := range profiles.Children {
				if props := profile.child("properties"); props != nil && props.child(key) != nil {
					return resolvedVersion{err: fmt.Errorf("inherited property ${%s} is overridden by a parent profile", key)}
				}
			}
		}
	}
	value := scalar(owner.data, node)
	if refs := propertyReference.FindAllStringSubmatch(value, -1); len(refs) != 0 {
		if len(refs) != 1 || refs[0][0] != value {
			return resolvedVersion{err: fmt.Errorf("compound property value %q requires Maven interpolation", value)}
		}
		return m.property(p, context, refs[0][1], seen)
	}
	if profileOf(node) != nil {
		return resolvedVersion{value: value, declaration: owner.path}
	}
	return resolvedVersion{value: value, owner: &propertyOwner{owner.path, key}, declaration: owner.path}
}

func (m *repositoryModel) resolve(p *pomModel, context *element, value string) resolvedVersion {
	refs := propertyReference.FindAllStringSubmatch(value, -1)
	if len(refs) == 0 {
		return resolvedVersion{value: value, declaration: p.path}
	}
	if len(refs) != 1 || refs[0][0] != value {
		return resolvedVersion{err: fmt.Errorf("compound version %q requires Maven interpolation", value)}
	}
	return m.property(p, context, refs[0][1], map[string]bool{})
}

func managementKey(data []byte, n *element) string {
	typeName := textAt(data, n, "type")
	if typeName == "" {
		typeName = "jar"
	}
	return coordinate(data, n) + ":" + typeName + ":" + textAt(data, n, "classifier")
}

func (m *repositoryModel) managedDependency(p *pomModel, context *element, key string, seen map[string]bool) resolvedVersion {
	id := p.path + ":" + key
	if seen[id] {
		return resolvedVersion{err: fmt.Errorf("dependency management cycle for %s", key)}
	}
	seen[id] = true
	defer delete(seen, id)
	for owner := p; owner != nil; owner = owner.parent {
		var containers []*element
		if owner == p {
			if profile := profileOf(context); profile != nil {
				if dm := profile.child("dependencyManagement"); dm != nil {
					containers = append(containers, dm)
				}
			}
		}
		if dm := owner.root.child("dependencyManagement"); dm != nil {
			containers = append(containers, dm)
		}
		// Explicit management takes precedence over imported BOMs.
		for _, dm := range containers {
			if deps := dm.child("dependencies"); deps != nil {
				for _, n := range deps.Children {
					if managementKey(owner.data, n) == key && textAt(owner.data, n, "scope") != "import" {
						v := m.resolve(p, context, textAt(owner.data, n, "version"))
						v.declaration = owner.path
						if v.value == "" && v.err == nil {
							v.err = fmt.Errorf("managed declaration for %s has no version", key)
						}
						return v
					}
				}
			}
		}
		for _, dm := range containers {
			if deps := dm.child("dependencies"); deps != nil {
				for _, n := range deps.Children {
					if textAt(owner.data, n, "scope") != "import" || textAt(owner.data, n, "type") != "pom" {
						continue
					}
					version := m.resolve(p, context, textAt(owner.data, n, "version"))
					bom := m.localArtifact(coordinate(owner.data, n), version.value)
					if bom == nil {
						return resolvedVersion{err: fmt.Errorf("external BOM %s:%s may manage %s; its content is unavailable to the local model", coordinate(owner.data, n), version.value, key)}
					}
					v := m.managedDependency(bom, bom.root, key, seen)
					if v.err == nil && v.value != "" {
						return v
					}
				}
			}
		}
	}
	return resolvedVersion{err: fmt.Errorf("no resolvable local dependencyManagement for %s; update its owning external BOM/parent", key)}
}

func (m *repositoryModel) localArtifact(coord, version string) *pomModel {
	if version == "" {
		return nil
	}
	for _, path := range m.paths {
		p := m.poms[path]
		if projectCoordinate(p.data, p.root) == coord && projectVersion(p.data, p.root) == version {
			return p
		}
	}
	return nil
}

func (m *repositoryModel) version(p *pomModel, n *element) resolvedVersion {
	raw := textAt(p.data, n, "version")
	if raw != "" {
		return m.resolve(p, n, raw)
	}
	if artifactKind(n) == "dependency" {
		return m.managedDependency(p, n, managementKey(p.data, n), map[string]bool{})
	}
	if pluginNode(n) {
		for owner := p; owner != nil; owner = owner.parent {
			build := owner.root.child("build")
			if build == nil {
				continue
			}
			var groups []*element
			if owner != p {
				groups = append(groups, build.child("plugins"))
			}
			if pm := build.child("pluginManagement"); pm != nil {
				groups = append(groups, pm.child("plugins"))
			}
			for _, plugins := range groups {
				if plugins == nil {
					continue
				}
				for _, c := range plugins.Children {
					if coordinate(owner.data, c) == coordinate(p.data, n) && textAt(owner.data, c, "version") != "" {
						v := m.resolve(p, n, textAt(owner.data, c, "version"))
						v.declaration = owner.path
						return v
					}
				}
			}
		}
	}
	return resolvedVersion{err: fmt.Errorf("version is inherited or implicit outside the local model")}
}

func policyVersion(policy Policy, kind, coord string) string {
	switch kind {
	case "plugin":
		return policy.Plugins[coord]
	case "parent":
		return policy.Parents[coord]
	default:
		return policy.Dependencies[coord]
	}
}

func (m *repositoryModel) inspect(root string) Inspection {
	report := Inspection{Root: root, POMs: append([]string(nil), m.paths...), Artifacts: []Artifact{}}
	for _, path := range m.paths {
		p := m.poms[path]
		walk(p.root, func(n *element) {
			kind := artifactKind(n)
			if kind == "" {
				return
			}
			v := m.version(p, n)
			a := Artifact{Path: path, Kind: kind, Coordinate: coordinate(p.data, n), Declared: textAt(p.data, n, "version"), Resolved: v.value, Owner: v.declaration}
			if v.owner != nil {
				a.Property = v.owner.name
				a.Owner = v.owner.path
			}
			if profile := profileOf(n); profile != nil {
				a.Profile = textAt(p.data, profile, "id")
			}
			if v.err != nil {
				a.Problem = v.err.Error()
			}
			report.Artifacts = append(report.Artifacts, a)
		})
	}
	return report
}

func Inspect(root string) (Inspection, error) {
	root, err := canonicalRoot(root)
	if err != nil {
		return Inspection{}, err
	}
	contents, err := readRepositoryPOMs(root)
	if err != nil {
		return Inspection{}, err
	}
	m, err := newRepositoryModel(contents)
	if err != nil {
		return Inspection{}, err
	}
	return m.inspect(root), nil
}

// Properties are changed at their owner only when every known terminal use is
// an artifact explicitly selected for the same target. Other shared uses keep
// the property intact and get declaration-local pins instead.
func updateOwnedProperties(contents map[string][]byte, policy Policy) ([]Change, error) {
	m, err := newRepositoryModel(contents)
	if err != nil {
		return nil, err
	}
	type request struct {
		target   string
		approved bool
	}
	requests := map[propertyOwner]*request{}
	for _, path := range m.paths {
		p := m.poms[path]
		walk(p.root, func(n *element) {
			if artifactKind(n) == "" {
				return
			}
			v := m.version(p, n)
			target := policyVersion(policy, artifactKind(n), coordinate(p.data, n))
			order, comparable := compareArtifactVersions(coordinate(p.data, n), v.value, target)
			if v.err != nil || v.owner == nil || target == "" || !comparable || order >= 0 {
				return
			}
			r := requests[*v.owner]
			if r == nil {
				requests[*v.owner] = &request{target, true}
			} else if r.target != target {
				r.approved = false
			}
		})
	}
	for _, path := range m.paths {
		p := m.poms[path]
		walk(p.root, func(n *element) {
			if len(n.Children) != 0 {
				return
			}
			// Property aliases forward consumers; scan their terminal uses instead.
			if n.Parent != nil && n.Parent.Name == "properties" {
				return
			}
			for _, ref := range propertyReference.FindAllStringSubmatch(scalar(p.data, n), -1) {
				v := m.property(p, n, ref[1], map[string]bool{})
				if v.owner == nil {
					continue
				}
				r := requests[*v.owner]
				if r == nil {
					continue
				}
				if n.Name != "version" || n.Parent == nil || artifactKind(n.Parent) == "" || policyVersion(policy, artifactKind(n.Parent), coordinate(p.data, n.Parent)) != r.target {
					r.approved = false
				}
			}
		})
	}
	var owners []propertyOwner
	for owner, r := range requests {
		if r.approved {
			owners = append(owners, owner)
		}
	}
	sort.Slice(owners, func(i, j int) bool {
		if owners[i].path != owners[j].path {
			return owners[i].path < owners[j].path
		}
		return owners[i].name < owners[j].name
	})
	var changes []Change
	for _, owner := range owners {
		data := contents[owner.path]
		root, err := parseXML(data)
		if err != nil {
			return nil, err
		}
		data, err = setScalar(data, root.child("properties"), owner.name, requests[owner].target)
		if err != nil {
			return nil, err
		}
		contents[owner.path] = data
		changes = append(changes, Change{owner.path, "update owned property " + owner.name + " to " + requests[owner].target})
	}
	return changes, nil
}

func unresolvedPolicyFindings(m *repositoryModel, policy Policy) []Finding {
	var findings []Finding
	for _, path := range m.paths {
		p := m.poms[path]
		walk(p.root, func(n *element) {
			kind := artifactKind(n)
			if kind == "" {
				return
			}
			coord := coordinate(p.data, n)
			target := policyVersion(policy, kind, coord)
			if target == "" {
				return
			}
			v := m.version(p, n)
			if v.err != nil {
				findings = append(findings, Finding{"blocker", path, "requested upgrade for " + coord + " could not resolve its version: " + v.err.Error()})
				return
			}
			order, comparable := compareArtifactVersions(coord, v.value, target)
			if v.value != target && (!comparable || order < 0) {
				findings = append(findings, Finding{"blocker", path, "requested upgrade for " + coord + " remains at " + v.value + "; target is " + target + " (owner " + v.declaration + ")"})
			}
		})
	}
	return findings
}
