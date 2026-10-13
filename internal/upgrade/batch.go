package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type BatchEntry struct {
	Name string `json:"name"`
	Repo string `json:"repo"`
}
type BatchManifest struct {
	Schema       int          `json:"schema"`
	Repositories []BatchEntry `json:"repositories"`
}
type BatchResult struct {
	Name         string `json:"name"`
	Repo         string `json:"repo"`
	Plan         string `json:"plan"`
	Log          string `json:"log"`
	Status       string `json:"status"`
	Stage        string `json:"stage"`
	Files        int    `json:"files"`
	Java         int    `json:"java,omitempty"`
	Build        string `json:"build,omitempty"`
	Error        string `json:"error,omitempty"`
	Verification string `json:"verification_context,omitempty"`
}
type BatchReport struct {
	Schema       int           `json:"schema"`
	ManifestSHA  string        `json:"manifest_sha256"`
	Policy       Policy        `json:"policy"`
	ResolveMaven bool          `json:"resolve_maven"`
	DryRun       bool          `json:"dry_run"`
	Results      []BatchResult `json:"results"`
}
type BatchOptions struct {
	Policy       Policy
	Verify       VerifyOptions
	DryRun       bool
	Resume       bool
	ResolveMaven bool
}

func ReadBatchManifest(path string) (BatchManifest, error) {
	var manifest BatchManifest
	f, err := os.Open(path)
	if err != nil {
		return manifest, err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&manifest); err != nil {
		return manifest, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return manifest, fmt.Errorf("batch manifest must contain one JSON object")
	}
	if manifest.Schema != 1 || len(manifest.Repositories) == 0 {
		return manifest, fmt.Errorf("batch requires schema 1 and repositories")
	}
	names, roots := map[string]bool{}, map[string]bool{}
	for i, entry := range manifest.Repositories {
		if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`).MatchString(entry.Name) || names[entry.Name] || entry.Repo == "" {
			return manifest, fmt.Errorf("invalid/duplicate batch entry %q", entry.Name)
		}
		names[entry.Name] = true
		root := entry.Repo
		if !filepath.IsAbs(root) {
			root = filepath.Join(filepath.Dir(path), root)
		}
		root, err := canonicalRoot(root)
		if err != nil {
			return manifest, err
		}
		if roots[root] {
			return manifest, fmt.Errorf("duplicate batch repository %s", root)
		}
		roots[root] = true
		manifest.Repositories[i].Repo = root
	}
	return manifest, nil
}

func verificationContext(options VerifyOptions) string {
	data, _ := json.Marshal(struct {
		JavaHome, MavenRepo, MavenSettings, Profiles, GradleCommand, GradleUserHome string
		Offline                                                                     bool
	}{options.JavaHome, options.MavenRepo, options.MavenSettings, options.Profiles, options.GradleCommand, options.GradleUserHome, options.Offline})
	return hash(data)
}

// RunBatch always saves each recovery plan before applying it. Resume reuses
// those plans regardless of later policy/environment changes and refuses stale
// repositories. A failure in one repository does not hide other results.
func RunBatch(ctx context.Context, manifest BatchManifest, output string, options BatchOptions) (BatchReport, error) {
	var report BatchReport
	output, err := filepath.Abs(output)
	if err != nil {
		return report, err
	}
	if options.Resume {
		output, err = canonicalRoot(output)
	} else {
		var parent string
		parent, err = canonicalRoot(filepath.Dir(output))
		if err == nil {
			output = filepath.Join(parent, filepath.Base(output))
		}
	}
	if err != nil {
		return report, err
	}
	if manifest.Schema != 1 || len(manifest.Repositories) == 0 {
		return report, fmt.Errorf("invalid batch manifest")
	}
	names, roots := map[string]bool{}, map[string]bool{}
	for _, entry := range manifest.Repositories {
		root, err := canonicalRoot(entry.Repo)
		if err != nil {
			return report, err
		}
		if root != entry.Repo || names[entry.Name] || roots[root] || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`).MatchString(entry.Name) {
			return report, fmt.Errorf("invalid/duplicate batch entry")
		}
		names[entry.Name], roots[root] = true, true
	}
	for _, entry := range manifest.Repositories {
		relative, err := filepath.Rel(entry.Repo, output)
		if err != nil {
			return report, err
		}
		if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			ignored := false
			for _, part := range strings.Split(relative, string(filepath.Separator)) {
				if part == ".jupgrade" {
					ignored = true
				}
			}
			if !ignored {
				return report, fmt.Errorf("batch output must be outside repositories or under .jupgrade")
			}
		}
	}
	data, _ := json.Marshal(manifest)
	manifestSHA := hash(data)
	reportPath := filepath.Join(output, "report.json")
	if options.Resume {
		data, err := os.ReadFile(reportPath)
		if err != nil {
			return report, err
		}
		if err := json.Unmarshal(data, &report); err != nil {
			return report, err
		}
		if report.Schema != 1 || report.ManifestSHA != manifestSHA || len(report.Results) != len(manifest.Repositories) {
			return report, fmt.Errorf("batch manifest changed; use a new output directory")
		}
		if err := report.Policy.Validate(); err != nil {
			return report, err
		}
		options.Policy, options.ResolveMaven = report.Policy, report.ResolveMaven
	} else {
		if err := options.Policy.Validate(); err != nil {
			return report, err
		}
		if err := os.Mkdir(output, 0700); err != nil {
			return report, fmt.Errorf("create new batch directory (use --resume for existing work): %w", err)
		}
		report = BatchReport{Schema: 1, ManifestSHA: manifestSHA, Policy: options.Policy, ResolveMaven: options.ResolveMaven, DryRun: options.DryRun, Results: []BatchResult{}}
		for _, entry := range manifest.Repositories {
			report.Results = append(report.Results, BatchResult{Name: entry.Name, Repo: entry.Repo, Plan: filepath.Join(output, entry.Name+".plan.json"), Log: filepath.Join(output, entry.Name+".log"), Status: "pending", Stage: "plan"})
		}
	}
	for i, entry := range manifest.Repositories {
		row := report.Results[i]
		if row.Name != entry.Name || row.Repo != entry.Repo || row.Plan != filepath.Join(output, entry.Name+".plan.json") || row.Log != filepath.Join(output, entry.Name+".log") {
			return report, fmt.Errorf("invalid batch report identity")
		}
	}
	unlock, err := lock(output)
	if err != nil {
		return report, err
	}
	defer unlock()
	save := func() error {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		if _, err := os.Stat(reportPath); os.IsNotExist(err) {
			return writeNewFile(reportPath, data)
		}
		return atomicWrite(reportPath, data)
	}
	if err := save(); err != nil {
		return report, err
	}
	verification := verificationContext(options.Verify)
	failures := 0
	for i := range report.Results {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		row := &report.Results[i]
		row.Error = ""
		log, err := os.OpenFile(row.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return report, err
		}
		verify := options.Verify
		verify.Stdout, verify.Stderr = log, log
		work := func() error {
			var plan Plan
			if _, err := os.Stat(row.Plan); err == nil {
				plan, err = ReadPlan(row.Plan)
				if err != nil {
					return err
				}
				if plan.Root != row.Repo {
					return fmt.Errorf("recovery plan belongs to a different repository")
				}
			} else if !os.IsNotExist(err) {
				return err
			} else {
				row.Stage = "plan"
				policy := options.Policy
				if options.Resume && policy.Java == 0 {
					return fmt.Errorf("missing recovery plan; supply policy options for unplanned repositories")
				}
				if options.ResolveMaven {
					policy, err = ResolveMaven(row.Repo, policy, verify.MavenRepo)
					if err != nil {
						return err
					}
				}
				plan, err = CreateRecipePlan(ctx, row.Repo, policy, verify)
				if err != nil {
					return err
				}
				if err := WritePlan(row.Plan, plan); err != nil {
					return err
				}
			}
			row.Java, row.Build, row.Files = plan.Policy.Java, plan.Build, len(plan.Files)
			if plan.HasBlockers() {
				row.Status = "blocked"
				return fmt.Errorf("plan has blockers; inspect %s", row.Plan)
			}
			if options.DryRun {
				row.Status = "planned"
				row.Stage = "plan"
				return nil
			}
			if row.Status == "verified" && row.Verification == verification {
				if err := Apply(plan, false); err != nil {
					return err
				}
				if err := checkApplied(plan); err != nil {
					return err
				}
				current, err := CreatePlan(row.Repo, plan.Policy)
				if err != nil {
					return err
				}
				if current.HasBlockers() || len(current.Files) != 0 {
					return fmt.Errorf("verified migration no longer converges")
				}
				return nil
			}
			row.Status = "running"
			row.Stage = "apply"
			if err := save(); err != nil {
				return err
			}
			if err := Apply(plan, false); err != nil {
				return err
			}
			row.Stage = "verify"
			if err := save(); err != nil {
				return err
			}
			if err := Verify(ctx, plan, verify); err != nil {
				return err
			}
			row.Status = "verified"
			row.Verification = verification
			return nil
		}
		if err := work(); err != nil {
			if row.Status != "blocked" {
				row.Status = "failed"
			}
			row.Error = err.Error()
			if len(row.Error) > 4000 {
				row.Error = row.Error[:4000]
			}
			failures++
			_, _ = fmt.Fprintln(log, err)
		}
		if err := log.Close(); err != nil {
			return report, err
		}
		report.DryRun = options.DryRun
		if err := save(); err != nil {
			return report, err
		}
	}
	if failures > 0 {
		return report, fmt.Errorf("batch has %d failed/blocked repositories; inspect report.json and saved plans", failures)
	}
	return report, nil
}
