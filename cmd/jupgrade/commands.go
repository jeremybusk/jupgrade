package main

import (
	"flag"
	"fmt"
)

var commandNames = []string{"inspect", "discover", "resolve", "recipes", "policy", "plan", "upgrade", "apply", "verify", "rollback", "diff", "batch", "completion", "help"}

type commandConfig struct {
	flags                                                  *flag.FlagSet
	policy                                                 *policyFlags
	verify                                                 verifyFlags
	repo, out, plan, cache, model, manifest, build, format *string
	offline, resolve, dryRun, resume                       *bool
}

// The CLI and completion generators share one flag registry.
func configureCommand(command string) (commandConfig, error) {
	c := commandConfig{flags: flag.NewFlagSet(command, flag.ContinueOnError)}
	f := c.flags
	known := false
	for _, name := range commandNames {
		if command == name {
			known = true
		}
	}
	if command == "-h" || command == "--help" {
		known = true
	}
	if !known {
		return c, fmt.Errorf("unknown command %q", command)
	}
	switch command {
	case "plan", "upgrade", "policy", "inspect", "resolve", "batch":
		c.policy = bindPolicy(f)
	}
	switch command {
	case "plan", "upgrade", "verify", "discover", "batch":
		c.verify = bindVerify(f)
		c.offline = bindOffline(f)
	}
	switch command {
	case "plan", "upgrade", "inspect", "resolve", "discover":
		c.repo = f.String("repo", envDefault("JUPGRADE_REPO", "."), "repository root (JUPGRADE_REPO)")
	}
	switch command {
	case "plan", "upgrade", "resolve", "discover", "batch":
		c.out = f.String("out", envDefault("JUPGRADE_OUT", ""), "new output file/directory (JUPGRADE_OUT)")
	}
	switch command {
	case "apply", "rollback", "verify", "diff":
		c.plan = f.String("plan", envDefault("JUPGRADE_PLAN", ""), "saved plan (JUPGRADE_PLAN)")
	}
	switch command {
	case "plan", "upgrade", "inspect", "batch":
		c.resolve = f.Bool("resolve-maven", false, "capture cached Maven parents/BOMs (JUPGRADE_RESOLVE_MAVEN)")
	}
	switch command {
	case "inspect", "resolve":
		c.cache = f.String("maven-repo", envDefault("JUPGRADE_MAVEN_REPO", ""), "cached Maven repository")
	}
	switch command {
	case "inspect":
		c.model = f.String("model", envDefault("JUPGRADE_MODEL", ""), "saved effective build model")
	case "discover":
		c.build = f.String("build", envDefault("JUPGRADE_BUILD", ""), "select maven/gradle; default auto")
	case "upgrade":
		c.dryRun = f.Bool("dry-run", false, "preview without applying or verifying")
	case "batch":
		c.manifest = f.String("manifest", envDefault("JUPGRADE_BATCH_MANIFEST", ""), "batch repository manifest")
		c.dryRun = f.Bool("dry-run", false, "save plans without applying or verifying")
		c.resume = f.Bool("resume", false, "resume saved plans and captured policy")
	case "diff":
		c.format = f.String("format", "unified", "unified, summary or json")
	}
	return c, nil
}
