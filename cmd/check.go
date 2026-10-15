// SPDX-License-Identifier: Apache-2.0
package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/kengou/jira-ticket-creator/internal/config"
	"github.com/kengou/jira-ticket-creator/internal/jira"
)

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Verify Jira connection, authentication, and project access",
	Long: `Runs read-only preflight checks against the configured Jira instance:
detected platform mode, authentication (and credential style), API reachability,
and — when a config file with a project key is provided — project access.

No issue or other content is created or modified. The command exits non-zero if
any step fails, and failure messages explain the likely cause.

Examples:
  # Check auth and reachability
  jira-ai-creator check

  # Also verify access to the project key in a config file
  jira-ai-creator check -f issues.yaml`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		return runCheck()
	},
}

func init() {
	rootCmd.AddCommand(checkCmd)
}

// preflightClient is the read-only probe surface used by runPreflight. The
// concrete *jira.Client satisfies it; tests supply a fake. This keeps the preflight
// decoupled from apply.JiraClient (which is intentionally not grown).
type preflightClient interface {
	CheckAuth(ctx context.Context) error
	CheckProjectAccess(ctx context.Context, projectKey string) error
}

// credentialStyle names the credential style in use for the given mode, for
// human-readable output.
func credentialStyle(mode jira.Mode) string {
	if mode == jira.ModeCloud {
		return "Basic auth (Atlassian email + API token)"
	}
	return "Bearer auth (personal access token / PAT)"
}

// diagnoseAuthError converts an authentication probe error into a
// diagnosis-oriented message. A 401/403 from Cloud points at the email + API token
// (Basic auth) requirement; from Data Center it points at the PAT. Other errors are
// returned as-is.
func diagnoseAuthError(mode jira.Mode, err error) string {
	var apiErr *jira.APIError
	if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden) {
		if mode == jira.ModeCloud {
			return fmt.Sprintf("authentication failed (%d): Jira Cloud requires an Atlassian email together with an API token (Basic auth). Check --email/JIRA_EMAIL and that the token is a Cloud API token from id.atlassian.com", apiErr.StatusCode)
		}
		return fmt.Sprintf("authentication failed (%d): Jira Data Center requires a valid personal access token (PAT). Check --token/JIRA_TOKEN (Bearer auth)", apiErr.StatusCode)
	}
	return fmt.Sprintf("authentication check failed: %v", err)
}

// diagnoseProjectError converts a project-access probe error into a
// diagnosis-oriented message. A 404/403 names the project key and the permission
// possibility. Other errors are returned as-is.
func diagnoseProjectError(projectKey string, err error) string {
	var apiErr *jira.APIError
	if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusNotFound || apiErr.StatusCode == http.StatusForbidden) {
		return fmt.Sprintf("project access failed (%d): project %q was not found or you lack permission to access it. Verify the project key and your permissions", apiErr.StatusCode, projectKey)
	}
	return fmt.Sprintf("project access check failed: %v", err)
}

// runPreflightSteps runs the authentication probe and (when projectKey is
// non-empty) the project-access probe against client. For each step it calls
// report(step, err) — err is nil on success or the diagnosed error on failure.
// Execution stops on the first failure. The function returns nil when all
// executed steps pass, or the diagnosed error of the failing step.
//
// This is the shared step engine used by both runPreflight (silent, used by
// apply) and runCheck (prints per-step status, used by the check command).
func runPreflightSteps(ctx context.Context, client preflightClient, mode jira.Mode, projectKey string, report func(step string, err error)) error {
	if authErr := client.CheckAuth(ctx); authErr != nil {
		diagnosed := errors.New(diagnoseAuthError(mode, authErr))
		if report != nil {
			report("auth", diagnosed)
		}
		return diagnosed
	}
	if report != nil {
		report("auth", nil)
	}

	if projectKey != "" {
		if projErr := client.CheckProjectAccess(ctx, projectKey); projErr != nil {
			diagnosed := errors.New(diagnoseProjectError(projectKey, projErr))
			if report != nil {
				report("project", diagnosed)
			}
			return diagnosed
		}
		if report != nil {
			report("project", nil)
		}
	}
	return nil
}

// runPreflight runs the lightweight preflight subset — authentication, then (when a
// non-empty projectKey is supplied) project access — returning a diagnosis-wrapped
// error on the first failing step. It is shared by the check command and the
// non-dry-run apply path.
func runPreflight(ctx context.Context, client preflightClient, mode jira.Mode, projectKey string) error {
	return runPreflightSteps(ctx, client, mode, projectKey, nil)
}

// projectKeyFromConfig reads defaults.projectKey from the config file when one is
// provided via -f/--file. It returns "" (and no error) when no file is configured,
// so the check command can proceed with auth-only verification.
func projectKeyFromConfig() (string, error) {
	if configFile == "" {
		return "", nil
	}
	raw, err := os.ReadFile(filepath.Clean(configFile))
	if err != nil {
		return "", fmt.Errorf("failed to read config file: %w", err)
	}
	cfg, err := config.LoadConfigFromBytes(raw)
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}
	return cfg.Defaults.ProjectKey, nil
}

// runCheck executes the standalone check command: it reports the detected platform
// mode and how it was detected, the API base when a Cloud custom domain was
// resolved to its *.atlassian.net address, the credential style, authentication +
// API reachability, and (when a project key is available) project access. It
// returns a non-nil error (→ non-zero exit) on the first failing step.
//
// requireAuth must have run before runCheck so that resolvedPlatform is populated.
func runCheck() error {
	pass := emoji("✅", "[OK]")
	fail := emoji("❌", "[FAIL]")

	// Use the platform already resolved by requireAuth.
	mode := resolvedPlatform.Mode
	fmt.Fprintf(os.Stderr, "%s Platform mode: %s (%s)\n", pass, mode, resolvedPlatform.Source)
	if resolvedPlatform.CustomDomain() {
		fmt.Fprintf(os.Stderr, "   API base: %s (custom domain %s)\n", resolvedPlatform.APIBaseURL, configuredHost())
	}
	fmt.Fprintf(os.Stderr, "   Credential style: %s\n", credentialStyle(mode))

	client, err := newJiraClient()
	if err != nil {
		return fmt.Errorf("create Jira client: %w", err)
	}

	// Project access (only when a project key is available from the config file).
	projectKey, err := projectKeyFromConfig()
	if err != nil {
		return err
	}

	ctx := context.Background()

	// Run preflight steps, printing a status line for each.
	stepErr := runPreflightSteps(ctx, client, mode, projectKey, func(step string, err error) {
		switch step {
		case "auth":
			if err == nil {
				fmt.Fprintf(os.Stderr, "%s Authentication and API reachability\n", pass)
			} else {
				fmt.Fprintf(os.Stderr, "%s Authentication / reachability\n", fail)
			}
		case "project":
			if err == nil {
				fmt.Fprintf(os.Stderr, "%s Project access (%s)\n", pass, projectKey)
			} else {
				fmt.Fprintf(os.Stderr, "%s Project access (%s)\n", fail, projectKey)
			}
		}
	})
	if stepErr != nil {
		return stepErr
	}

	// If auth passed but no project key was available, report the skip.
	if projectKey == "" {
		fmt.Fprintf(os.Stderr, "%s  Project access skipped (no project key; pass -f <config> to check it)\n", emoji("⏭️", "[SKIP]"))
	}
	return nil
}
