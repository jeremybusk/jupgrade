package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// MavenModel is immutable evidence, embedded in a resolved policy. Model paths
// never become writable repository paths and caches are never edited.
type MavenModel struct {
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

var mavenName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

func validateMavenModels(models map[string]MavenModel) error {
	keys := make([]string, 0, len(models))
	for key := range models {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, gav := range keys {
		parts := strings.Split(gav, ":")
		if len(parts) != 3 || !mavenName.MatchString(parts[0]) || !mavenName.MatchString(parts[1]) || !exactVersion.MatchString(parts[2]) || wildcardVersion.MatchString(parts[2]) {
			return fmt.Errorf("invalid Maven snapshot coordinate %q", gav)
		}
		model := models[gav]
		if hash([]byte(model.Content)) != model.SHA256 {
			return fmt.Errorf("Maven snapshot hash mismatch for %s", gav)
		}
		root, err := parseXML([]byte(model.Content))
		if err != nil {
			return fmt.Errorf("Maven snapshot %s: %w", gav, err)
		}
		if projectCoordinate([]byte(model.Content), root) != parts[0]+":"+parts[1] || projectVersion([]byte(model.Content), root) != parts[2] {
			return fmt.Errorf("Maven snapshot identity mismatch for %s", gav)
		}
	}
	return nil
}

// ResolveMaven captures reachable cached parent/BOM models, including requested
// replacements. It is offline and fails rather than inventing unavailable models.
func ResolveMaven(root string, policy Policy, cache string) (Policy, error) {
	if err := policy.Validate(); err != nil {
		return policy, err
	}
	root, err := canonicalRoot(root)
	if err != nil {
		return policy, err
	}
	if build, err := detectBuild(root, policy.Build); err != nil || build != "maven" {
		if err == nil {
			err = fmt.Errorf("Maven resolution requires a Maven build")
		}
		return policy, err
	}
	contents, err := readRepositoryPOMs(root)
	if err != nil {
		return policy, err
	}
	if cache == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return policy, err
		}
		cache = filepath.Join(home, ".m2", "repository")
	}
	cache, err = canonicalRoot(cache)
	if err != nil {
		return policy, fmt.Errorf("Maven cache: %w", err)
	}
	models := map[string]MavenModel{}
	for gav, model := range policy.MavenModels {
		models[gav] = model
	}
	policy.MavenModels = models
	for {
		m, err := newRepositoryModel(contents, models)
		if err != nil {
			return policy, err
		}
		all := make([]string, 0, len(m.poms))
		for path := range m.poms {
			all = append(all, path)
		}
		sort.Strings(all)
		requested := map[string]bool{}
		var unresolved error
		for _, path := range all {
			p := m.poms[path]
			walk(p.root, func(n *element) {
				parent := n.Name == "parent" && n.Parent == p.root
				bom := dependencyNode(n) && n.Parent.Parent != nil && n.Parent.Parent.Name == "dependencyManagement" && textAt(p.data, n, "type") == "pom" && textAt(p.data, n, "scope") == "import"
				if !parent && !bom {
					return
				}
				coord := coordinate(p.data, n)
				v := m.resolve(p, n, textAt(p.data, n, "version"))
				if v.err != nil || v.value == "" {
					if unresolved == nil {
						unresolved = fmt.Errorf("cannot resolve cached model reference %s in %s", coord, path)
					}
					return
				}
				if m.localArtifact(coord, v.value) == nil {
					requested[coord+":"+v.value] = true
				}
				kind := "managed-dependency"
				if parent {
					kind = "parent"
				}
				if target := policyVersion(policy, kind, coord); target != "" && m.localArtifact(coord, target) == nil {
					requested[coord+":"+target] = true
				}
			})
		}
		if len(requested) == 0 {
			if unresolved != nil {
				return policy, unresolved
			}
			return policy, validateMavenModels(models)
		}
		keys := make([]string, 0, len(requested))
		for gav := range requested {
			keys = append(keys, gav)
		}
		sort.Strings(keys)
		for _, gav := range keys {
			parts := strings.Split(gav, ":")
			if len(parts) != 3 || !mavenName.MatchString(parts[0]) || !mavenName.MatchString(parts[1]) || !exactVersion.MatchString(parts[2]) || wildcardVersion.MatchString(parts[2]) {
				return policy, fmt.Errorf("model reference must be an exact Maven coordinate: %s", gav)
			}
			relative := filepath.Join(filepath.FromSlash(strings.ReplaceAll(parts[0], ".", "/")), parts[1], parts[2], parts[1]+"-"+parts[2]+".pom")
			path, err := safePath(cache, relative)
			if err != nil {
				return policy, fmt.Errorf("cached Maven model %s is unavailable; resolve it with Maven first: %w", gav, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return policy, err
			}
			models[gav] = MavenModel{hash(data), string(data)}
		}
		if err := validateMavenModels(models); err != nil {
			return policy, err
		}
	}
}
