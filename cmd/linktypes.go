// SPDX-License-Identifier: Apache-2.0
package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kengou/jira-ticket-creator/internal/jira"
)

var linkTypeSearch string

var linkTypesCmd = &cobra.Command{
	Use:   "linktypes",
	Short: "List available Jira issue link types",
	Long: `Fetches and displays all issue link types from the Jira instance.
Each link type has a name (e.g. "Blocks"), an inward description
(e.g. "is blocked by") and an outward description (e.g. "blocks").

When creating links in your YAML config, use the exact name, inward,
or outward description from this list.

Examples:
  # List all available link types
  jira-ai-creator linktypes

  # Search for link types matching "block"
  jira-ai-creator linktypes --search block`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateLinkTypesFlags(); err != nil {
			return err
		}
		return runLinkTypes()
	},
}

func init() {
	rootCmd.AddCommand(linkTypesCmd)

	linkTypesCmd.Flags().StringVarP(&linkTypeSearch, "search", "s", "", "Filter link types by name (case-insensitive substring match)")
}

// validateLinkTypesFlags checks that required flags for the linktypes command are set.
func validateLinkTypesFlags() error {
	return requireAuth()
}

// matchLinkTypes filters link types by the search string (case-insensitive).
// It matches against the name, inward, and outward fields.
func matchLinkTypes(types []jira.IssueLinkTypeInfo, search string) []jira.IssueLinkTypeInfo {
	if search == "" {
		return types
	}

	searchLower := strings.ToLower(search)
	var matched []jira.IssueLinkTypeInfo
	for _, lt := range types {
		if strings.Contains(strings.ToLower(lt.Name), searchLower) ||
			strings.Contains(strings.ToLower(lt.Inward), searchLower) ||
			strings.Contains(strings.ToLower(lt.Outward), searchLower) {
			matched = append(matched, lt)
		}
	}
	return matched
}

func runLinkTypes() error {
	fmt.Printf("Fetching issue link types from %s...\n\n", targetLabel())

	client, err := newJiraClient()
	if err != nil {
		return fmt.Errorf("create Jira client: %w", err)
	}

	spin := newSpinner("Fetching link types…")
	spin.Start()
	types, err := client.FetchIssueLinkTypes(context.Background())
	spin.Stop()
	if err != nil {
		return fmt.Errorf("failed to fetch issue link types: %w", err)
	}

	matched := matchLinkTypes(types, linkTypeSearch)

	if len(matched) == 0 {
		if linkTypeSearch != "" {
			fmt.Printf("No link types found matching %q.\n", linkTypeSearch)
		} else {
			fmt.Println("No link types found.")
		}
		return nil
	}

	fmt.Printf("Found %d link type(s):\n\n", len(matched))
	fmt.Printf("  %-5s  %-25s  %-30s  %s\n", "ID", "NAME", "INWARD", "OUTWARD")
	fmt.Printf("  %-5s  %-25s  %-30s  %s\n", "---", "----", "------", "-------")

	for _, lt := range matched {
		fmt.Printf("  %-5s  %-25s  %-30s  %s\n", lt.ID, lt.Name, lt.Inward, lt.Outward)
	}

	fmt.Printf("\nIn your YAML config, use the NAME, INWARD, or OUTWARD value as the link type.\n")
	fmt.Printf("The tool will automatically resolve the correct Jira link type and direction.\n")

	return nil
}
