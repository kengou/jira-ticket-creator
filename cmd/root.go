// SPDX-License-Identifier: Apache-2.0
//
// Package cmd implements the CLI commands for jira-ai-creator.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kengou/jira-ticket-creator/internal/jira"
	"github.com/kengou/jira-ticket-creator/internal/ui"
)

var (
	// Global flags
	configFile string
	jiraURL    string
	jiraToken  string
	jiraEmail  string
	verbose    bool
	isCloud    bool

	// resolvedPlatform is the Jira platform resolved once by requireAuth: mode,
	// how it was detected, the configured URL, and the API base URL every
	// authenticated request (and therefore every credential) goes to.
	resolvedPlatform jira.Platform

	// Command-specific flags
	dryRun          bool
	continueOnError bool
)

// detectPlatform resolves the platform for a Jira URL. It is a seam: cmd
// tests replace it so they never touch the network.
var detectPlatform = jira.DetectPlatform

var rootCmd = &cobra.Command{
	Use:   "jira-ai-creator",
	Short: "Create Jira issues from YAML configuration",
	Long: `jira-ai-creator is a CLI tool that creates Jira issues from YAML configuration files.

It supports:
  - Epic/Story/Bug hierarchies with parent references
  - Issue dependencies and automatic ordering
  - Custom fields and description templates
  - Idempotency (won't create duplicates on re-runs)
  - Issue links (blocks, relates to, duplicates, etc.)

Quick start:
  1. Export credentials:

     Jira Cloud (Atlassian email + API token from id.atlassian.com):
       export JIRA_URL="https://yourcompany.atlassian.net"
       export JIRA_EMAIL="you@yourcompany.com"
       export JIRA_TOKEN="your-api-token"

     Jira Data Center (personal access token):
       export JIRA_URL="https://jira.yourcompany.com"
       export JIRA_TOKEN="your-personal-access-token"

     The platform is detected automatically: *.atlassian.net URLs are Cloud;
     any other URL is identified by one unauthenticated request to
     /rest/api/2/serverInfo, so Cloud sites on a custom domain work and their
     API calls go to the site's *.atlassian.net address. Run
     "jira-ai-creator check" to see what was detected. --cloud and
     JIRA_CLOUD are deprecated.
     To pin the Cloud tenant, set JIRA_ALLOWED_HOSTS to include both your
     configured host and its *.atlassian.net host.

  2. Validate your config:
     jira-ai-creator validate -f issues.yaml

  3. Preview what will be created:
     jira-ai-creator plan -f issues.yaml

  4. Create issues:
     jira-ai-creator apply -f issues.yaml`,
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	// Persistent flags (available to all commands).
	// Sensitive values (jira-url, token) default to "" so that the actual values
	// never appear in --help output. The env vars are applied in requireAuth().
	rootCmd.PersistentFlags().StringVarP(&configFile, "file", "f", "", "YAML configuration file")
	rootCmd.PersistentFlags().StringVar(&jiraURL, "jira-url", "", "Jira base URL (env: JIRA_URL)")
	rootCmd.PersistentFlags().StringVar(&jiraToken, "token", "", "Jira API token (Cloud) or personal access token (Data Center) (env: JIRA_TOKEN)")
	rootCmd.PersistentFlags().BoolVar(&verbose, "verbose", false, "Enable verbose output")
	rootCmd.PersistentFlags().BoolVar(&isCloud, "cloud", false, "Deprecated: force Jira Cloud mode (env: JIRA_CLOUD)")
	if err := rootCmd.PersistentFlags().MarkDeprecated("cloud", "Cloud is detected automatically (including custom domains); the flag will be removed in a future release"); err != nil {
		log.Fatalf("internal error: failed to mark --cloud deprecated: %v", err)
	}
	rootCmd.PersistentFlags().StringVar(&jiraEmail, "email", "", "Atlassian account email for Jira Cloud (env: JIRA_EMAIL)")
}

// newJiraClient creates a configured Jira client for the platform resolved by
// requireAuth: it talks to resolvedPlatform.APIBaseURL in resolvedPlatform.Mode
// with the current credential globals. It refuses an unresolved platform, so
// credentials can never go to an empty or stale base.
func newJiraClient() (*jira.Client, error) {
	if resolvedPlatform.APIBaseURL == "" {
		return nil, errors.New("internal error: Jira platform not resolved (requireAuth must run before newJiraClient)")
	}
	return jira.NewClientWithMode(resolvedPlatform.APIBaseURL, resolvedPlatform.Mode, jira.Credentials{Email: jiraEmail, Token: jiraToken})
}

// maskToken redacts a token for safe logging, exposing only the last 4 characters.
func maskToken(token string) string {
	if len(token) < 12 {
		return "****"
	}
	return "****" + token[len(token)-4:]
}

