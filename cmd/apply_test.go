// SPDX-License-Identifier: Apache-2.0
package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kengou/jira-ticket-creator/internal/jira"
)

func TestRunApply_ReviewedMarkerSkippedWhenReviewedTrue(t *testing.T) {
	t.Chdir(t.TempDir())
	// A file without "# reviewed: false" should not trigger the review gate.
	content := `schemaVersion: "1.0"
defaults:
  projectKey: TEST
issues:
  - id: t1
    issueType: Task
    summary: "A task"
`
	tmp := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmp, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	// Save and restore globals.
	origCfg := configFile
	origDryRun := dryRun
	t.Cleanup(func() {
		configFile = origCfg
		dryRun = origDryRun
	})

	configFile = tmp
	dryRun = true

	err := runApply()
	// The function may fail downstream (e.g. schema validation, etc.) but
	// it must NOT fail with the review-gate message.
	if err != nil && strings.Contains(err.Error(), "reviewed") {
		t.Errorf("unexpected review gate error: %v", err)
	}
}

func TestRunApply_ReviewedMarkerGate_DryRun(t *testing.T) {
	t.Chdir(t.TempDir())
	// In dry-run mode the reviewed-marker gate prints a warning but does not
	// prompt, so the function should get past the gate.
	content := `# ai-generated: true
# reviewed: false
schemaVersion: "1.0"
defaults:
  projectKey: TEST
issues:
  - id: t1
    issueType: Task
    summary: "A task"
`
	tmp := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmp, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	origCfg := configFile
	origDryRun := dryRun
	t.Cleanup(func() {
		configFile = origCfg
		dryRun = origDryRun
	})

	configFile = tmp
	dryRun = true

	err := runApply()
	// The function must NOT fail with the review-gate abort.
	if err != nil && strings.Contains(err.Error(), "aborted") {
		t.Errorf("dry-run should not abort on review gate: %v", err)
	}
}

func TestRunApply_SchemaValidationFailsOnInvalidYAML(t *testing.T) {
	t.Chdir(t.TempDir())
	// A file with an extra unknown top-level key should fail schema validation.
	content := `schemaVersion: "1.0"
defaults:
  projectKey: TEST
unknownTopLevelKey: bad
issues:
  - id: t1
    issueType: Task
    summary: "A task"
`
	tmp := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmp, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	origCfg := configFile
	origDryRun := dryRun
	t.Cleanup(func() {
		configFile = origCfg
		dryRun = origDryRun
	})

	configFile = tmp
	dryRun = true

	err := runApply()
	if err == nil {
		t.Fatal("expected schema validation error, got nil")
	}
	if !strings.Contains(err.Error(), "schema validation") {
		t.Errorf("expected 'schema validation' in error, got: %v", err)
	}
}

// --- apply preflight (non-dry-run) ---

func writeApplyConfig(t *testing.T, projectKey string) string {
	t.Helper()
	content := `schemaVersion: "1.0"
defaults:
  projectKey: ` + projectKey + `
issues:
  - id: TASK-1
    issueType: Task
    summary: "A task"
`
	tmp := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmp, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return tmp
}

func resetApplyGlobals(t *testing.T) {
	t.Helper()
	origCfg, origDry, origURL, origToken, origEmail, origCloud, origYes := configFile, dryRun, jiraURL, jiraToken, jiraEmail, isCloud, applyYes
	origPlatform := resolvedPlatform
	t.Cleanup(func() {
		configFile, dryRun, jiraURL, jiraToken, jiraEmail, isCloud, applyYes = origCfg, origDry, origURL, origToken, origEmail, origCloud, origYes
		resolvedPlatform = origPlatform
	})
}

