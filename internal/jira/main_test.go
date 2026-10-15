// SPDX-License-Identifier: Apache-2.0

package jira

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
)

// jiraEnvVars are the environment variables the CLI reads. TestMain unsets
// them so no test depends on the developer's shell.
var jiraEnvVars = []string{"JIRA_URL", "JIRA_TOKEN", "JIRA_EMAIL", "JIRA_CLOUD", "JIRA_ALLOWED_HOSTS"}

// TestMain makes the whole internal/jira test binary hermetic: the JIRA_*
// environment is cleared, and every request through http.DefaultTransport to
// a non-loopback host fails loudly.
func TestMain(m *testing.M) {
	for _, k := range jiraEnvVars {
		if err := os.Unsetenv(k); err != nil {
			fmt.Fprintf(os.Stderr, "TestMain: unset %s: %v\n", k, err)
			os.Exit(1)
		}
	}
	http.DefaultTransport = loopbackOnlyTransport{next: http.DefaultTransport}
	os.Exit(m.Run())
}

// loopbackOnlyTransport refuses requests to any host that is not loopback.
type loopbackOnlyTransport struct{ next http.RoundTripper }

// RoundTrip delegates loopback requests (httptest servers) to the wrapped
// transport and blocks every other host.
func (l loopbackOnlyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	host := r.URL.Hostname()
	if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return l.next.RoundTrip(r)
	}
	return nil, fmt.Errorf("jira tests must not reach the network: request to %s blocked", r.URL.Host)
}

func TestNoNetworkGuard(t *testing.T) {
	resp, err := http.Get("https://example.invalid/")
	if resp != nil {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("close response body: %v", closeErr)
		}
		t.Fatalf("http.Get(https://example.invalid/) returned a response (HTTP %d); want the offline guard to block it", resp.StatusCode)
	}
	if err == nil || !strings.Contains(err.Error(), "jira tests must not reach the network") {
		t.Fatalf("http.Get(https://example.invalid/) error = %v, want the offline guard error", err)
	}
}

func TestMain_ClearsJiraEnv(t *testing.T) {
	for _, k := range []string{"JIRA_URL", "JIRA_TOKEN", "JIRA_EMAIL", "JIRA_CLOUD", "JIRA_ALLOWED_HOSTS"} {
		if _, set := os.LookupEnv(k); set {
			t.Errorf("%s is set at test start; TestMain must unset it so no test depends on the developer's shell", k)
		}
	}
}