// useEmoji reports whether emoji output is appropriate (NO_COLOR is not set
// and TERM is not dumb). It delegates to internal/ui for a single source of
// truth; the local wrapper keeps all existing call sites (including spinner.go)
// unchanged.
func useEmoji() bool {
	return ui.UseEmoji()
}

// emoji returns e when emoji output is appropriate, otherwise fallback. It
// delegates to internal/ui for a single source of truth; the local wrapper
// keeps all existing call sites unchanged.
func emoji(e, fallback string) string {
	return ui.Emoji(e, fallback)
}

// configuredHost returns host[:port] of the configured Jira URL for labels and warnings.
func configuredHost() string {
	u, err := url.Parse(resolvedPlatform.ConfiguredURL)
	if err != nil || u.Host == "" {
		return resolvedPlatform.ConfiguredURL
	}
	return u.Host
}

// targetLabel names where authenticated requests go: the API base URL, plus
// "(via <configured host>)" when a Cloud custom domain was resolved to its
// *.atlassian.net address.
func targetLabel() string {
	if resolvedPlatform.CustomDomain() {
		return resolvedPlatform.APIBaseURL + " (via " + configuredHost() + ")"
	}
	return resolvedPlatform.APIBaseURL
}

// requireAuth applies env-var defaults, checks that Jira authentication is set,
// and detects the Jira platform. It MUST run before newJiraClient, runCheck,
// runApply (non-dry-run) or targetLabel because it populates the package-level
// resolvedPlatform global that those read. A failed detection leaves
// resolvedPlatform zero, so newJiraClient refuses to build a client. In Cloud
// mode a missing email is rejected before any authenticated request is made.
func requireAuth() error {
	// Apply env var defaults when the flag was not explicitly provided.
	if jiraURL == "" {
		jiraURL = os.Getenv("JIRA_URL")
	}
	if jiraToken == "" {
		jiraToken = os.Getenv("JIRA_TOKEN")
	}
	if jiraEmail == "" {
		jiraEmail = os.Getenv("JIRA_EMAIL")
	}

	if jiraURL == "" {
		return errors.New("jira URL is required (use --jira-url or set JIRA_URL)")
	}
	if jiraToken == "" {
		return errors.New("jira token is required (use --token or set JIRA_TOKEN)")
	}

	// Warn when the token was provided via the CLI flag rather than the env var.
	// CLI arguments are visible in process listings (ps aux) and shell history.
	if strings.TrimSpace(os.Getenv("JIRA_TOKEN")) != strings.TrimSpace(jiraToken) {
		fmt.Fprintln(os.Stderr, emoji("\u26a0\ufe0f", "[WARN]")+"  Warning: --token passed on command line is visible in process listings; prefer setting JIRA_TOKEN env var")
	}

	envCloud := cloudEnvSet()
	if envCloud {
		fmt.Fprintln(os.Stderr, "Warning: JIRA_CLOUD is deprecated: Cloud is detected automatically (including custom domains); unset JIRA_CLOUD. It will be removed in a future release.")
	}

	// Reset first (plan gate P9): a failed detection must never leave a platform
	// from an earlier call in place for newJiraClient to send credentials to.
	resolvedPlatform = jira.Platform{}
	platform, err := detectPlatform(context.Background(), jiraURL, isCloud || envCloud)
	if err != nil {
		return err
	}
	resolvedPlatform = platform

	if platform.Source == jira.SourceFallback {
		fmt.Fprintf(os.Stderr, "Warning: could not detect the Jira platform for %s (%s); assuming Data Center. If this is Jira Cloud, set JIRA_URL to your https://<site>.atlassian.net address.\n", configuredHost(), platform.FallbackReason)
	}

	if verbose {
		fmt.Fprintf(os.Stderr, "Jira platform mode: %s (%s)\n", platform.Mode, platform.Source)
		if platform.CustomDomain() {
			fmt.Fprintf(os.Stderr, "Jira custom domain: %s → API base %s\n", configuredHost(), platform.APIBaseURL)
		}
	}

	// Fail fast: Cloud requires an Atlassian email together with the API token.
	if platform.Mode == jira.ModeCloud && strings.TrimSpace(jiraEmail) == "" {
		return errors.New("jira Cloud requires an Atlassian email together with an API token (use --email or set JIRA_EMAIL)")
	}

	return nil
}

// cloudEnvSet reports whether the JIRA_CLOUD environment variable is set to a
// truthy value ("1", "true", "yes", case-insensitive).
func cloudEnvSet() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("JIRA_CLOUD"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// validateFlags checks that required flags are set.
func validateFlags(needsAuth bool) error {
	if configFile == "" {
		return errors.New("config file is required (use -f or --file)")
	}
	if needsAuth {
		return requireAuth()
	}
	return nil
}
