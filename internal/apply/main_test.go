// SPDX-License-Identifier: Apache-2.0

package apply

import (
	"fmt"
	"os"
	"testing"
)

// jiraEnvVars are the environment variables the CLI reads. TestMain unsets
// them so no test depends on the developer's shell.
var jiraEnvVars = []string{"JIRA_URL", "JIRA_TOKEN", "JIRA_EMAIL", "JIRA_CLOUD", "JIRA_ALLOWED_HOSTS"}

// TestMain makes the whole internal/apply test binary (package apply and
// apply_test alike) independent of the developer's shell by clearing the
// JIRA_* environment before any test runs.
func TestMain(m *testing.M) {
	for _, k := range jiraEnvVars {
		if err := os.Unsetenv(k); err != nil {
			fmt.Fprintf(os.Stderr, "TestMain: unset %s: %v\n", k, err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func TestMain_ClearsJiraEnv(t *testing.T) {
	for _, k := range []string{"JIRA_URL", "JIRA_TOKEN", "JIRA_EMAIL", "JIRA_CLOUD", "JIRA_ALLOWED_HOSTS"} {
		if _, set := os.LookupEnv(k); set {
			t.Errorf("%s is set at test start; TestMain must unset it so no test depends on the developer's shell", k)
		}
	}
}
