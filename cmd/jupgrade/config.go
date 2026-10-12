package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"jupgrade/internal/upgrade"
)

// Scalar flags take their initial values from the environment. Version maps are
// merged separately so a CLI rule replaces only its matching environment rule.
func envDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

type versionRules []string

func (v *versionRules) String() string         { return strings.Join(*v, ",") }
func (v *versionRules) Set(value string) error { *v = append(*v, value); return nil }

type policyFlags struct {
	path, java, preset, boot, maven, mavenSHA *string
	dependencies, plugins, parents            versionRules
}

func bindPolicy(flags *flag.FlagSet) *policyFlags {
	p := &policyFlags{}
	p.path = flags.String("policy", envDefault("JUPGRADE_POLICY", ""), "complete JSON policy (JUPGRADE_POLICY)")
	p.java = flags.String("java", envDefault("JUPGRADE_JAVA", ""), "target Java: 17, 21 or 25; default 21 (JUPGRADE_JAVA)")
	p.preset = flags.String("preset", envDefault("JUPGRADE_PRESET", "build"), "pinned preset: build or common (JUPGRADE_PRESET)")
	p.boot = flags.String("spring-boot", envDefault("JUPGRADE_SPRING_BOOT", ""), "exact Spring Boot parent/BOM/plugin target; major migrations block (JUPGRADE_SPRING_BOOT)")
	p.maven = flags.String("maven", envDefault("JUPGRADE_MAVEN", ""), "exact Maven 3 wrapper target (JUPGRADE_MAVEN)")
	p.mavenSHA = flags.String("maven-sha256", envDefault("JUPGRADE_MAVEN_SHA256", ""), "target Maven archive SHA-256 (JUPGRADE_MAVEN_SHA256)")
	flags.Var(&p.dependencies, "dependency", "groupId:artifactId=version; repeatable (JUPGRADE_DEPENDENCIES: comma-separated)")
	flags.Var(&p.plugins, "plugin", "groupId:artifactId=version; repeatable (JUPGRADE_PLUGINS: comma-separated)")
	flags.Var(&p.parents, "parent", "groupId:artifactId=version; repeatable (JUPGRADE_PARENTS: comma-separated)")
	return p
}

func mergeRules(dest map[string]string, values []string) error {
	for _, value := range values {
		coordinate, version, ok := strings.Cut(strings.TrimSpace(value), "=")
		if !ok || strings.TrimSpace(coordinate) == "" || strings.TrimSpace(version) == "" {
			return fmt.Errorf("invalid version rule %q; use groupId:artifactId=version", value)
		}
		dest[strings.TrimSpace(coordinate)] = strings.TrimSpace(version)
	}
	return nil
}

func (f *policyFlags) resolve() (upgrade.Policy, error) {
	p, err := upgrade.ReadPolicy(*f.path)
	if err != nil {
		return p, err
	}
	java := p.Java
	if *f.java != "" {
		java, err = strconv.Atoi(*f.java)
		if err != nil {
			return p, fmt.Errorf("invalid --java/JUPGRADE_JAVA %q: use 17, 21 or 25", *f.java)
		}
	}
	defaults, err := upgrade.PolicyForJava(java)
	if err != nil {
		return p, err
	}
	if err := defaults.AddPreset(*f.preset); err != nil {
		return p, err
	}
	// A supplied policy is a complete document and has priority over presets.
	if *f.path == "" {
		p = defaults
	} else {
		p.Java = java
	}
	if *f.maven != "" {
		if p.Maven != *f.maven {
			p.MavenSHA256 = ""
		}
		p.Maven = *f.maven
	}
	if *f.mavenSHA != "" {
		p.MavenSHA256 = *f.mavenSHA
	}
	if p.Plugins == nil {
		p.Plugins = map[string]string{}
	}
	if p.Dependencies == nil {
		p.Dependencies = map[string]string{}
	}
	if p.Parents == nil {
		p.Parents = map[string]string{}
	}
	if *f.boot != "" {
		p.Parents["org.springframework.boot:spring-boot-starter-parent"] = *f.boot
		p.Dependencies["org.springframework.boot:spring-boot-dependencies"] = *f.boot
		p.Plugins["org.springframework.boot:spring-boot-maven-plugin"] = *f.boot
	}
	for _, rules := range []struct {
		env  string
		cli  []string
		dest map[string]string
	}{
		{"JUPGRADE_DEPENDENCIES", f.dependencies, p.Dependencies},
		{"JUPGRADE_PLUGINS", f.plugins, p.Plugins},
		{"JUPGRADE_PARENTS", f.parents, p.Parents},
	} {
		if value := os.Getenv(rules.env); value != "" {
			if err := mergeRules(rules.dest, strings.Split(value, ",")); err != nil {
				return p, fmt.Errorf("%s: %w", rules.env, err)
			}
		}
		if err := mergeRules(rules.dest, rules.cli); err != nil {
			return p, err
		}
	}
	return p, p.Validate()
}

type verifyFlags struct{ home, repo, profiles, timeout *string }

func bindVerify(flags *flag.FlagSet) verifyFlags {
	return verifyFlags{
		flags.String("java-home", envDefault("JUPGRADE_JAVA_HOME", ""), "target JDK installation directory (JUPGRADE_JAVA_HOME; otherwise JAVA_HOME/PATH)"),
		flags.String("maven-repo", envDefault("JUPGRADE_MAVEN_REPO", ""), "Maven local artifact cache directory (JUPGRADE_MAVEN_REPO)"),
		flags.String("profiles", envDefault("JUPGRADE_PROFILES", ""), "comma-separated Maven profiles (JUPGRADE_PROFILES)"),
		flags.String("timeout", envDefault("JUPGRADE_TIMEOUT", "20m"), "maximum build duration (JUPGRADE_TIMEOUT)"),
	}
}

// Bool flags retain --offline shorthand and allow --offline=false to override
// an environment default, including an invalid one.
func bindOffline(flags *flag.FlagSet) *bool {
	return flags.Bool("offline", false, "use cached Maven artifacts (JUPGRADE_OFFLINE)")
}
func (f verifyFlags) options(flags *flag.FlagSet, offline bool) (upgrade.VerifyOptions, error) {
	explicit := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "offline" {
			explicit = true
		}
	})
	if !explicit {
		if value := os.Getenv("JUPGRADE_OFFLINE"); value != "" {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return upgrade.VerifyOptions{}, fmt.Errorf("JUPGRADE_OFFLINE: %w", err)
			}
			offline = parsed
		}
	}
	timeout, err := time.ParseDuration(*f.timeout)
	if err != nil || timeout <= 0 {
		return upgrade.VerifyOptions{}, fmt.Errorf("--timeout/JUPGRADE_TIMEOUT must be a positive duration, got %q", *f.timeout)
	}
	return upgrade.VerifyOptions{JavaHome: *f.home, MavenRepo: *f.repo, Profiles: *f.profiles, Offline: offline, Timeout: timeout, Stdout: os.Stdout, Stderr: os.Stderr}, nil
}
