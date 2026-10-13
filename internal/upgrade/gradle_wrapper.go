package upgrade

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var gradleDistributionPattern = regexp.MustCompile(`(^|/)gradle-([0-9]+(?:\.[0-9]+)+)-(bin|all)\.zip$`)

func gradleWrapperVersion(data []byte) string {
	raw, _, _, err := wrapperProperty(data, "distributionUrl")
	if err != nil {
		return ""
	}
	u, err := url.Parse(strings.ReplaceAll(raw, `\:`, ":"))
	if err != nil {
		return ""
	}
	parts := gradleDistributionPattern.FindStringSubmatch(u.Path)
	if len(parts) == 0 {
		return ""
	}
	return parts[2]
}

func migrateGradleWrapper(original []byte, policy Policy) ([]byte, []Change, []Finding) {
	blocked := func(message string) ([]byte, []Change, []Finding) {
		return original, nil, []Finding{{"blocker", gradleWrapperProperties, message}}
	}
	raw, start, end, err := wrapperProperty(original, "distributionUrl")
	if err != nil {
		return blocked(err.Error())
	}
	u, err := url.Parse(strings.ReplaceAll(raw, `\:`, ":"))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return blocked("Gradle wrapper requires a standard HTTPS distribution URL")
	}
	parts := gradleDistributionPattern.FindStringSubmatch(u.Path)
	if len(parts) == 0 {
		return blocked("cannot determine Gradle distribution version")
	}
	current := parts[2]
	checksum, a, b, err := wrapperProperty(original, "distributionSha256Sum")
	if err != nil {
		return blocked(err.Error())
	}
	if compareVersions(current, policy.Gradle) > 0 {
		return original, nil, []Finding{{"info", gradleWrapperProperties, "retained newer Gradle wrapper " + current}}
	}
	if current != policy.Gradle && checksum != "" && policy.GradleSHA256 == "" {
		return blocked("Gradle wrapper has a distribution checksum; provide --gradle-sha256 for the target archive")
	}
	data := append([]byte(nil), original...)
	var changes []Change
	if current != policy.Gradle {
		old := string(data[start:end])
		trailing := old[len(strings.TrimRight(old, " \t")):]
		newURL := strings.Replace(raw, "gradle-"+current+"-"+parts[3]+".zip", "gradle-"+policy.Gradle+"-"+parts[3]+".zip", 1)
		data = replace(data, start, end, newURL+trailing)
		changes = append(changes, Change{gradleWrapperProperties, "pin Gradle wrapper to " + policy.Gradle})
	}
	if policy.GradleSHA256 != "" && checksum != strings.ToLower(policy.GradleSHA256) {
		_, a, b, _ = wrapperProperty(data, "distributionSha256Sum")
		value := strings.ToLower(policy.GradleSHA256)
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
		changes = append(changes, Change{gradleWrapperProperties, "set Gradle distribution SHA-256"})
	}
	var findings []Finding
	if checksum == "" && policy.GradleSHA256 == "" {
		findings = append(findings, Finding{"warning", gradleWrapperProperties, fmt.Sprintf("Gradle %s distribution has no SHA-256 pin; supply --gradle-sha256", policy.Gradle)})
	}
	return data, changes, findings
}
