package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
)

// requireMaven identifies unsupported/mixed roots before any transformation.
func requireMaven(root string) error {
	has := func(name string) bool {
		_, err := os.Lstat(filepath.Join(root, name))
		return err == nil
	}
	gradle := has("build.gradle") || has("build.gradle.kts") || has("settings.gradle") || has("settings.gradle.kts")
	if gradle {
		return fmt.Errorf("Gradle build detected at %s; Gradle migration is not implemented yet; select a Maven-only root or use a Gradle migration recipe", root)
	}
	if !has("pom.xml") {
		return fmt.Errorf("no supported build at %s: expected pom.xml (Gradle migration is not implemented yet)", root)
	}
	return nil
}
