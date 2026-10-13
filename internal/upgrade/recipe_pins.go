package upgrade

import (
	"fmt"
	"strings"
)

// Protect explicit overrides when a framework recipe replaces/removes version
// pins. New managed versions must be established from captured model evidence.
func validateSourceVersionPins(before, after map[string][]byte, policy Policy, build string) error {
	if build != "maven" {
		return nil
	}
	oldModel, err := newRepositoryModel(contentsForPOMs(before), policy.MavenModels)
	if err != nil {
		return err
	}
	newModel, err := newRepositoryModel(contentsForPOMs(after), policy.MavenModels)
	if err != nil {
		return err
	}
	for _, path := range oldModel.paths {
		old := oldModel.poms[path]
		next := newModel.poms[path]
		if next == nil {
			continue
		}
		declarations := map[string][]*element{}
		key := func(p *pomModel, n *element) string {
			profile := ""
			if value := profileOf(n); value != nil {
				profile = textAt(p.data, value, "id")
			}
			return artifactKind(n) + ":" + coordinate(p.data, n) + ":" + managementKey(p.data, n) + ":" + profile
		}
		walk(next.root, func(n *element) {
			if artifactKind(n) != "" {
				k := key(next, n)
				declarations[k] = append(declarations[k], n)
			}
		})
		var problem error
		walk(old.root, func(n *element) {
			if problem != nil || artifactKind(n) == "" {
				return
			}
			k := key(old, n)
			matches := declarations[k]
			if len(matches) == 0 {
				return
			}
			replacement := matches[0]
			declarations[k] = matches[1:]
			if textAt(old.data, n, "version") == "" {
				return
			}
			oldVersion := oldModel.version(old, n)
			newVersion := newModel.version(next, replacement)
			if oldVersion.err == nil && newVersion.err == nil && oldVersion.value == newVersion.value {
				return
			}
			removed := textAt(next.data, replacement, "version") == ""
			if oldVersion.err != nil || newVersion.err != nil {
				if removed {
					problem = fmt.Errorf("source recipe removed an explicit %s version pin in %s without resolvable management; capture its parent/BOM or keep an explicit rule", coordinate(old.data, n), path)
				}
				return
			}
			order, known := compareArtifactVersions(coordinate(old.data, n), oldVersion.value, newVersion.value)
			if known && order > 0 {
				problem = fmt.Errorf("source recipe would downgrade explicit %s %s -> %s in %s; preserve the override", coordinate(old.data, n), oldVersion.value, newVersion.value, path)
			}
			if removed && !known && strings.TrimSpace(oldVersion.value) != strings.TrimSpace(newVersion.value) {
				problem = fmt.Errorf("cannot verify removal of qualified version pin for %s in %s", coordinate(old.data, n), path)
			}
		})
		if problem != nil {
			return problem
		}
	}
	return nil
}
