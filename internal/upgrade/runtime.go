package upgrade

import "strings"

// Compilation success cannot detect an old bytecode parser on a server/plugin
// classpath. Reject the known legacy Jetty scanner unless its ASM is overridden.
func jettyRuntimeFindings(model *repositoryModel) []Finding {
	var findings []Finding
	for _, path := range model.paths {
		p := model.poms[path]
		walk(p.root, func(n *element) {
			if !pluginNode(n) || coordinate(p.data, n) != "org.eclipse.jetty:jetty-maven-plugin" {
				return
			}
			var asm *resolvedVersion
			if deps := n.child("dependencies"); deps != nil {
				for _, d := range deps.Children {
					if coordinate(p.data, d) == "org.ow2.asm:asm" {
						v := model.version(p, d)
						asm = &v
					}
				}
			}
			if asm != nil {
				if asm.err != nil || !numericVersion(asm.value) || compareVersions(asm.value, "9.5") < 0 {
					findings = append(findings, Finding{"blocker", path, "Jetty explicitly overrides ASM with a version that cannot be verified for Java 21; upgrade org.ow2.asm:asm and its related plugin dependencies to ASM >= 9.5"})
				}
				return
			}
			version := model.version(p, n)
			order, known := compareArtifactVersions("org.eclipse.jetty:jetty-maven-plugin", version.value, "9.4.48.v20220622")
			if version.err == nil && known && order <= 0 && strings.HasPrefix(version.value, "9.") {
				findings = append(findings, Finding{"blocker", path, "legacy Jetty " + version.value + " has an annotation scanner that cannot read Java 21 class files; add org.eclipse.jetty:jetty-maven-plugin to the upgrade policy (default: 9.4.58.v20250814). Maven clean verify does not exercise jetty:run"})
			}
		})
	}
	return findings
}
