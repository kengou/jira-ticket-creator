// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/kengou/jira-ticket-creator/internal/jira"
)

// jiraEnvVars are the environment variables the CLI reads. TestMain unsets
// them so no test depends on the developer's shell.
var jiraEnvVars = []string{"JIRA_URL", "JIRA_TOKEN", "JIRA_EMAIL", "JIRA_CLOUD", "JIRA_ALLOWED_HOSTS"}

// TestMain makes the whole cmd test binary hermetic: the JIRA_* environment is
// cleared, detection is replaced by a host-rule fake, and http.DefaultTransport
// refuses any non-loopback host, so an accidental real request fails loudly
// instead of reaching the network.
func TestMain(m *testing.M) {
	for _, k := range jiraEnvVars {
		if err := os.Unsetenv(k); err != nil {
			fmt.Fprintf(os.Stderr, "TestMain: unset %s: %v\n", k, err)
			os.Exit(1)
		}
	}
	detectPlatform = offlineDetect
	http.DefaultTransport = loopbackOnlyTransport{next: http.DefaultTransport}
	os.Exit(m.Run())
}

// offlineDetect mimics DetectPlatform's host rule without any I/O:
// forced → Cloud/SourceOverride; *.atlassian.net → Cloud/SourceHost;
// anything else → DataCenter/SourceServerInfo. APIBaseURL == ConfiguredURL.
func offlineDetect(_ context.Context, rawURL string, forceCloud bool, _ ...jira.DetectOption) (jira.Platform, error) {
	configured := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	u, err := url.Parse(configured)
	if err != nil {
		return jira.Platform{}, fmt.Errorf("invalid Jira URL: %w", err)
	}
	host := strings.ToLower(u.Hostname())

	p := jira.Platform{ConfiguredURL: configured, APIBaseURL: configured}
	switch {
	case forceCloud:
		p.Mode, p.Source = jira.ModeCloud, jira.SourceOverride
	case strings.HasSuffix(host, ".atlassian.net"):
		p.Mode, p.Source = jira.ModeCloud, jira.SourceHost
	default:
		p.Mode, p.Source = jira.ModeDataCenter, jira.SourceServerInfo
	}
	return p, nil
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
	return nil, fmt.Errorf("cmd tests must not reach the network: request to %s blocked", r.URL.Host)
}

// detectCall records one detectPlatform invocation.
type detectCall struct {
	rawURL     string
	forceCloud bool
}

// stubDetect makes detectPlatform return p, err for the rest of the test and
// records every call; the previous detector is restored by t.Cleanup.
func stubDetect(t *testing.T, p jira.Platform, err error) *[]detectCall {
	t.Helper()
	calls := &[]detectCall{}
	prev := detectPlatform
	detectPlatform = func(_ context.Context, rawURL string, forceCloud bool, _ ...jira.DetectOption) (jira.Platform, error) {
		*calls = append(*calls, detectCall{rawURL: rawURL, forceCloud: forceCloud})
		return p, err
	}
	t.Cleanup(func() { detectPlatform = prev })
	return calls
}

// captureOutput redirects os.Stdout and os.Stderr while fn runs and returns
// what each received. Both pipes are drained in goroutines while fn runs, so
// output larger than the pipe buffer cannot deadlock.
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe (stdout): %v", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		closeFile(t, rOut, "stdout pipe reader")
		closeFile(t, wOut, "stdout pipe writer")
		t.Fatalf("os.Pipe (stderr): %v", err)
	}

	var outBuf, errBuf strings.Builder
	var wg sync.WaitGroup
	drain := func(dst *strings.Builder, src *os.File, name string) {
		defer wg.Done()
		if _, err := io.Copy(dst, src); err != nil {
			t.Errorf("drain captured %s: %v", name, err)
		}
	}
	wg.Add(2)
	go drain(&outBuf, rOut, "stdout")
	go drain(&errBuf, rErr, "stderr")

	oldStdout, oldStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = wOut, wErr
	func() {
		// Deferred so the streams are restored and the pipes closed even when fn
		// fails the test with t.FailNow.
		defer func() {
			os.Stdout, os.Stderr = oldStdout, oldStderr
			closeFile(t, wOut, "stdout pipe writer")
			closeFile(t, wErr, "stderr pipe writer")
			wg.Wait()
			closeFile(t, rOut, "stdout pipe reader")
			closeFile(t, rErr, "stderr pipe reader")
		}()
		fn()
	}()
	return outBuf.String(), errBuf.String()
}

// closeFile closes f and reports a failure to t.
func closeFile(t *testing.T, f *os.File, name string) {
	t.Helper()
	if err := f.Close(); err != nil {
		t.Errorf("close %s: %v", name, err)
	}
}

func TestNoNetworkGuard(t *testing.T) {
	resp, err := http.Get("https://example.invalid/")
	if resp != nil {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("close response body: %v", closeErr)
		}
		t.Fatalf("http.Get(https://example.invalid/) returned a response (HTTP %d); want the offline guard to block it", resp.StatusCode)
	}
	if err == nil || !strings.Contains(err.Error(), "cmd tests must not reach the network") {
		t.Fatalf("http.Get(https://example.invalid/) error = %v, want the offline guard error", err)
	}
}