func TestRunApply_PreflightFailureAbortsBeforeCreate(t *testing.T) {
	t.Chdir(t.TempDir())
	resetApplyGlobals(t)
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_TOKEN", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_CLOUD", "")

	var createHits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/issue") {
			createHits++
			w.WriteHeader(http.StatusCreated)
			if _, err := w.Write([]byte(`{"key":"X-1"}`)); err != nil {
				t.Errorf("write: %v", err)
			}
			return
		}
		// /myself auth probe is rejected.
		w.WriteHeader(http.StatusUnauthorized)
		if _, err := w.Write([]byte(`{"errorMessages":["Unauthorized"]}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer server.Close()

	configFile = writeApplyConfig(t, "PROJ")
	dryRun = false
	applyYes = true
	jiraURL = server.URL
	jiraToken = "bad"
	jiraEmail = ""
	isCloud = false
	resolvedPlatform = jira.Platform{Mode: jira.ModeDataCenter, Source: jira.SourceServerInfo, ConfiguredURL: server.URL, APIBaseURL: server.URL}

	err := runApply()
	if err == nil {
		t.Fatal("runApply: want preflight error against rejecting server, got nil")
	}
	if createHits != 0 {
		t.Errorf("no issue must be created when preflight fails; observed %d create requests", createHits)
	}
	if !strings.Contains(err.Error(), "PAT") && !strings.Contains(err.Error(), "personal access token") {
		t.Errorf("error = %q, want Data Center auth diagnosis naming the PAT", err.Error())
	}
}

func TestRunApply_PreflightPassesThenCreates(t *testing.T) {
	t.Chdir(t.TempDir())
	resetApplyGlobals(t)
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_TOKEN", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_CLOUD", "")

	var createHits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/issue"):
			createHits++
			w.WriteHeader(http.StatusCreated)
			if _, err := w.Write([]byte(`{"key":"PROJ-2"}`)); err != nil {
				t.Errorf("write: %v", err)
			}
		case strings.HasSuffix(r.URL.Path, "/myself"):
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte(`{"name":"user"}`)); err != nil {
				t.Errorf("write: %v", err)
			}
		case strings.Contains(r.URL.Path, "/project/"):
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte(`{"key":"PROJ"}`)); err != nil {
				t.Errorf("write: %v", err)
			}
		default:
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte(`{}`)); err != nil {
				t.Errorf("write: %v", err)
			}
		}
	}))
	defer server.Close()

	// Use a unique temp config with unique issue ID to avoid state file collision
	configContent := `schemaVersion: "1.0"
defaults:
  projectKey: PROJ
issues:
  - id: PASS-TEST-1
    issueType: Task
    summary: "A task"
`
	tmp := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmp, []byte(configContent), 0600); err != nil {
		t.Fatal(err)
	}
	configFile = tmp

	dryRun = false
	applyYes = true
	jiraURL = server.URL
	jiraToken = "good"
	jiraEmail = ""
	isCloud = false
	resolvedPlatform = jira.Platform{Mode: jira.ModeDataCenter, Source: jira.SourceServerInfo, ConfiguredURL: server.URL, APIBaseURL: server.URL}

	if err := runApply(); err != nil {
		t.Fatalf("runApply after passing preflight: %v", err)
	}
	if createHits < 1 {
		t.Errorf("expected at least 1 create after passing preflight, got %d", createHits)
	}
}

func TestRunApply_DryRunMakesNoNetworkCalls(t *testing.T) {
	t.Chdir(t.TempDir())
	resetApplyGlobals(t)

	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer server.Close()

	configFile = writeApplyConfig(t, "PROJ")
	dryRun = true
	jiraURL = server.URL
	jiraToken = "tok"

	if err := runApply(); err != nil {
		t.Fatalf("runApply dry-run: %v", err)
	}
	if hits != 0 {
		t.Errorf("dry-run must make no network calls; observed %d requests", hits)
	}
}

func TestRunApply_TargetLineNamesAPIBaseViaConfiguredHost(t *testing.T) {
	t.Chdir(t.TempDir())
	resetApplyGlobals(t)
	clearJiraEnv(t)

	var createHits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/issue") {
			createHits.Add(1)
			w.WriteHeader(http.StatusCreated)
			if _, err := w.Write([]byte(`{"key":"X-1"}`)); err != nil {
				t.Errorf("write: %v", err)
			}
			return
		}
		// /myself auth probe is rejected.
		w.WriteHeader(http.StatusUnauthorized)
		if _, err := w.Write([]byte(`{"errorMessages":["Unauthorized"]}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer server.Close()

	configFile = writeApplyConfig(t, "PROJ")
	dryRun = false
	applyYes = true
	jiraToken = "tok"
	jiraEmail = "user@acme.com"
	isCloud = false
	resolvedPlatform = jira.Platform{
		Mode:          jira.ModeCloud,
		Source:        jira.SourceServerInfo,
		ConfiguredURL: "https://jira.custom.example",
		APIBaseURL:    server.URL,
	}

	var err error
	_, stderr := captureOutput(t, func() { err = runApply() })
	if err == nil {
		t.Fatal("runApply: want preflight error against rejecting server, got nil")
	}
	if want := "Target: " + server.URL + " (via jira.custom.example)"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
	if n := createHits.Load(); n != 0 {
		t.Errorf("no issue must be created when preflight fails; observed %d create requests", n)
	}
}
