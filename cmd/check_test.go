// SPDX-License-Identifier: Apache-2.0
package cmd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kengou/jira-ticket-creator/internal/jira"
)

func TestCredentialStyle(t *testing.T) {
	if got := credentialStyle(jira.ModeCloud); !strings.Contains(got, "email") || !strings.Contains(got, "API token") {
		t.Errorf("credentialStyle(Cloud) = %q, want it to mention email + API token", got)
	}
	if got := credentialStyle(jira.ModeDataCenter); !strings.Contains(got, "PAT") && !strings.Contains(got, "personal access token") {
		t.Errorf("credentialStyle(DataCenter) = %q, want it to mention the PAT", got)
	}
}

func TestDiagnoseAuthError_Cloud401NamesEmailAndToken(t *testing.T) {
	err := &jira.APIError{StatusCode: 401, Body: "Unauthorized"}
	msg := diagnoseAuthError(jira.ModeCloud, err)
	if !strings.Contains(msg, "email") || !strings.Contains(msg, "API token") {
		t.Errorf("Cloud 401 diagnosis = %q, want it to name the email + API token requirement", msg)
	}
}

// --- finding 7: 403 cases ---

func TestDiagnoseAuthError_Cloud403NamesCredentials(t *testing.T) {
	err := &jira.APIError{StatusCode: 403, Body: "Forbidden"}
	msg := diagnoseAuthError(jira.ModeCloud, err)
	if !strings.Contains(msg, "email") || !strings.Contains(msg, "API token") {
		t.Errorf("Cloud 403 diagnosis = %q, want it to name the email + API token requirement", msg)
	}
	if !strings.Contains(msg, "403") {
		t.Errorf("Cloud 403 diagnosis = %q, want it to include the status code", msg)
	}
}

func TestDiagnoseAuthError_DataCenter403NamesPAT(t *testing.T) {
	err := &jira.APIError{StatusCode: 403, Body: "Forbidden"}
	msg := diagnoseAuthError(jira.ModeDataCenter, err)
	if !strings.Contains(msg, "PAT") && !strings.Contains(msg, "personal access token") {
		t.Errorf("Data Center 403 diagnosis = %q, want it to point at the PAT", msg)
	}
	if strings.Contains(msg, "email") {
		t.Errorf("Data Center 403 diagnosis = %q, must NOT mention email", msg)
	}
	if !strings.Contains(msg, "403") {
		t.Errorf("Data Center 403 diagnosis = %q, want it to include the status code", msg)
	}
}

