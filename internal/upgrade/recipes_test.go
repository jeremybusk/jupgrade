package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeRecipeCache(t *testing.T) string {
	t.Helper()
	cache := t.TempDir()
	for _, gav := range []string{rewriteMavenPlugin, "org.openrewrite.recipe:rewrite-migrate-java:3.42.1", "org.openrewrite.recipe:rewrite-spring:6.37.1"} {
		parts := strings.Split(gav, ":")
		path := filepath.Join(strings.ReplaceAll(parts[0], ".", "/"), parts[1], parts[2], parts[1]+"-"+parts[2]+".jar")
		writeTestFile(t, cache, path, "test adapter artifact")
	}
	return cache
}
func fakeRecipeCommand(t *testing.T, body string) {
	t.Helper()
	bin := t.TempDir()
	path := filepath.Join(bin, "mvn")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
func TestRecipePlanIsolatedConvergentReplayAndRollback(t *testing.T) {
	fakeRecipeCommand(t, `python3 - <<'PY'
from pathlib import Path
p=Path('src/main/java/example/App.java')
p.write_text(p.read_text().replace('String.format("Hello, %s", name)', '"Hello, %s".formatted(name)'))
PY`)
	root := t.TempDir()
	pom := `<project><properties><maven.compiler.release>17</maven.compiler.release></properties></project>`
	source := `package example; class App { String hello(String name) { return String.format("Hello, %s", name); } }`
	writeTestFile(t, root, "pom.xml", pom)
	writeTestFile(t, root, "src/main/java/example/App.java", source)
	writeTestFile(t, root, "README.md", "original context\n")
	policy := DefaultPolicy()
	policy.Recipes = []string{"string-format"}
	if p := testPlan(t, root, policy); !p.HasBlockers() {
		t.Fatal("unexecuted source recipe was accepted")
	}
	p, err := CreateRecipePlan(context.Background(), root, policy, VerifyOptions{MavenRepo: fakeRecipeCache(t), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.HasBlockers() || p.Policy.RecipeRun == nil || p.Schema != 4 {
		t.Fatal(p)
	}
	if readTestFile(t, root, "pom.xml") != pom || readTestFile(t, root, "src/main/java/example/App.java") != source {
		t.Fatal("worker wrote original inputs")
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := WritePlan(path, p); err != nil {
		t.Fatal(err)
	}
	p, err = ReadPlan(path)
	if err != nil {
		t.Fatal(err)
	}
	// Replay must not invoke Maven or require the artifact cache.
	fakeRecipeCommand(t, "exit 97")
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readTestFile(t, root, "src/main/java/example/App.java"), `.formatted(name)`) {
		t.Fatal("source result not applied")
	}
	if next := testPlan(t, root, p.Policy); len(next.Files) != 0 || next.HasBlockers() {
		t.Fatal(next)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, root, "pom.xml") != pom || readTestFile(t, root, "src/main/java/example/App.java") != source {
		t.Fatal("source rollback did not restore bytes")
	}
}

func TestRecipeReceiptRejectsTamperingAndChangedContext(t *testing.T) {
	fakeRecipeCommand(t, `sed -i 's/old/new/g' src/main/java/App.java`)
	root := t.TempDir()
	writeTestFile(t, root, "pom.xml", `<project/>`)
	writeTestFile(t, root, "src/main/java/App.java", `class App { String value="old"; }`)
	writeTestFile(t, root, "README.md", "context")
	policy := DefaultPolicy()
	policy.Recipes = []string{"string-format"}
	p, err := CreateRecipePlan(context.Background(), root, policy, VerifyOptions{MavenRepo: fakeRecipeCache(t)})
	if err != nil {
		t.Fatal(err)
	}
	for i := range p.Files {
		if strings.HasSuffix(p.Files[i].Path, ".java") {
			p.Files[i].After += "// unrelated"
			p.Files[i].AfterSHA = hash([]byte(p.Files[i].After))
		}
	}
	if err := Apply(p, false); err == nil {
		t.Fatal("tampered source output accepted")
	}
	p = testPlan(t, root, p.Policy)
	writeTestFile(t, root, "README.md", "changed context")
	if err := Apply(p, false); err == nil {
		t.Fatal("changed source context accepted")
	}
}

func TestRecipeWorkerRejectsFailureCreatesAndNonconvergence(t *testing.T) {
	for name, body := range map[string]string{
		"error":         "echo 'worker error'; exit 1",
		"parse":         "echo 'Failed to parse App.java'; exit 0",
		"create":        "touch new.java",
		"nonconvergent": "echo '// changes every run' >> src/main/java/App.java",
	} {
		t.Run(name, func(t *testing.T) {
			fakeRecipeCommand(t, body)
			root := t.TempDir()
			writeTestFile(t, root, "pom.xml", `<project/>`)
			writeTestFile(t, root, "src/main/java/App.java", `class App {}`)
			policy := DefaultPolicy()
			policy.Recipes = []string{"base64"}
			if _, err := CreateRecipePlan(context.Background(), root, policy, VerifyOptions{MavenRepo: fakeRecipeCache(t)}); err == nil {
				t.Fatal("bad worker output accepted")
			}
			if readTestFile(t, root, "pom.xml") != `<project/>` || readTestFile(t, root, "src/main/java/App.java") != `class App {}` {
				t.Fatal("failed worker changed repository")
			}
		})
	}
}

func TestBootMajorRequiresExecutedSourceRecipe(t *testing.T) {
	fakeRecipeCommand(t, `sed -i 's/2.7.18/3.5.16/g' pom.xml
sed -i 's/javax.servlet/jakarta.servlet/g' src/main/java/App.java`)
	root := t.TempDir()
	before := `<project><parent><groupId>org.springframework.boot</groupId><artifactId>spring-boot-starter-parent</artifactId><version>2.7.18</version><relativePath/></parent></project>`
	source := `import javax.servlet.http.HttpServlet; class App extends HttpServlet {}`
	writeTestFile(t, root, "pom.xml", before)
	writeTestFile(t, root, "src/main/java/App.java", source)
	policy := DefaultPolicy()
	policy.Parents["org.springframework.boot:spring-boot-starter-parent"] = "3.5.16"
	policy.Recipes = []string{"boot3.5"}
	if p := testPlan(t, root, policy); !p.HasBlockers() {
		t.Fatal("major Boot upgrade allowed before recipe execution")
	}
	p, err := CreateRecipePlan(context.Background(), root, policy, VerifyOptions{MavenRepo: fakeRecipeCache(t)})
	if err != nil {
		t.Fatal(err)
	}
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readTestFile(t, root, "src/main/java/App.java"), "jakarta.servlet") {
		t.Fatal("Boot API migration not applied")
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, root, "pom.xml") != before || readTestFile(t, root, "src/main/java/App.java") != source {
		t.Fatal("Boot migration rollback lost originals")
	}
}

func TestRecipeCannotDowngradeJavaOrExplicitPins(t *testing.T) {
	cases := []struct{ name, pom, body string }{
		{"java", `<project><properties><maven.compiler.release>25</maven.compiler.release></properties></project>`, `sed -i 's/>25</>21</g' pom.xml`},
		{"pin", `<project><dependencies><dependency><groupId>internal</groupId><artifactId>fixed</artifactId><version>3.0</version></dependency></dependencies></project>`, `sed -i 's/<version>3.0<\//<version>2.0<\//g' pom.xml`},
		{"removed", `<project><dependencies><dependency><groupId>internal</groupId><artifactId>fixed</artifactId><version>3.0</version></dependency></dependencies></project>`, `sed -i 's#<version>3.0</version>##g' pom.xml`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeRecipeCommand(t, c.body)
			root := t.TempDir()
			writeTestFile(t, root, "pom.xml", c.pom)
			policy := DefaultPolicy()
			policy.Recipes = []string{"base64"}
			if _, err := CreateRecipePlan(context.Background(), root, policy, VerifyOptions{MavenRepo: fakeRecipeCache(t)}); err == nil {
				t.Fatal("unsafe source recipe accepted")
			}
			if readTestFile(t, root, "pom.xml") != c.pom {
				t.Fatal("unsafe recipe changed original")
			}
		})
	}
}
