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
	command, err := configureCommand(args[0])
	if err != nil {
		return err
	}
	flags := command.flags
	switch args[0] {
	case "diff":
		if err := parseFlags(flags, args[1:]); err != nil {
			return err
		}
		if *command.plan == "" {
			return errors.New("provide --plan PATH")
		}
		p, err := upgrade.ReadPlanForReview(*command.plan)
		if err != nil {
			return err
		}
		switch *command.format {
		case "unified":
			return upgrade.WritePlanDiff(os.Stdout, p)
		case "json":
			return printJSON(p)
		case "summary":
			fmt.Printf("%s: Java %d, %d files\n", p.Root, p.Policy.Java, len(p.Files))
			for _, file := range p.Files {
				action := file.Action
				if action == "" {
					action = "edit"
				}
				fmt.Printf("  %s %s\n", action, file.Path)
			}
			return nil
		default:
			return errors.New("diff --format must be unified, summary or json")
		}
	case "batch":
		if err := parseFlags(flags, args[1:]); err != nil {
			return err
		}
		if *command.manifest == "" || *command.out == "" {
			return errors.New("batch requires --manifest FILE --out NEW_DIRECTORY")
		}
		manifest, err := upgrade.ReadBatchManifest(*command.manifest)
		if err != nil {
			return err
		}
		options, err := command.verify.options(flags, *command.offline)
		if err != nil {
			return err
		}
		var policy upgrade.Policy
		resolve := false
		if !*command.resume {
			policy, err = command.policy.resolve()
			if err != nil {
				return err
			}
			resolve, err = environmentBool(flags, "resolve-maven", "JUPGRADE_RESOLVE_MAVEN", *command.resolve)
			if err != nil {
				return err
			}
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		report, workErr := upgrade.RunBatch(ctx, manifest, *command.out, upgrade.BatchOptions{Policy: policy, Verify: options, DryRun: *command.dryRun, Resume: *command.resume, ResolveMaven: resolve})
		if report.Schema != 0 {
			if err := printJSON(report); err != nil {
				return err
			}
		}
		return workErr
	case "completion":
		if len(args) != 2 {
			return errors.New("usage: jupgrade completion <bash|zsh|fish>")
		}
		if args[1] == "--help" || args[1] == "-h" {
			fmt.Println("jupgrade completion <bash|zsh|fish>")
			return nil
		}
		script, err := completionScript(args[1])
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(os.Stdout, script)
		return err
	case "discover":
		repo := command.repo
		output := command.out
		vf := command.verify
		offline := command.offline
		if err := parseFlags(flags, args[1:]); err != nil {
			return err
		}
		options, err := vf.options(flags, *offline)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		options.Build = *command.build
		model, err := upgrade.DiscoverBuildModel(ctx, *repo, options)
		if err != nil {
			return err
		}
		if *output == "" {
			return printJSON(model)
		}
		return upgrade.WriteBuildModel(*output, model)
	case "inspect":
		modelPath := command.model
		repo := command.repo
		cache := command.cache
		resolve := command.resolve
		config := command.policy
		if err := parseFlags(flags, args[1:]); err != nil {
			return err
		}
		if *modelPath != "" {
			model, err := upgrade.ReadBuildModel(*modelPath)
			if err != nil {
				return err
			}
			report, err := upgrade.InspectBuildModel(*repo, model)
			if err != nil {
				return err
			}
			return printJSON(report)
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
		repo := command.repo
		cache := command.cache
		output := command.out
		config := command.policy
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
		config := command.policy
		if err := parseFlags(flags, args[1:]); err != nil {
			return err
		}
		p, err := config.resolve()
		if err != nil {
			return err
		}
		return printJSON(p)
	case "plan", "upgrade":
		repo := command.repo
		config := command.policy
		output := command.out
		resolve := command.resolve
		vf := command.verify
		offline := command.offline
		modelCache := vf.repo
		dryRun := false
		if err := parseFlags(flags, args[1:]); err != nil {
			return err
		}
		if args[0] == "upgrade" {
			dryRun = *command.dryRun
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
		path := command.plan
		var vf verifyFlags
		var offline *bool
		if args[0] == "verify" {
			vf = command.verify
			offline = command.offline
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
		if args[0] == "help" && len(args) == 2 {
			return run([]string{args[1], "--help"})
		}
		fmt.Println(`jupgrade inspect [--repo DIR]
jupgrade discover [--repo DIR] [--out MODEL.json] [build options]
jupgrade resolve [--repo DIR] [--out POLICY.json] [--maven-repo CACHE]
jupgrade plan [--repo DIR] [--out PLAN.json] [--java 17|21|25] [--preset build|common]
jupgrade upgrade --out PLAN.json [policy/build options] [--dry-run]
jupgrade apply --plan PLAN.json
jupgrade verify --plan PLAN.json [--java-home DIR] [--profiles LIST] [--offline]
jupgrade rollback --plan PLAN.json
jupgrade policy [--java 17|21|25] [--preset build|common]
jupgrade recipes
jupgrade diff --plan PLAN.json [--format unified|summary|json]
jupgrade batch --manifest REPOSITORIES.json --out NEW_DIRECTORY [--dry-run|--resume]
jupgrade completion <bash|zsh|fish>

Policy options: --build maven|gradle|auto, --policy FILE, --dependency G:A=V, --plugin G:A=V,
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
