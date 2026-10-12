package upgrade

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const mavenWrapperProperties = ".mvn/wrapper/maven-wrapper.properties"

// This deliberately handles standard single-line wrapper properties only.
// Continuations/duplicates must be reviewed instead of changing the wrong URL.
func wrapperProperty(data []byte, key string) (string, int, int, error) {
	pattern := regexp.MustCompile(`(?m)^[\t ]*` + regexp.QuoteMeta(key) + `[\t ]*[=:][\t ]*([^\r\n]*)`)
	matches := pattern.FindAllSubmatchIndex(data, -1)
	if len(matches) > 1 {
		return "", 0, 0, fmt.Errorf("duplicate wrapper property %s", key)
	}
	if len(matches) == 0 {
		return "", 0, 0, nil
	}
	m := matches[0]
	raw := string(data[m[2]:m[3]])
	if strings.HasSuffix(strings.TrimRight(raw, "\t "), `\`) {
		return "", 0, 0, fmt.Errorf("continued wrapper property %s is unsupported", key)
	}
	return strings.TrimSpace(raw), m[2], m[3], nil
}

func migrateMavenWrapper(original []byte, policy Policy) ([]byte, []Change, []Finding) {
	data := append([]byte(nil), original...)
	blocked := func(message string) ([]byte, []Change, []Finding) {
		return original, nil, []Finding{{"blocker", mavenWrapperProperties, message}}
	}
	raw, start, end, err := wrapperProperty(data, "distributionUrl")
	if err != nil {
		return blocked(err.Error())
	}
	if raw == "" {
		return blocked("Maven wrapper has no distributionUrl")
	}
	decoded := strings.ReplaceAll(raw, `\:`, ":")
	u, err := url.Parse(decoded)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return blocked("Maven wrapper distributionUrl must be a standard HTTPS Maven binary archive without query/fragment")
	}
	archive := regexp.MustCompile(`(^|/)apache-maven-([0-9]+(?:\.[0-9]+)*)-bin\.(zip|tar\.gz)$`)
	parts := archive.FindStringSubmatch(u.Path)
	if len(parts) == 0 {
		return blocked("cannot determine Maven wrapper distribution version from its binary archive URL")
	}
	current := parts[2]
	if compareVersions(current, "4.0.0") >= 0 {
		return blocked("Maven 4 wrapper migrations are not implemented yet")
	}
	checksum, _, _, err := wrapperProperty(data, "distributionSha256Sum")
	if err != nil {
		return blocked(err.Error())
	}
	if compareVersions(current, policy.Maven) > 0 {
		return data, nil, []Finding{{"info", mavenWrapperProperties, "retained newer Maven wrapper " + current}}
	}
	if current != policy.Maven && checksum != "" && policy.MavenSHA256 == "" {
		return blocked("Maven wrapper has a distribution checksum; provide --maven-sha256 for the target archive to preserve checksum verification")
	}
	var changes []Change
	var findings []Finding
	if current != policy.Maven {
		// Recognize Central and Apache binary layouts while retaining mirror hosts,
		// escaped property colons, archive type, comments, whitespace, and line endings.
		central := "/org/apache/maven/apache-maven/" + current + "/apache-maven-" + current + "-bin."
		apache := "/maven-3/" + current + "/binaries/apache-maven-" + current + "-bin."
		newURL := raw
		switch {
		case strings.Contains(u.Path, central):
			newURL = strings.Replace(raw, central, "/org/apache/maven/apache-maven/"+policy.Maven+"/apache-maven-"+policy.Maven+"-bin.", 1)
		case strings.Contains(u.Path, apache):
			newURL = strings.Replace(raw, apache, "/maven-3/"+policy.Maven+"/binaries/apache-maven-"+policy.Maven+"-bin.", 1)
		default:
			return blocked("custom Maven wrapper URL layout is unsupported; update it manually or use a standard Maven archive URL")
		}
		// Preserve trailing spaces outside the scalar value.
		oldValue := string(data[start:end])
		trailing := oldValue[len(strings.TrimRight(oldValue, "\t ")):]
		data = replace(data, start, end, newURL+trailing)
		changes = append(changes, Change{mavenWrapperProperties, "pin Maven wrapper to " + policy.Maven})
	}
	if policy.MavenSHA256 != "" && checksum != strings.ToLower(policy.MavenSHA256) {
		_, a, b, _ := wrapperProperty(data, "distributionSha256Sum")
		value := strings.ToLower(policy.MavenSHA256)
		if a > 0 {
			data = replace(data, a, b, value)
		} else {
			newline := "\n"
			if bytes.Contains(data, []byte("\r\n")) {
				newline = "\r\n"
			}
			if len(data) > 0 && data[len(data)-1] != '\n' {
				data = append(data, newline...)
			}
			data = append(data, []byte("distributionSha256Sum="+value+newline)...)
		}
		changes = append(changes, Change{mavenWrapperProperties, "set Maven distribution SHA-256"})
	}
	if checksum == "" && policy.MavenSHA256 == "" {
		findings = append(findings, Finding{"warning", mavenWrapperProperties, "Maven distribution has no SHA-256 pin; supply --maven-sha256 to verify the target archive"})
	}
	return data, changes, findings
}

func contentsForPOMs(contents map[string][]byte) map[string][]byte {
	poms := map[string][]byte{}
	for path, data := range contents {
		if filepath.Base(path) == "pom.xml" {
			poms[path] = data
		}
	}
	return poms
}

func readRepositoryInputs(root string, policy Policy) (map[string][]byte, error) {
	contents, err := readRepositoryPOMs(root)
	if err != nil || policy.Maven == "" {
		return contents, err
	}
	_, err = os.Lstat(filepath.Join(root, mavenWrapperProperties))
	if os.IsNotExist(err) {
		return contents, nil
	}
	if err != nil {
		return nil, err
	}
	path, err := safePath(root, mavenWrapperProperties)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	contents[mavenWrapperProperties] = data
	return contents, nil
}
