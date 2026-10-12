package upgrade

import (
	"fmt"
	"strings"
)

// Compilation success cannot detect an old bytecode parser on a server/plugin
// classpath. Reject incompatible versions that can be established locally.
func jettyRuntimeFindings(model *repositoryModel, java int) []Finding {
	var findings []Finding
	minimumASM := map[int]string{17: "9.1", 21: "9.5", 25: "9.8"}[java]
	for _, path := range model.paths {
		p := model.poms[path]
		walk(p.root, func(n *element) {
			if !pluginNode(n) || coordinate(p.data, n) != "org.eclipse.jetty:jetty-maven-plugin" {
				return
			}
			hasASM := false
			if deps := n.child("dependencies"); deps != nil {
				for _, d := range deps.Children {
					coord := coordinate(p.data, d)
					if !strings.HasPrefix(coord, "org.ow2.asm:") {
						continue
					}
					if coord == "org.ow2.asm:asm" {
						hasASM = true
					}
					v := model.version(p, d)
					if v.err != nil || !numericVersion(v.value) || compareVersions(v.value, minimumASM) < 0 {
						findings = append(findings, Finding{"blocker", path, fmt.Sprintf("Jetty explicitly overrides ASM with %s %s, which cannot be verified for Java %d; upgrade ASM and its related plugin dependencies to >= %s", coord, v.value, java, minimumASM)})
					}
				}
			}
			if hasASM {
				return
			}
			v := model.version(p, n)
			if v.err != nil || !strings.HasPrefix(v.value, "9.") {
				return
			}
			order, known := compareArtifactVersions("org.eclipse.jetty:jetty-maven-plugin", v.value, "9.4.48.v20220622")
			if (java == 21 && known && order <= 0) || java == 25 {
				message := fmt.Sprintf("legacy Jetty %s has an annotation scanner that cannot be verified for Java %d; Maven clean verify does not exercise jetty:run", v.value, java)
				if java == 21 {
					message += "; add org.eclipse.jetty:jetty-maven-plugin to the upgrade policy (default: 9.4.58.v20250814)"
				} else {
					message += "; migrate the runtime or explicitly configure compatible ASM plugin dependencies >= " + minimumASM
				}
				findings = append(findings, Finding{"blocker", path, message})
			}
		})
	}
	return findings
}

func libraryRuntimeFindings(model *repositoryModel, java int) []Finding {
	floors := map[string]string{
		"org.projectlombok:lombok":       map[int]string{17: "1.18.22", 21: "1.18.30", 25: "1.18.40"}[java],
		"org.jacoco:jacoco-maven-plugin": map[int]string{17: "0.8.8", 21: "0.8.11", 25: "0.8.14"}[java],
	}
	for _, name := range []string{"asm", "asm-tree", "asm-commons", "asm-analysis", "asm-util"} {
		floors["org.ow2.asm:"+name] = map[int]string{17: "9.1", 21: "9.5", 25: "9.8"}[java]
	}
	var findings []Finding
	for _, path := range model.paths {
		p := model.poms[path]
		walk(p.root, func(n *element) {
			if artifactKind(n) == "" {
				return
			}
			coord := coordinate(p.data, n)
			floor := floors[coord]
			if floor == "" {
				return
			}
			v := model.version(p, n)
			if v.err == nil && numericVersion(v.value) && compareVersions(v.value, floor) < 0 {
				findings = append(findings, Finding{"blocker", path, fmt.Sprintf("%s %s is below the Java %d compatibility floor %s; select --preset common or provide an explicit version rule", coord, v.value, java, floor)})
			}
		})
	}
	return findings
}

// A Boot major upgrade requires type-aware API/configuration recipes. Merely
// changing the parent, BOM or plugin must not be presented as such a migration.
func springBootMigrationFindings(originals map[string][]byte, policy Policy) ([]Finding, error) {
	model, err := newRepositoryModel(originals)
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, path := range model.paths {
		p := model.poms[path]
		walk(p.root, func(n *element) {
			coord := coordinate(p.data, n)
			if !strings.HasPrefix(coord, "org.springframework.boot:") || artifactKind(n) == "" {
				return
			}
			target := policyVersion(policy, artifactKind(n), coord)
			if target == "" {
				return
			}
			v := model.version(p, n)
			if v.err != nil || !numericVersion(v.value) || !numericVersion(target) || compareVersions(v.value, target) >= 0 {
				return
			}
			if compareVersions(strings.Split(v.value, ".")[0], strings.Split(target, ".")[0]) != 0 {
				findings = append(findings, Finding{"blocker", path, fmt.Sprintf("Spring Boot %s -> %s crosses a major version; framework source/configuration recipes are required before applying this plan", v.value, target)})
			} else {
				findings = append(findings, Finding{"warning", path, fmt.Sprintf("Spring Boot %s -> %s updates version declarations; review release notes and run application startup/integration tests", v.value, target)})
			}
		})
	}
	return findings, nil
}
