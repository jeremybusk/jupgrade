package upgrade

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type VerifyOptions struct {
	JavaHome  string
	MavenRepo string
	Profiles  string
	Offline   bool
	Timeout   time.Duration
	Stdout    io.Writer
	Stderr    io.Writer
}

// Verify executes the repository's build. Build failures and source changes are
// errors; a no-op transformation alone never constitutes migration success.
func Verify(ctx context.Context, p Plan, options VerifyOptions) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.HasBlockers() {
		return fmt.Errorf("plan contains blockers")
	}
	unlock, err := lock(p.Root)
	if err != nil {
		return err
	}
	defer unlock()
	if err := checkApplied(p); err != nil {
		return err
	}
	before, err := CreatePlan(p.Root, p.Policy)
	if err != nil {
		return err
	}
	if before.HasBlockers() || len(before.Files) != 0 {
		return fmt.Errorf("migration has not converged; create and inspect a new plan")
	}
	if options.Timeout <= 0 {
		options.Timeout = 20 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	executable, err := exec.LookPath("mvn")
	if err != nil {
		executable = "mvn"
	}
	wrapper := filepath.Join(p.Root, "mvnw")
	if info, err := os.Lstat(wrapper); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Maven wrapper may not be a symlink")
		}
		if info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("mvnw is not executable")
		}
		executable = wrapper
	}
	env := os.Environ()
	if options.JavaHome != "" {
		javaHome, err := filepath.Abs(options.JavaHome)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(javaHome, "bin", "java")); err != nil {
			return fmt.Errorf("invalid Java home: %w", err)
		}
		filtered := make([]string, 0, len(env))
		for _, value := range env {
			if !strings.HasPrefix(value, "JAVA_HOME=") && !strings.HasPrefix(value, "PATH=") {
				filtered = append(filtered, value)
			}
		}
		env = append(filtered, "JAVA_HOME="+javaHome, "PATH="+filepath.Join(javaHome, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	version := exec.CommandContext(ctx, executable, "--version")
	version.Dir, version.Env = p.Root, env
	output, err := version.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Maven version check: %w\n%s", err, output)
	}
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	clean := ansi.ReplaceAllString(string(output), "")
	match := regexp.MustCompile(`Java version:\s*([0-9]+)`).FindStringSubmatch(clean)
	if len(match) != 2 {
		return fmt.Errorf("could not determine Maven runtime JDK:\n%s", clean)
	}
	major, _ := strconv.Atoi(match[1])
	if major != p.Policy.Java {
		return fmt.Errorf("Maven runs on Java %d; verification requires Java %d (use --java-home)", major, p.Policy.Java)
	}
	if p.Policy.Maven != "" {
		maven := regexp.MustCompile(`Apache Maven\s+([0-9]+(?:\.[0-9]+)+)`).FindStringSubmatch(clean)
		if len(maven) != 2 || compareVersions(maven[1], p.Policy.Maven) < 0 || compareVersions(maven[1], "4.0.0") >= 0 {
			return fmt.Errorf("verification requires Maven 3 >= %s; runtime reports:\n%s", p.Policy.Maven, clean)
		}
	}
	if options.Stdout != nil {
		_, _ = fmt.Fprint(options.Stdout, clean)
	}
	args := []string{"-B", "-ntp"}
	if options.MavenRepo != "" {
		repo, err := filepath.Abs(options.MavenRepo)
		if err != nil {
			return err
		}
		args = append(args, "-Dmaven.repo.local="+repo)
	}
	if options.Offline {
		args = append(args, "-o")
	}
	if options.Profiles != "" {
		args = append(args, "-P"+options.Profiles)
	}
	// These command-line properties override common accidental skip settings.
	args = append(args, "-Dmaven.test.skip=false", "-DskipTests=false", "-DskipITs=false", "clean", "verify")
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir, command.Env, command.Stdout, command.Stderr = p.Root, env, options.Stdout, options.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("Maven clean verify failed: %w", err)
	}
	if err := checkApplied(p); err != nil {
		return fmt.Errorf("build modified migration inputs: %w", err)
	}
	after, err := CreatePlan(p.Root, p.Policy)
	if err != nil {
		return err
	}
	if after.HasBlockers() || len(after.Files) != 0 {
		return fmt.Errorf("migration is not idempotent after build")
	}
	if len(after.Inputs) != len(before.Inputs) {
		return fmt.Errorf("build changed source/input inventory; review generated files")
	}
	for path, expected := range before.Inputs {
		if after.Inputs[path] != expected {
			return fmt.Errorf("build changed source/input %s", path)
		}
	}
	return nil
}

func checkApplied(p Plan) error {
	expected := map[string]string{}
	for path, h := range p.Inputs {
		expected[path] = h
	}
	for _, f := range p.Files {
		expected[f.Path] = f.AfterSHA
	}
	for relative, h := range expected {
		path, err := safePath(p.Root, relative)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if hash(data) != h {
			return fmt.Errorf("%s does not match the applied plan", relative)
		}
	}
	return nil
}
