package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"jupgrade/internal/upgrade"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "jupgrade:", err)
		os.Exit(1)
	}
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func parseFlags(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	return nil
}

func verifyPlan(p upgrade.Plan, options upgrade.VerifyOptions) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := upgrade.Verify(ctx, p, options); err != nil {
		return err
	}
	fmt.Printf("Java %d build passed; migration produced zero further edits. Review remaining findings and run application smoke tests.\n", p.Policy.Java)
	return nil
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: jupgrade <inspect|resolve|recipes|plan|upgrade|apply|verify|rollback|policy> [options]")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "inspect":
		repo := flags.String("repo", envDefault("JUPGRADE_REPO", "."), "repository root (JUPGRADE_REPO)")
		cache := flags.String("maven-repo", envDefault("JUPGRADE_MAVEN_REPO", ""), "cached parent/BOM repository")
		resolve := flags.Bool("resolve-maven", false, "capture cached Maven models for inspection (JUPGRADE_RESOLVE_MAVEN)")
		config := bindPolicy(flags)
		if err := parseFlags(flags, args[1:]); err != nil {
			return err
		}
		p, err := config.resolve()
		if err != nil {
			return err
		}
		enabled, err := environmentBool(flags, "resolve-maven", "JUPGRADE_RESOLVE_MAVEN", *resolve)
		if err != nil {
			return err
		}
		if enabled {
			p, err = upgrade.ResolveMaven(*repo, p, *cache)
			if err != nil {
				return err
			}
		}
		report, err := upgrade.InspectWithPolicy(*repo, p)
		if err != nil {
			return err
		}
		return printJSON(report)
	case "recipes":
		if err := parseFlags(flags, args[1:]); err != nil {
			return err
		}
		return printJSON(upgrade.RecipeCatalog())
	case "resolve":
		repo := flags.String("repo", envDefault("JUPGRADE_REPO", "."), "Maven repository root (JUPGRADE_REPO)")
		cache := flags.String("maven-repo", envDefault("JUPGRADE_MAVEN_REPO", ""), "cached parent/BOM repository (JUPGRADE_MAVEN_REPO)")
		output := flags.String("out", envDefault("JUPGRADE_OUT", ""), "new resolved policy file; omit to print JSON")
		config := bindPolicy(flags)
		if err := parseFlags(flags, args[1:]); err != nil {
			return err
		}
		p, err := config.resolve()
		if err != nil {
			return err
		}
		p, err = upgrade.ResolveMaven(*repo, p, *cache)
		if err != nil {
			return err
		}
		if *output == "" {
			return printJSON(p)
		}
		return upgrade.WritePolicy(*output, p)
	case "policy":
		config := bindPolicy(flags)
		if err := parseFlags(flags, args[1:]); err != nil {
			return err
		}
		p, err := config.resolve()
		if err != nil {
			return err
		}
		return printJSON(p)
	case "plan", "upgrade":
		repo := flags.String("repo", envDefault("JUPGRADE_REPO", "."), "repository root (JUPGRADE_REPO)")
		config := bindPolicy(flags)
		output := flags.String("out", envDefault("JUPGRADE_OUT", ""), "new plan file; plan prints JSON if omitted (JUPGRADE_OUT)")
		resolve := flags.Bool("resolve-maven", false, "capture cached parent/BOM POMs in the plan (JUPGRADE_RESOLVE_MAVEN)")
		vf := bindVerify(flags)
		offline := bindOffline(flags)
		modelCache := vf.repo
		dryRun := false
		if args[0] == "upgrade" {
			flags.BoolVar(&dryRun, "dry-run", false, "save/print the plan without applying or building")
		}
		if err := parseFlags(flags, args[1:]); err != nil {
			return err
		}
		if args[0] == "upgrade" && !dryRun && *output == "" {
			return errors.New("upgrade requires --out PATH to save a recoverable plan; use --dry-run to preview")
		}
		options, err := vf.options(flags, *offline)
		if err != nil {
			return err
		}
		p, err := config.resolve()
		if err != nil {
			return err
		}
		resolveModels, err := environmentBool(flags, "resolve-maven", "JUPGRADE_RESOLVE_MAVEN", *resolve)
		if err != nil {
			return err
		}
		if resolveModels {
			p, err = upgrade.ResolveMaven(*repo, p, *modelCache)
			if err != nil {
				return err
			}
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		plan, err := upgrade.CreateRecipePlan(ctx, *repo, p, options)
		if err != nil {
			return err
		}
		if *output == "" {
			err = printJSON(plan)
		} else {
			err = upgrade.WritePlan(*output, plan)
		}
		if err != nil {
			return err
		}
		summary(plan)
		if plan.HasBlockers() {
			return errors.New("plan saved with blockers; resolve them before applying")
		}
		if args[0] == "plan" || dryRun {
			return nil
		}
		fmt.Fprintf(os.Stderr, "Saved recovery plan: %s\n", *output)
		if err := upgrade.Apply(plan, false); err != nil {
			return err
		}
		if err := verifyPlan(plan, options); err != nil {
			return fmt.Errorf("%w; changes remain applied for inspection; rollback with jupgrade rollback --plan %q", err, *output)
		}
		return nil
	case "apply", "rollback", "verify":
		path := flags.String("plan", envDefault("JUPGRADE_PLAN", ""), "saved migration plan (JUPGRADE_PLAN)")
		var vf verifyFlags
		var offline *bool
		if args[0] == "verify" {
			vf = bindVerify(flags)
			offline = bindOffline(flags)
		}
		if err := parseFlags(flags, args[1:]); err != nil {
			return err
		}
		if *path == "" {
			return errors.New("provide --plan PATH (or JUPGRADE_PLAN)")
		}
		p, err := upgrade.ReadPlan(*path)
		if err != nil {
			return err
		}
		if args[0] == "verify" {
			options, err := vf.options(flags, *offline)
			if err != nil {
				return err
			}
			return verifyPlan(p, options)
		}
		if err := upgrade.Apply(p, args[0] == "rollback"); err != nil {
			return err
		}
		fmt.Printf("%s complete (%d planned files)\n", args[0], len(p.Files))
		return nil
	case "help", "-h", "--help":
		fmt.Println(`jupgrade inspect [--repo DIR]
jupgrade resolve [--repo DIR] [--out POLICY.json] [--maven-repo CACHE]
jupgrade plan [--repo DIR] [--out PLAN.json] [--java 17|21|25] [--preset build|common]
jupgrade upgrade --out PLAN.json [policy/build options] [--dry-run]
jupgrade apply --plan PLAN.json
jupgrade verify --plan PLAN.json [--java-home DIR] [--profiles LIST] [--offline]
jupgrade rollback --plan PLAN.json
jupgrade policy [--java 17|21|25] [--preset build|common]
jupgrade recipes

Policy options: --policy FILE, --dependency G:A=V, --plugin G:A=V,
--parent G:A=V, --spring-boot VERSION, --maven VERSION [--maven-sha256 HEX],
--gradle VERSION [--gradle-sha256 HEX], --gradle-plugin ID=V, --recipe ALIAS.
Version rules are repeatable. Use --resolve-maven to capture cached parent/BOM models.
Defaults: Java 21 and pinned build plugins; existing newer versions are retained.
Use jupgrade COMMAND --help for options and their JUPGRADE_* environment variables.
Native planning is offline; the resolved policy is saved in the plan.
Gradle supports literal Groovy/Kotlin DSL, toolchains and version catalogs.
Source recipes execute pinned OpenRewrite workers on isolated copies.
Use jupgrade recipes to list supported Java and Spring Boot recipes.`)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func summary(p upgrade.Plan) {
	fmt.Fprintf(os.Stderr, "%d files to change, %d actions, %d findings (target Java %d)\n", len(p.Files), len(p.Changes), len(p.Findings), p.Policy.Java)
	for _, c := range p.Changes {
		fmt.Fprintf(os.Stderr, "  %s: %s\n", c.Path, c.Reason)
	}
	for _, f := range p.Findings {
		fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", f.Severity, f.Path, f.Message)
	}
}