func TestDiagnoseProjectError_403NamesKeyAndPermission(t *testing.T) {
	err := &jira.APIError{StatusCode: 403, Body: "Forbidden"}
	msg := diagnoseProjectError("MYPROJ", err)
	if !strings.Contains(msg, "MYPROJ") {
		t.Errorf("project 403 diagnosis = %q, want it to name the project key MYPROJ", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "permission") {
		t.Errorf("project 403 diagnosis = %q, want it to mention the permission possibility", msg)
	}
	if !strings.Contains(msg, "403") {
		t.Errorf("project 403 diagnosis = %q, want it to include the status code", msg)
	}
}

func TestDiagnoseAuthError_DataCenter401NamesPAT(t *testing.T) {
	err := &jira.APIError{StatusCode: 401, Body: "Unauthorized"}
	msg := diagnoseAuthError(jira.ModeDataCenter, err)
	if !strings.Contains(msg, "PAT") && !strings.Contains(msg, "personal access token") {
		t.Errorf("Data Center 401 diagnosis = %q, want it to point at the PAT", msg)
	}
	if strings.Contains(msg, "email") {
		t.Errorf("Data Center 401 diagnosis = %q, must NOT mention email", msg)
	}
}

func TestDiagnoseAuthError_NonAPIErrorPassesThrough(t *testing.T) {
	err := errors.New("connection refused")
	msg := diagnoseAuthError(jira.ModeCloud, err)
	if !strings.Contains(msg, "connection refused") {
		t.Errorf("diagnosis = %q, want it to include the raw error", msg)
	}
}

func TestDiagnoseProjectError_404NamesKey(t *testing.T) {
	err := &jira.APIError{StatusCode: 404, Body: "No project could be found"}
	msg := diagnoseProjectError("ACME", err)
	if !strings.Contains(msg, "ACME") {
		t.Errorf("project diagnosis = %q, want it to name the project key ACME", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "permission") {
		t.Errorf("project diagnosis = %q, want it to mention the permission possibility", msg)
	}
}

// fakePreflightClient records probe calls and returns configured errors.
type fakePreflightClient struct {
	authErr    error
	projectErr error
	authCalls  int
	projCalls  int
	lastProj   string
}

func (f *fakePreflightClient) CheckAuth(_ context.Context) error {
	f.authCalls++
	return f.authErr
}

func (f *fakePreflightClient) CheckProjectAccess(_ context.Context, projectKey string) error {
	f.projCalls++
	f.lastProj = projectKey
	return f.projectErr
}

func TestRunPreflight_AuthFailureAbortsBeforeProjectProbe(t *testing.T) {
	fake := &fakePreflightClient{authErr: &jira.APIError{StatusCode: 401, Body: "no"}}
	err := runPreflight(context.Background(), fake, jira.ModeCloud, "PROJ")
	if err == nil {
		t.Fatal("runPreflight: want error when auth fails, got nil")
	}
	if fake.authCalls != 1 {
		t.Errorf("authCalls = %d, want 1", fake.authCalls)
	}
	if fake.projCalls != 0 {
		t.Errorf("projCalls = %d, want 0 (project probe must not run after auth fails)", fake.projCalls)
	}
	if !strings.Contains(err.Error(), "email") {
		t.Errorf("error = %q, want Cloud auth diagnosis naming email", err.Error())
	}
}

func TestRunPreflight_SuccessRunsBothProbes(t *testing.T) {
	fake := &fakePreflightClient{}
	if err := runPreflight(context.Background(), fake, jira.ModeDataCenter, "PROJ"); err != nil {
		t.Fatalf("runPreflight: %v", err)
	}
	if fake.authCalls != 1 || fake.projCalls != 1 {
		t.Errorf("authCalls=%d projCalls=%d, want 1 and 1", fake.authCalls, fake.projCalls)
	}
	if fake.lastProj != "PROJ" {
		t.Errorf("project probed = %q, want PROJ", fake.lastProj)
	}
}

func TestRunPreflight_EmptyProjectKeySkipsProjectProbe(t *testing.T) {
	fake := &fakePreflightClient{}
	if err := runPreflight(context.Background(), fake, jira.ModeDataCenter, ""); err != nil {
		t.Fatalf("runPreflight: %v", err)
	}
	if fake.authCalls != 1 {
		t.Errorf("authCalls = %d, want 1", fake.authCalls)
	}
	if fake.projCalls != 0 {
		t.Errorf("projCalls = %d, want 0 (no project key → skip project probe)", fake.projCalls)
	}
}

func TestRunPreflight_ProjectFailureIsDiagnosed(t *testing.T) {
	fake := &fakePreflightClient{projectErr: &jira.APIError{StatusCode: 404, Body: "not found"}}
	err := runPreflight(context.Background(), fake, jira.ModeDataCenter, "ACME")
	if err == nil {
		t.Fatal("runPreflight: want error when project access fails, got nil")
	}
	if !strings.Contains(err.Error(), "ACME") {
		t.Errorf("error = %q, want it to name the project key ACME", err.Error())
	}
}

// --- finding 6: projectKeyFromConfig tests ---

func TestProjectKeyFromConfig_EmptyConfigFile_ReturnsEmpty(t *testing.T) {
	origCfg := configFile
	t.Cleanup(func() { configFile = origCfg })
	configFile = ""

	key, err := projectKeyFromConfig()
	if err != nil {
		t.Fatalf("projectKeyFromConfig with empty configFile: unexpected error: %v", err)
	}
	if key != "" {
		t.Errorf("key = %q, want empty string when configFile is not set", key)
	}
}

func TestProjectKeyFromConfig_UnreadablePath_ReturnsError(t *testing.T) {
	origCfg := configFile
	t.Cleanup(func() { configFile = origCfg })
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")

	_, err := projectKeyFromConfig()
	if err == nil {
		t.Fatal("projectKeyFromConfig: want error for unreadable path, got nil")
	}
}

func TestProjectKeyFromConfig_ValidYAML_ReturnsProjectKey(t *testing.T) {
	origCfg := configFile
	t.Cleanup(func() { configFile = origCfg })

	content := `schemaVersion: "1.0"
defaults:
  projectKey: MYPROJECT
issues: []
`
	tmp := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmp, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	configFile = tmp

	key, err := projectKeyFromConfig()
	if err != nil {
		t.Fatalf("projectKeyFromConfig: unexpected error: %v", err)
	}
	if key != "MYPROJECT" {
		t.Errorf("key = %q, want MYPROJECT", key)
	}
}

// --- finding 6: runPreflightSteps tests (shared step engine) ---

func TestRunPreflightSteps_AuthPassNoProject_CallbackAuthOnly(t *testing.T) {
	fake := &fakePreflightClient{}
	var steps []string
	var errs []error
	err := runPreflightSteps(context.Background(), fake, jira.ModeDataCenter, "", func(step string, e error) {
		steps = append(steps, step)
		errs = append(errs, e)
	})
	if err != nil {
		t.Fatalf("runPreflightSteps: unexpected error: %v", err)
	}
	if len(steps) != 1 || steps[0] != "auth" {
		t.Errorf("steps = %v, want [auth]", steps)
	}
	if errs[0] != nil {
		t.Errorf("auth step error = %v, want nil", errs[0])
	}
	if fake.projCalls != 0 {
		t.Errorf("projCalls = %d, want 0 when projectKey is empty", fake.projCalls)
	}
}

func TestRunPreflightSteps_AuthPassProjectPass_BothCallbacks(t *testing.T) {
	fake := &fakePreflightClient{}
	var steps []string
	err := runPreflightSteps(context.Background(), fake, jira.ModeDataCenter, "DEMO", func(step string, e error) {
		steps = append(steps, step)
	})
	if err != nil {
		t.Fatalf("runPreflightSteps: unexpected error: %v", err)
	}
	if len(steps) != 2 || steps[0] != "auth" || steps[1] != "project" {
		t.Errorf("steps = %v, want [auth project]", steps)
	}
}

func TestRunPreflightSteps_AuthFail_CallbackReceivesError(t *testing.T) {
	apiErr := &jira.APIError{StatusCode: 401, Body: "no"}
	fake := &fakePreflightClient{authErr: apiErr}
	var callbackErr error
	err := runPreflightSteps(context.Background(), fake, jira.ModeCloud, "PROJ", func(step string, e error) {
		if step == "auth" {
			callbackErr = e
		}
	})
	if err == nil {
		t.Fatal("runPreflightSteps: want error on auth failure, got nil")
	}
	if callbackErr == nil {
		t.Fatal("callback: want non-nil error for auth failure")
	}
	if !strings.Contains(callbackErr.Error(), "email") {
		t.Errorf("callback error = %q, want Cloud auth diagnosis naming email", callbackErr.Error())
	}
	// project callback must NOT fire
	if fake.projCalls != 0 {
		t.Errorf("projCalls = %d, want 0 when auth fails", fake.projCalls)
	}
}

// --- runCheck: platform output ---

// newMyselfServer answers paths ending in /myself with 200 {} and everything
// else with 404.
func newMyselfServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/myself") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRunCheck_PrintsSourceAndCustomDomainAPIBase(t *testing.T) {
	resetAuthGlobals(t)
	clearJiraEnv(t)
	server := newMyselfServer(t)

	configFile = ""
	jiraEmail = "user@acme.com"
	jiraToken = "tok"
	resolvedPlatform = jira.Platform{
		Mode:          jira.ModeCloud,
		Source:        jira.SourceServerInfo,
		ConfiguredURL: "https://jira.custom.example",
		APIBaseURL:    server.URL,
	}

	var err error
	_, stderr := captureOutput(t, func() { err = runCheck() })
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	for _, want := range []string{
		"Platform mode: Cloud (serverInfo)",
		"API base: " + server.URL + " (custom domain jira.custom.example)",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
	}
}

func TestRunCheck_NoAPIBaseLineWithoutCustomDomain(t *testing.T) {
	resetAuthGlobals(t)
	clearJiraEnv(t)
	server := newMyselfServer(t)

	configFile = ""
	jiraEmail = "user@acme.com"
	jiraToken = "tok"
	resolvedPlatform = jira.Platform{
		Mode:          jira.ModeDataCenter,
		Source:        jira.SourceServerInfo,
		ConfiguredURL: server.URL,
		APIBaseURL:    server.URL,
	}

	var err error
	_, stderr := captureOutput(t, func() { err = runCheck() })
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	if !strings.Contains(stderr, "Platform mode: Data Center (serverInfo)") {
		t.Errorf("stderr = %q, want it to contain %q", stderr, "Platform mode: Data Center (serverInfo)")
	}
	if strings.Contains(stderr, "API base:") {
		t.Errorf("stderr = %q, must not print an API base line without a custom domain", stderr)
	}
}
