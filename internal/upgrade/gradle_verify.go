package upgrade

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

func runGradleBuild(ctx context.Context, p Plan, options VerifyOptions, env []string) error {
	if options.Profiles != "" {
		return fmt.Errorf("--profiles is Maven-specific; Gradle projects use their own configured build variants")
	}
	executable := options.GradleCommand
	if executable == "" {
		executable = "gradle"
		wrapper := filepath.Join(p.Root, "gradlew")
		if info, err := os.Lstat(wrapper); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0111 == 0 {
				return fmt.Errorf("gradlew must be a regular executable")
			}
			executable = wrapper
		}
	}
	if options.GradleUserHome != "" {
		filtered := make([]string, 0, len(env))
		for _, value := range env {
			if !strings.HasPrefix(value, "GRADLE_USER_HOME=") {
				filtered = append(filtered, value)
			}
		}
		home, err := filepath.Abs(options.GradleUserHome)
		if err != nil {
			return err
		}
		env = append(filtered, "GRADLE_USER_HOME="+home)
	}
	version := exec.CommandContext(ctx, executable, "--version", "--no-daemon")
	version.Dir, version.Env = p.Root, env
	output, err := version.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Gradle version check: %w\n%s", err, output)
	}
	clean := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(string(output), "")
	release := regexp.MustCompile(`(?m)^Gradle\s+([0-9]+(?:\.[0-9]+)+)`).FindStringSubmatch(clean)
	java := regexp.MustCompile(`(?m)(?:Launcher JVM|JVM):\s*([0-9]+)`).FindStringSubmatch(clean)
	if len(release) != 2 || !numericVersion(release[1]) {
		return fmt.Errorf("could not determine Gradle runtime version:\n%s", clean)
	}
	if compareVersions(release[1], p.Policy.Gradle) < 0 {
		return fmt.Errorf("Gradle %s is below saved target %s", release[1], p.Policy.Gradle)
	}
	if len(java) != 2 {
		return fmt.Errorf("could not determine Gradle runtime JDK:\n%s", clean)
	}
	major, _ := strconv.Atoi(java[1])
	if major != p.Policy.Java {
		return fmt.Errorf("Gradle runs on Java %d; verification requires Java %d (use --java-home)", major, p.Policy.Java)
	}
	// An installed executable override can be newer than the pinned wrapper.
	// Recheck framework compatibility against the actual tool that will run.
	inputs, err := readGradleInputs(p.Root)
	if err != nil {
		return err
	}
	checkPolicy := p.Policy
	checkPolicy.Gradle = release[1]
	checkPolicy.GradleSHA256 = ""
	delete(inputs, gradleWrapperProperties)
	_, _, findings, err := transformGradle(inputs, checkPolicy)
	if err != nil {
		return err
	}
	for _, finding := range findings {
		if finding.Severity == "blocker" {
			return fmt.Errorf("Gradle runtime compatibility: %s: %s", finding.Path, finding.Message)
		}
	}
	if options.Stdout != nil {
		_, _ = fmt.Fprint(options.Stdout, clean)
	}
	args := []string{"--no-daemon", "--console", "plain", "--stacktrace", "--rerun-tasks", "-Dorg.gradle.java.installations.auto-download=false"}
	if options.Offline {
		args = append(args, "--offline")
	}
	args = append(args, "clean", "check")
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir, command.Env, command.Stdout, command.Stderr = p.Root, env, options.Stdout, options.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("Gradle clean check failed: %w", err)
	}
	return nil
}
