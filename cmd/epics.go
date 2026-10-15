// SPDX-License-Identifier: Apache-2.0
package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/kengou/jira-ticket-creator/internal/jira"
)

var projectKey string
var epicStatus string
var epicsOutputFile string

var epicsCmd = &cobra.Command{
	Use:   "epics",
	Short: "List existing Epics in a Jira project",
	Long: `Fetches and displays all existing Epics from the specified Jira project.
Optionally filter by status (e.g. "In Progress", "Done").
Prefix with "NOT:" to negate (e.g. "NOT:Done" returns all epics that are NOT Done).

Use --output/-o to save the epics as a YAML file that follows the jira-ai-creator
schema format (schemaVersion, defaults, issues). The YAML includes the description
of each epic and can be used as input for the apply command.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateEpicsFlags(); err != nil {
			return err
		}
		return runEpics()
	},
}

func init() {
	rootCmd.AddCommand(epicsCmd)

	epicsCmd.Flags().StringVarP(&projectKey, "project", "p", "", "Jira project key (required)")
	epicsCmd.Flags().StringVarP(&epicStatus, "status", "s", "", "Filter by status (e.g. \"In Progress\", \"NOT:Done\" to negate)")
	epicsCmd.Flags().StringVarP(&epicsOutputFile, "output", "o", "", "Save epics to a YAML file (jira-ai-creator schema format)")
	if err := epicsCmd.MarkFlagRequired("project"); err != nil {
		log.Fatalf("internal error: failed to mark flag required: %v", err)
	}
}

// validateEpicsFlags checks that required flags for the epics command are set.
func validateEpicsFlags() error {
	// The global -f/--file flag is an input config for apply/validate/plan; the
	// epics command writes output via -o/--output. Reject -f loudly so a mixed-up
	// invocation doesn't silently fetch epics without writing any file.
	if configFile != "" {
		return fmt.Errorf("epics does not read a config file; did you mean --output/-o %s to save the epics to a YAML file?", configFile)
	}
	if err := requireAuth(); err != nil {
		return err
	}
	if projectKey == "" {
		return errors.New("project key is required (use -p or --project)")
	}
	return nil
}

// epicsYAMLIssue represents an epic in the YAML output format.
type epicsYAMLIssue struct {
	ID          string `json:"id" yaml:"id"`
	IssueType   string `json:"issueType" yaml:"issueType"`
	EpicName    string `json:"epicName" yaml:"epicName"`
	Summary     string `json:"summary" yaml:"summary"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// epicsYAMLDefaults represents the defaults section in the YAML output.
type epicsYAMLDefaults struct {
	ProjectKey string `json:"projectKey" yaml:"projectKey"`
}

// epicsYAMLConfig represents the full YAML output structure.
type epicsYAMLConfig struct {
	SchemaVersion string            `json:"schemaVersion" yaml:"schemaVersion"`
	Defaults      epicsYAMLDefaults `json:"defaults" yaml:"defaults"`
	Issues        []epicsYAMLIssue  `json:"issues" yaml:"issues"`
}

// epicsToYAML converts fetched epics to a YAML byte slice following the
// jira-ai-creator schema format.
func epicsToYAML(project string, epics []jira.Epic) ([]byte, error) {
	cfg := epicsYAMLConfig{
		SchemaVersion: "1.0",
		Defaults: epicsYAMLDefaults{
			ProjectKey: project,
		},
		Issues: make([]epicsYAMLIssue, 0, len(epics)),
	}

	for _, e := range epics {
		issue := epicsYAMLIssue{
			ID:          e.Key,
			IssueType:   "Epic",
			EpicName:    e.Summary,
			Summary:     e.Summary,
			Description: e.Description,
		}
		cfg.Issues = append(cfg.Issues, issue)
	}

	return yaml.Marshal(&cfg)
}

func runEpics() error {
	if epicStatus != "" {
		fmt.Fprintf(os.Stderr, "Fetching Epics for project %s (status: %s) from %s...\n\n", projectKey, epicStatus, targetLabel())
	} else {
		fmt.Fprintf(os.Stderr, "Fetching Epics for project %s from %s...\n\n", projectKey, targetLabel())
	}

	client, err := newJiraClient()
	if err != nil {
		return fmt.Errorf("create Jira client: %w", err)
	}

	spin := newSpinner("Fetching epics\u2026")
	spin.Start()
	epics, err := client.FetchEpics(context.Background(), projectKey, epicStatus)
	spin.Stop()
	if err != nil {
		return fmt.Errorf("failed to fetch epics: %w", err)
	}

	if len(epics) == 0 {
		fmt.Fprintf(os.Stderr, "No Epics found in project %s.\n", projectKey)
		return nil
	}

	fmt.Fprintf(os.Stderr, "Found %d Epic(s):\n\n", len(epics))
	fmt.Printf("  %-12s  %-14s  %s\n", "KEY", "STATUS", "SUMMARY")
	fmt.Printf("  %-12s  %-14s  %s\n", "---", "------", "-------")

	for _, epic := range epics {
		fmt.Printf("  %-12s  %-14s  %s\n", epic.Key, epic.Status, epic.Summary)
	}

	// Save to YAML file if --output is specified
	if epicsOutputFile != "" {
		yamlData, err := epicsToYAML(projectKey, epics)
		if err != nil {
			return fmt.Errorf("failed to marshal epics to YAML: %w", err)
		}

		if err := os.WriteFile(epicsOutputFile, yamlData, 0600); err != nil {
			return fmt.Errorf("failed to write YAML file %q: %w", epicsOutputFile, err)
		}

		fmt.Fprintf(os.Stderr, "\nEpics saved to %s\n", epicsOutputFile)
	}

	return nil
}
