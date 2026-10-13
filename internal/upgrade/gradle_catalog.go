package upgrade

import (
	"fmt"
	"regexp"
	"strings"
)

type catalogRecord struct {
	coord, target, ref, current string
	value                       buildToken
	refKeyStart, refKeyEnd      int
}

// Support literal single-line catalogs with shared version ownership. Executable
// or rich version semantics require a model worker and are explicit blockers.
func migrateVersionCatalog(data []byte, path string, policy Policy) ([]byte, []Change, []Finding, error) {
	versions := map[string]buildToken{}
	var records []*catalogRecord
	section := ""
	offset := 0
	blocked := func(message string) ([]byte, []Change, []Finding, error) {
		return data, nil, []Finding{{"blocker", path, message}}, nil
	}
	keyPattern := regexp.MustCompile(`^\s*([A-Za-z0-9_.-]+)\s*=\s*`)
	for _, line := range strings.SplitAfter(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			offset += len(line)
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			end := strings.Index(trimmed, "]")
			if end < 0 {
				return blocked("invalid version catalog table")
			}
			section = strings.TrimSpace(trimmed[1:end])
			if strings.HasPrefix(section, "libraries.") || strings.HasPrefix(section, "plugins.") || strings.HasPrefix(section, "versions.") {
				return blocked("catalog sub-tables require a Gradle model worker; use literal inline entries")
			}
			offset += len(line)
			continue
		}
		if section != "versions" && section != "libraries" && section != "plugins" {
			offset += len(line)
			continue
		}
		match := keyPattern.FindStringSubmatchIndex(line)
		if match == nil {
			return blocked("catalog entries must use bare literal alias keys")
		}
		alias := line[match[2]:match[3]]
		valueStart := match[1]
		tokens, err := lexBuild([]byte(line[valueStart:]), true)
		if err != nil {
			return blocked(err.Error())
		}
		for i := range tokens {
			tokens[i].start += offset + valueStart
			tokens[i].end += offset + valueStart
		}
		if len(tokens) == 0 {
			return blocked("empty catalog entry " + alias)
		}
		if section == "versions" {
			if len(tokens) != 1 || tokens[0].kind != "string" {
				return blocked("rich/dynamic catalog version " + alias + " requires model-aware resolution")
			}
			if _, exists := versions[alias]; exists {
				return blocked("duplicate catalog version " + alias)
			}
			versions[alias] = tokens[0]
			offset += len(line)
			continue
		}
		r := &catalogRecord{refKeyStart: -1}
		if len(tokens) == 1 && tokens[0].kind == "string" && section == "libraries" {
			parts := strings.Split(tokens[0].value, ":")
			if len(parts) != 3 {
				return blocked("catalog shorthand must contain group:artifact:version")
			}
			r.coord = parts[0] + ":" + parts[1]
			r.current = parts[2]
			r.value = tokens[0]
		} else {
			if tokens[0].value != "{" || tokens[len(tokens)-1].value != "}" {
				return blocked("catalog entries must be single-line inline tables")
			}
			fields := map[string]buildToken{}
			keys := map[string][2]int{}
			for i := 1; i < len(tokens)-1; {
				start := i
				key := tokens[i].value
				i++
				for i+1 < len(tokens) && tokens[i].value == "." {
					key += "." + tokens[i+1].value
					i += 2
				}
				if i+1 >= len(tokens) || tokens[i].value != "=" || tokens[i+1].kind != "string" {
					return blocked("nonliteral catalog field " + key + " requires a Gradle model worker")
				}
				if _, exists := fields[key]; exists {
					return blocked("duplicate catalog field " + key)
				}
				keys[key] = [2]int{tokens[start].start, tokens[i-1].end}
				fields[key] = tokens[i+1]
				i += 2
				if i < len(tokens)-1 {
					if tokens[i].value != "," {
						return blocked("invalid inline catalog separators")
					}
					i++
				}
			}
			if section == "plugins" {
				r.coord = "gradle-plugin:" + fields["id"].value
			} else if module := fields["module"].value; module != "" {
				r.coord = module
			} else {
				r.coord = fields["group"].value + ":" + fields["name"].value
			}
			if v, ok := fields["version"]; ok {
				r.current = v.value
				r.value = v
			} else if v, ok := fields["version.ref"]; ok {
				r.ref = v.value
				r.value = v
				r.refKeyStart = keys["version.ref"][0]
				r.refKeyEnd = keys["version.ref"][1]
			}
		}
		if section == "plugins" {
			r.target = policy.GradlePlugins[strings.TrimPrefix(r.coord, "gradle-plugin:")]
		} else {
			r.target = policy.Dependencies[r.coord]
		}
		records = append(records, r)
		offset += len(line)
	}
	refs := map[string][]*catalogRecord{}
	for _, r := range records {
		if r.ref != "" {
			refs[r.ref] = append(refs[r.ref], r)
		}
	}
	updatedRefs := map[string]bool{}
	var edits []buildEdit
	var findings []Finding
	floors := compatibilityFloors(policy.Java)
	for _, r := range records {
		current := r.current
		if r.ref != "" {
			v, ok := versions[r.ref]
			if !ok {
				return blocked("missing catalog version reference " + r.ref)
			}
			current = v.value
		}
		if r.coord == "gradle-plugin:org.springframework.boot" {
			planned := current
			if numericVersion(current) && numericVersion(r.target) && compareVersions(current, r.target) < 0 {
				planned = r.target
			}
			if message := bootGradleCompatibility(planned, policy.Gradle); message != "" {
				return blocked(message)
			}
		}
		if message := gradleLibraryCompatibility(r.coord, current, r.target, floors, nil); message != "" {
			return blocked(message)
		}
		if r.target == "" {
			continue
		}
		if current == "" {
			return blocked("selected catalog entry " + r.coord + " is versionless; update its owning platform/BOM")
		}
		order, known := compareArtifactVersions(r.coord, current, r.target)
		if current == r.target || known && order >= 0 {
			continue
		}
		if !known {
			return blocked("cannot order catalog versions for " + r.coord + ": " + current + " -> " + r.target)
		}
		if r.coord == "gradle-plugin:org.springframework.boot" && strings.Split(current, ".")[0] != strings.Split(r.target, ".")[0] {
			return blocked("Spring Boot catalog major upgrade requires a source recipe")
		}
		reason := "pin catalog " + r.coord + " to " + r.target
		if r.ref != "" {
			own := true
			for _, consumer := range refs[r.ref] {
				if consumer.target != r.target {
					own = false
				}
			}
			if own {
				if !updatedRefs[r.ref] {
					edits = append(edits, tokenEdit(versions[r.ref], r.target, "update catalog version "+r.ref+" to "+r.target))
					updatedRefs[r.ref] = true
				}
			} else {
				edits = append(edits, buildEdit{r.refKeyStart, r.refKeyEnd, "version", reason}, tokenEdit(r.value, r.target, reason))
			}
		} else {
			value := r.target
			if strings.Count(r.value.value, ":") == 2 {
				parts := strings.Split(r.value.value, ":")
				parts[2] = r.target
				value = strings.Join(parts, ":")
			}
			edits = append(edits, tokenEdit(r.value, value, reason))
		}
	}
	after, changes, err := applyBuildEdits(data, path, edits)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("catalog: %w", err)
	}
	return after, changes, findings, nil
}
