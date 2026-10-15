// SPDX-License-Identifier: Apache-2.0
package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kengou/jira-ticket-creator/internal/jira"
)

var (
	fieldSearch string
	fieldCustom bool
)

var fieldsCmd = &cobra.Command{
	Use:   "fields",
	Short: "List available Jira fields",
	Long: `Fetches and displays all field definitions from the Jira instance.
This is useful for discovering custom field IDs, for example finding
the correct "Epic Link" or "Epic Name" field for your Jira Data Center.

Examples:
  # List all fields containing "epic" in the name
  jira-ai-creator fields --search epic

  # List only custom fields
  jira-ai-creator fields --custom-only

  # Combine both: custom fields matching "epic"
  jira-ai-creator fields --search epic --custom-only`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateFieldsFlags(); err != nil {
			return err
		}
		return runFields()
	},
}

func init() {
	rootCmd.AddCommand(fieldsCmd)

	fieldsCmd.Flags().StringVarP(&fieldSearch, "search", "s", "", "Filter fields by name (case-insensitive substring match)")
	fieldsCmd.Flags().BoolVar(&fieldCustom, "custom-only", false, "Show only custom fields")
}

// validateFieldsFlags checks that required flags for the fields command are set.
func validateFieldsFlags() error {
	return requireAuth()
}

// matchFields filters and returns fields matching the current search/custom criteria.
func matchFields(fields []jira.Field) []jira.Field {
	var matched []jira.Field
	searchLower := strings.ToLower(fieldSearch)

	for _, f := range fields {
		if fieldCustom && !f.Custom {
			continue
		}
		if fieldSearch != "" && !strings.Contains(strings.ToLower(f.Name), searchLower) {
			continue
		}
		matched = append(matched, f)
	}
	return matched
}

func runFields() error {
	fmt.Printf("Fetching fields from %s...\n\n", targetLabel())

	client, err := newJiraClient()
	if err != nil {
		return fmt.Errorf("create Jira client: %w", err)
	}

	spin := newSpinner("Fetching fields…")
	spin.Start()
	fields, err := client.FetchFields(context.Background())
	spin.Stop()
	if err != nil {
		return fmt.Errorf("failed to fetch fields: %w", err)
	}

	matched := matchFields(fields)

	if len(matched) == 0 {
		if fieldSearch != "" {
			fmt.Printf("No fields found matching %q.\n", fieldSearch)
		} else {
			fmt.Println("No fields found.")
		}
		return nil
	}

	fmt.Printf("Found %d field(s):\n\n", len(matched))
	fmt.Printf("  %-30s  %-8s  %-40s  %s\n", "ID", "CUSTOM", "NAME", "SCHEMA TYPE")
	fmt.Printf("  %-30s  %-8s  %-40s  %s\n", "---", "------", "----", "-----------")

	for _, f := range matched {
		custom := "no"
		if f.Custom {
			custom = "yes"
		}

		schemaType := "-"
		if f.Schema != nil {
			schemaType = f.Schema.Type
			if f.Schema.Custom != "" {
				schemaType += " (" + f.Schema.Custom + ")"
			}
		}

		fmt.Printf("  %-30s  %-8s  %-40s  %s\n", f.ID, custom, f.Name, schemaType)
	}

	return nil
}
