// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// e2eChildEnv makes the re-executed test binary behave as the CLI.
const e2eChildEnv = "JIRA_AI_CREATOR_E2E_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(e2eChildEnv) == "1" {
		main() // exits 1 itself on error
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// seenRequest is one request the fake Jira received.
type seenRequest struct {
	method, path, authorization, cookie string
}

// fakeJira answers /rest/api/2/serverInfo with serverInfoStatus/serverInfoBody,
// any path ending in "/myself" with 200 {"name":"e2e","accountId":"e2e"},
// and everything else with 404. It records every request.
type fakeJira struct {
	t                *testing.T
	mu               sync.Mutex
	seen             []seenRequest
	serverInfoStatus int
	serverInfoBody   string
}

func (f *fakeJira) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen = append(f.seen, seenRequest{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Cookie")})
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/rest/api/2/serverInfo":
		w.WriteHeader(f.serverInfoStatus)
		if _, err := w.Write([]byte(f.serverInfoBody)); err != nil {
			f.t.Errorf("write: %v", err)
		}
	case strings.HasSuffix(r.URL.Path, "/myself"):
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"name":"e2e","accountId":"e2e"}`)); err != nil {
			f.t.Errorf("write: %v", err)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
		if _, err := w.Write([]byte(`{}`)); err != nil {
			f.t.Errorf("write: %v", err)
		}
	}
}

func (f *fakeJira) requests() []seenRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seenRequest(nil), f.seen...)
}

// runCLI runs the CLI (this test binary in child mode) with ONLY the given env
// plus the fixed safety env, and returns stderr and the exit code.
func runCLI(t *testing.T, env []string, args ...string) (stderr string, exitCode int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c := exec.CommandContext(ctx, os.Args[0], args...) //nolint:gosec // G204: re-executes this test binary as the CLI under test
	c.Env = append([]string{
		e2eChildEnv + "=1",
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"NO_COLOR=1",
		"HTTPS_PROXY=http://127.0.0.1:9",
		"HTTP_PROXY=http://127.0.0.1:9",
	}, env...)
	var stdoutBuf, stderrBuf bytes.Buffer
	c.Stdout = &stdoutBuf
	c.Stderr = &stderrBuf

	err := c.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return stderrBuf.String(), 0
	case errors.As(err, &exitErr):
		return stderrBuf.String(), exitErr.ExitCode()
	default:
		t.Fatalf("running CLI: %v", err)
		return "", -1
	}
}

func assertContains(t *testing.T, stderr, want string) {
	t.Helper()
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
}

func TestE2E_PlatformDetection(t *testing.T) {
	const email = "JIRA_EMAIL=e2e@example.com"

	tests := []struct {
		name   string
		args   []string
		env    []string
		status int
		body   string
		check  func(t *testing.T, stderr string, exitCode int, reqs []seenRequest)
	}{
		{
			name:   "serverinfo_datacenter",
			args:   []string{"check"},
			status: http.StatusOK,
			body:   `{"deploymentType":"Server"}`,
			check: func(t *testing.T, stderr string, exitCode int, reqs []seenRequest) {
				if exitCode != 0 {
					t.Errorf("exit = %d, want 0; stderr = %q", exitCode, stderr)
				}
				assertContains(t, stderr, "Platform mode: Data Center (serverInfo)")
				if n := countPath(reqs, "/rest/api/2/serverInfo"); n != 1 {
					t.Errorf("serverInfo requests = %d, want 1; stderr = %q", n, stderr)
				}
				if !hasRequest(reqs, "/rest/api/2/myself", func(a string) bool { return a == "Bearer e2e-dummy-token" }) {
					t.Errorf("no Bearer request to /rest/api/2/myself; stderr = %q", stderr)
				}
			},
		},
		{
			name:   "probe_failure_warns",
			args:   []string{"check"},
			status: http.StatusNotFound,
			body:   `{"errorMessages":["nope"]}`,
			check: func(t *testing.T, stderr string, exitCode int, _ []seenRequest) {
				if exitCode != 0 {
					t.Errorf("exit = %d, want 0; stderr = %q", exitCode, stderr)
				}
				assertContains(t, stderr, "Warning: could not detect the Jira platform for 127.0.0.1")
				assertContains(t, stderr, "assuming Data Center")
				assertContains(t, stderr, "Platform mode: Data Center (not detected, assumed)")
				if strings.Contains(stderr, "nope") {
					t.Errorf("stderr = %q, must not echo the response body", stderr)
				}
			},
		},
		{
			name:   "cloud_without_usable_baseurl_fails_closed",
			args:   []string{"check"},
			env:    []string{email},
			status: http.StatusOK,
			body:   `{"deploymentType":"Cloud","baseUrl":"https://evil.example.com"}`,
			check: func(t *testing.T, stderr string, exitCode int, reqs []seenRequest) {
				if exitCode == 0 {
					t.Errorf("exit = 0, want non-zero; stderr = %q", stderr)
				}
				assertContains(t, stderr, "did not advertise a usable *.atlassian.net API address")
				assertNoCredentials(t, stderr, reqs)
			},
		},
		{
			name:   "allowlist_blocks_derived_cloud_host",
			args:   []string{"check"},
			env:    []string{email, "JIRA_ALLOWED_HOSTS=127.0.0.1"},
			status: http.StatusOK,
			body:   `{"deploymentType":"Cloud","baseUrl":"https://e2e-site.atlassian.net"}`,
			check: func(t *testing.T, stderr string, exitCode int, reqs []seenRequest) {
				if exitCode == 0 {
					t.Errorf("exit = 0, want non-zero; stderr = %q", stderr)
				}
				assertContains(t, stderr, "e2e-site.atlassian.net")
				assertContains(t, stderr, "JIRA_ALLOWED_HOSTS")
				assertNoCredentials(t, stderr, reqs)
			},
		},
		{
			name:   "jira_cloud_env_is_deprecated",
			args:   []string{"check"},
			env:    []string{email, "JIRA_CLOUD=true"},
			status: http.StatusOK,
			body:   `{"deploymentType":"Server"}`,
			check: func(t *testing.T, stderr string, exitCode int, reqs []seenRequest) {
				if exitCode != 0 {
					t.Errorf("exit = %d, want 0; stderr = %q", exitCode, stderr)
				}
				assertContains(t, stderr, "JIRA_CLOUD is deprecated")
				assertContains(t, stderr, "Platform mode: Cloud (forced by --cloud/JIRA_CLOUD (deprecated))")
				if !hasRequest(reqs, "/rest/api/3/myself", func(a string) bool { return strings.HasPrefix(a, "Basic ") }) {
					t.Errorf("no Basic request to /rest/api/3/myself; stderr = %q", stderr)
				}
			},
		},
		{
			name:   "cloud_flag_is_deprecated",
			args:   []string{"check", "--cloud"},
			env:    []string{email},
			status: http.StatusOK,
			body:   `{"deploymentType":"Server"}`,
			check: func(t *testing.T, stderr string, exitCode int, _ []seenRequest) {
				if exitCode != 0 {
					t.Errorf("exit = %d, want 0; stderr = %q", exitCode, stderr)
				}
				assertContains(t, stderr, "Flag --cloud has been deprecated")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeJira{t: t, serverInfoStatus: tt.status, serverInfoBody: tt.body}
			srv := httptest.NewServer(f)
			t.Cleanup(srv.Close)

			env := append([]string{"JIRA_URL=" + srv.URL, "JIRA_TOKEN=e2e-dummy-token"}, tt.env...)
			stderr, exitCode := runCLI(t, env, tt.args...)
			reqs := f.requests()

			for _, r := range reqs {
				if r.path != "/rest/api/2/serverInfo" {
					continue
				}
				if r.method != http.MethodGet {
					t.Errorf("serverInfo method = %q, want %q", r.method, http.MethodGet)
				}
				if r.authorization != "" {
					t.Errorf("serverInfo request carried an Authorization header")
				}
				if r.cookie != "" {
					t.Errorf("serverInfo request carried a Cookie header")
				}
			}

			tt.check(t, stderr, exitCode, reqs)
		})
	}
}

func countPath(reqs []seenRequest, path string) int {
	n := 0
	for _, r := range reqs {
		if r.path == path {
			n++
		}
	}
	return n
}

func hasRequest(reqs []seenRequest, path string, authOK func(string) bool) bool {
	for _, r := range reqs {
		if r.path == path && authOK(r.authorization) {
			return true
		}
	}
	return false
}

func assertNoCredentials(t *testing.T, stderr string, reqs []seenRequest) {
	t.Helper()
	for _, r := range reqs {
		if r.authorization != "" {
			t.Errorf("credential sent to %s %s; stderr = %q", r.method, r.path, stderr)
		}
	}
}
