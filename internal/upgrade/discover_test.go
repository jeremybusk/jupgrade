package upgrade

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMavenEffectiveDiscoveryCapturesProfilesSettingsAndRejectsStaleContext(t *testing.T) {
	fakeRecipeCommand(t, `if [ "$1" = "--version" ]; then echo 'Apache Maven 3.9.11'; echo 'Java version: 21.0.1'; exit 0; fi
python3 - "$@" <<'PY'
import sys
from pathlib import Path
args=sys.argv[1:]
assert 'org.apache.maven.plugins:maven-help-plugin:3.5.1:effective-pom' in args
assert '-Pprod' in args and '--settings' in args and '-o' in args
output=next(x.split('=',1)[1] for x in args if x.startswith('-Doutput='))
Path(output).write_text('<projects><project><groupId>example</groupId><artifactId>app</artifactId><version>1.0</version><dependencies><dependency><groupId>junit</groupId><artifactId>junit</artifactId><version>4.13.2</version></dependency></dependencies><profiles><profile><id>inactive</id><dependencies><dependency><groupId>internal</groupId><artifactId>inactive</artifactId><version>1.0</version></dependency></dependencies></profile></profiles></project><project><groupId>example</groupId><artifactId>child</artifactId><version>1.0</version></project></projects>')
PY`)
	root := t.TempDir()
	before := `<project><groupId>example</groupId><artifactId>app</artifactId><version>1.0</version><modules><module>${child.path}</module></modules></project>`
	writeTestFile(t, root, "pom.xml", before)
	writeTestFile(t, root, "child/pom.xml", `<project><groupId>example</groupId><artifactId>child</artifactId><version>1.0</version></project>`)
	settings := filepath.Join(t.TempDir(), "settings.xml")
	if err := os.WriteFile(settings, []byte("<settings><password>do-not-expose</password></settings>"), 0600); err != nil {
		t.Fatal(err)
	}
	model, err := DiscoverBuildModel(context.Background(), root, VerifyOptions{Profiles: "prod", MavenSettings: settings, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Modules) != 2 || model.Profiles != "prod" || model.Java != 21 || model.Settings[settings] == "" {
		t.Fatal(model)
	}
	if readTestFile(t, root, "pom.xml") != before {
		t.Fatal("discovery wrote original")
	}
	data, _ := json.Marshal(model)
	if strings.Contains(string(data), "do-not-expose") {
		t.Fatal("model exposed settings content")
	}
	path := filepath.Join(t.TempDir(), "model.json")
	if err := WriteBuildModel(path, model); err != nil {
		t.Fatal(err)
	}
	if err := WriteBuildModel(path, model); err == nil {
		t.Fatal("overwrote model output")
	}
	model, err = ReadBuildModel(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := InspectBuildModel(root, model)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Artifacts) != 1 || report.Artifacts[0].Resolved != "4.13.2" {
		t.Fatal("included inactive profile or missed effective version", report)
	}
	tampered := model
	tampered.ToolVersion = "3.8.1"
	if err := tampered.Validate(); err == nil {
		t.Fatal("tampered model accepted")
	}
	writeTestFile(t, root, "README.md", "changed context")
	if _, err := InspectBuildModel(root, model); err == nil {
		t.Fatal("stale model accepted")
	}
}

func TestGradleEffectiveDiscoveryHandlesDynamicModulesAndVersions(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "settings.gradle.kts", `val module = "app"; include(module)`)
	writeTestFile(t, root, "build.gradle.kts", `plugins { java }`)
	writeTestFile(t, root, "app/build.gradle.kts", `plugins { java }; dependencies { testImplementation("junit:junit:$version") }`)
	executable := filepath.Join(t.TempDir(), "gradle")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo 'Gradle 9.1.0'; echo 'Launcher JVM: 25.0.1'; exit 0; fi
python3 - "$@" <<'PY'
import json,sys
from pathlib import Path
args=sys.argv[1:]
assert '--init-script' in args and 'jupgradeDiscoverModel' in args
output=next(x.split('=',1)[1] for x in args if x.startswith('-Djupgrade.model.output='))
Path(output).write_text(json.dumps([{'path':'.','build_file':'build.gradle.kts','coordinate':'example:root:1.0','artifacts':[]},{'path':'app','build_file':'app/build.gradle.kts','coordinate':'example:app:1.0','artifacts':[{'path':'app/build.gradle.kts','kind':'configuration:testRuntimeClasspath','coordinate':'junit:junit','resolved_version':'4.13.2'}]}]))
PY
`
	if err := os.WriteFile(executable, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := CreatePlan(root, DefaultPolicy()); err == nil {
		t.Fatal("native discovery guessed a dynamic module")
	}
	model, err := DiscoverBuildModel(context.Background(), root, VerifyOptions{GradleCommand: executable})
	if err != nil {
		t.Fatal(err)
	}
	report, err := InspectBuildModel(root, model)
	if err != nil {
		t.Fatal(err)
	}
	if model.Java != 25 || len(model.Modules) != 2 || len(report.Artifacts) != 1 {
		t.Fatal(model, report)
	}
}

func TestEffectiveDiscoveryRejectsWorkerChangesAndNonconvergence(t *testing.T) {
	for _, body := range []string{`echo '<project><groupId>example</groupId><artifactId>app</artifactId><version>1.0</version></project>' > "$output"; echo changed >> pom.xml`, `echo "<project><groupId>example</groupId><artifactId>app</artifactId><version>$(date +%s%N)</version></project>" > "$output"`} {
		fakeRecipeCommand(t, `if [ "$1" = "--version" ]; then echo 'Apache Maven 3.9.11'; echo 'Java version: 21'; exit 0; fi
for arg in "$@"; do case "$arg" in -Doutput=*) output="${arg#-Doutput=}";; esac; done
`+body)
		root := t.TempDir()
		before := `<project><groupId>example</groupId><artifactId>app</artifactId><version>1.0</version></project>`
		writeTestFile(t, root, "pom.xml", before)
		if _, err := DiscoverBuildModel(context.Background(), root, VerifyOptions{}); err == nil {
			t.Fatal("unsafe model worker accepted")
		}
		if readTestFile(t, root, "pom.xml") != before {
			t.Fatal("model worker changed original")
		}
	}
}

func TestMavenEffectiveModelDiscoveryIntegration(t *testing.T) {
	if os.Getenv("JUPGRADE_MODEL_INTEGRATION") != "1" {
		t.Skip("set JUPGRADE_MODEL_INTEGRATION=1 to execute Maven effective-model discovery")
	}
	root := t.TempDir()
	copyIntegrationFixture(t, "maven-bom", root)
	model, err := DiscoverBuildModel(context.Background(), root, VerifyOptions{MavenRepo: os.Getenv("JUPGRADE_MAVEN_REPO"), Offline: os.Getenv("JUPGRADE_OFFLINE") == "1"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := InspectBuildModel(root, model)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, artifact := range report.Artifacts {
		if artifact.Coordinate == "org.junit.jupiter:junit-jupiter" && artifact.Resolved == "5.11.4" {
			found = true
		}
	}
	if !found {
		t.Fatal("effective BOM management was not captured", report)
	}
}
