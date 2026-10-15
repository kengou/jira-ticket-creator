// SPDX-License-Identifier: Apache-2.0
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kengou/jira-ticket-creator/internal/apply"
	"github.com/kengou/jira-ticket-creator/internal/config"
	"github.com/kengou/jira-ticket-creator/internal/jira"
	"github.com/kengou/jira-ticket-creator/internal/validation"
)

var applyYes bool

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Create issues in Jira",
	Long:  `Creates issues in Jira according to the configuration file.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// For dry-run, we don't need authentication
		needsAuth := !dryRun
		if err := validateFlags(needsAuth); err != nil {
			return err
		}
		return runApply()
	},
}

func init() {
	rootCmd.AddCommand(applyCmd)

	applyCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Simulate creation without making API calls")
	applyCmd.Flags().BoolVar(&continueOnError, "continue-on-error", false, "Continue creating issues even if one fails")
	applyCmd.Flags().BoolVar(&applyYes, "yes", false, "Skip review confirmation prompt (not recommended)")
}

func runApply() error {
	fmt.Fprintf(os.Stderr, "%s Applying: %s\n", emoji("\U0001f680", "[APPLY]"), configFile)
	if dryRun {
		fmt.Fprintln(os.Stderr, "   [DRY RUN MODE - No changes will be made]")
		fmt.Fprintln(os.Stderr)
	} else {
		fmt.Fprintf(os.Stderr, "   Target: %s (token: %s)\n\n", targetLabel(), maskToken(jiraToken))
	}

	// Read the config file once; reuse the bytes for the reviewed-marker check,
	// schema validation, and deserialization.
	rawBytes, readErr := os.ReadFile(filepath.Clean(configFile))
	if readErr != nil {
		return fmt.Errorf("failed to read config file: %w", readErr)
	}

	// Check for AI-generated, un-reviewed files.
	if strings.Contains(string(rawBytes), "# reviewed: false") {
		fmt.Fprintln(os.Stderr, "WARNING: This file was AI-generated and has not been marked as reviewed.")
		fmt.Fprintln(os.Stderr, "         Edit the file and change '# reviewed: false' to '# reviewed: true' after manual review.")
		if !dryRun {
			switch {
			case applyYes:
				fmt.Fprintln(os.Stderr, "WARNING: Skipping review confirmation (--yes set).")
			case !isTTY(os.Stdin):
				return errors.New("aborted: stdin is not interactive; use --yes to bypass the review gate or set '# reviewed: true' in the file")
			default:
				if !confirmPrompt(os.Stderr, os.Stdin, "Proceed anyway? [y/N] ") {
					return errors.New("aborted: review the AI-generated file before applying")
				}
			}
		}
	}

	// Schema-validate the raw YAML BEFORE deserialization (OWASP ASVS V5.5).
	if err := validation.ValidateRawYAML(rawBytes); err != nil {
		return fmt.Errorf("config file failed schema validation: %w", err)
	}

	// Load config
	cfg, err := config.LoadConfigFromBytes(rawBytes)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Override continue-on-error if flag is set
	if continueOnError {
		if cfg.Options == nil {
			cfg.Options = &config.Options{}
		}
		cfg.Options.ContinueOnError = true
	}

	// Validate
	errs := validateConfig(cfg)
	hasErrors := false
	for _, e := range errs {
		if e.Severity == validation.SeverityError {
			if !hasErrors {
				fmt.Fprintf(os.Stderr, "%s Configuration has validation errors:\n", emoji("\u274c", "[ERR]"))
				hasErrors = true
			}
			fmt.Fprintf(os.Stderr, "  - %s\n", e.String())
		}
	}
	if hasErrors {
		return errors.New("cannot apply due to validation errors")
	}

	// Print warnings
	hasWarnings := false
	for _, e := range errs {
		if e.Severity == validation.SeverityWarning {
			if !hasWarnings {
				fmt.Fprintf(os.Stderr, "%s  Warnings:\n", emoji("\u26a0\ufe0f", "[WARN]"))
				hasWarnings = true
			}
			fmt.Fprintf(os.Stderr, "  - %s\n", e.String())
		}
	}
	if hasWarnings {
		fmt.Fprintln(os.Stderr)
	}

	// Create Jira client (or a no-op stub for dry-run mode).
	var client apply.JiraClient = dryRunClient{}
	if !dryRun {
		c, err := newJiraClient()
		if err != nil {
			return fmt.Errorf("create Jira client: %w", err)
		}

		// Preflight: verify auth and project access before creating anything.
		// Dry-run skips this entirely (no network), keeping dry-run offline.
		fmt.Fprintf(os.Stderr, "%s Running preflight checks...\n", emoji("\U0001f50d", "[CHECK]"))
		if err := runPreflight(context.Background(), c, resolvedPlatform.Mode, cfg.Defaults.ProjectKey); err != nil {
			return fmt.Errorf("preflight failed: %w", err)
		}

		client = c
	}

	// Create applier — mode is the resolved platform mode (cmd/root.go); the
	// applier derives cloud-ness and selects its field encoder from it.
	applier := apply.NewApplier(cfg, client, resolvedPlatform.Mode, verbose, dryRun, configFile)

	// Apply
	if err := applier.Apply(context.Background()); err != nil {
		return err
	}

	if !dryRun {
		fmt.Fprintf(os.Stderr, "\n%s All done! Issues have been created in Jira.\n", emoji("\U0001f389", "[DONE]"))
	} else {
		fmt.Fprintf(os.Stderr, "\n%s Dry run complete. No issues were created.\n", emoji("\u2705", "[OK]"))
	}

	return nil
}

// dryRunClient is a no-op JiraClient used when --dry-run is set.
// It satisfies the apply.JiraClient interface but performs no real API calls;
// the Applier guards every client call with a dryRun check, so these methods
// are only invoked if that guard is accidentally removed.
type dryRunClient struct{}

var _ apply.JiraClient = dryRunClient{}

func (dryRunClient) CreateIssue(_ context.Context, _ *jira.CreateIssueRequest) (*jira.CreateIssueResponse, error) {
	return &jira.CreateIssueResponse{Key: "DRY-RUN"}, nil
}

func (dryRunClient) CreateIssueLink(_ context.Context, _ *jira.IssueLinkRequest) error { return nil }

func (dryRunClient) FetchIssueLinkTypes(_ context.Context) ([]jira.IssueLinkTypeInfo, error) {
	return nil, nil
}

func (dryRunClient) SearchUsers(_ context.Context, _ string) ([]jira.User, error) {
	return nil, nil
}
