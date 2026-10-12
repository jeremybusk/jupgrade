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
	"time"

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

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: jupgrade <inspect|plan|apply|verify|rollback|policy> [options]")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "inspect":
		repo := flags.String("repo", ".", "Maven repository root")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected positional arguments")
		}
		report, err := upgrade.Inspect(*repo)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	case "policy":
		if len(args) != 1 {
			return errors.New("usage: jupgrade policy")
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(upgrade.DefaultPolicy())
	case "plan":
		repo := flags.String("repo", ".", "Maven repository root")
		policy := flags.String("policy", "", "JSON policy (defaults to curated Java 21 build plugins)")
		output := flags.String("out", "", "new plan file; omit to print JSON to stdout")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected positional arguments")
		}
		p, err := upgrade.ReadPolicy(*policy)
		if err != nil {
			return err
		}
		plan, err := upgrade.CreatePlan(*repo, p)
		if err != nil {
			return err
		}
		if *output == "" {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(plan); err != nil {
				return err
			}
		} else if err := upgrade.WritePlan(*output, plan); err != nil {
			return err
		}
		summary(plan)
		if plan.HasBlockers() {
			return errors.New("plan saved with blockers; resolve them before applying")
		}
		return nil
	case "apply", "rollback", "verify":
		path := flags.String("plan", "", "saved migration plan")
		var home, profiles, mavenRepo *string
		var offline *bool
		var timeout *time.Duration
		if args[0] == "verify" {
			home = flags.String("java-home", "", "Java 21 installation directory")
			mavenRepo = flags.String("maven-repo", "", "Maven local artifact cache directory")
			profiles = flags.String("profiles", "", "comma-separated Maven profiles to verify")
			offline = flags.Bool("offline", false, "use only cached Maven artifacts")
			timeout = flags.Duration("timeout", 20*time.Minute, "maximum build duration")
		}
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *path == "" || flags.NArg() != 0 {
			return errors.New("provide --plan PATH; no positional arguments")
		}
		p, err := upgrade.ReadPlan(*path)
		if err != nil {
			return err
		}
		if args[0] == "verify" {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			err = upgrade.Verify(ctx, p, upgrade.VerifyOptions{JavaHome: *home, MavenRepo: *mavenRepo, Profiles: *profiles, Offline: *offline, Timeout: *timeout, Stdout: os.Stdout, Stderr: os.Stderr})
		} else {
			err = upgrade.Apply(p, args[0] == "rollback")
		}
		if err != nil {
			return err
		}
		if args[0] == "verify" {
			fmt.Println("Java 21 build passed; migration produced zero further edits. Review remaining findings and run application smoke tests.")
		} else {
			fmt.Printf("%s complete (%d planned files)\n", args[0], len(p.Files))
		}
		return nil
	case "help", "-h", "--help":
		fmt.Println("jupgrade inspect --repo DIR\njupgrade plan --repo DIR --out PLAN.json [--policy POLICY.json]\njupgrade apply --plan PLAN.json\njupgrade verify --plan PLAN.json [--java-home DIR] [--profiles LIST] [--offline]\njupgrade rollback --plan PLAN.json\njupgrade policy")
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func summary(p upgrade.Plan) {
	fmt.Fprintf(os.Stderr, "%d POMs to change, %d actions, %d findings\n", len(p.Files), len(p.Changes), len(p.Findings))
	for _, c := range p.Changes {
		fmt.Fprintf(os.Stderr, "  %s: %s\n", c.Path, c.Reason)
	}
	for _, f := range p.Findings {
		fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", f.Severity, f.Path, f.Message)
	}
}
