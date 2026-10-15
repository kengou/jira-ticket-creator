package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/kengou/jira-ticket-creator/internal/jira"
)

const (
	testJiraURL    = "https://jira.example.com"
	testJiraToken  = "my-token"
	testProjectKey = "PROJ"
)

// --- validateEpicsFlags ---

func TestValidateEpicsFlags_AllPresent(t *testing.T) {
	origURL := jiraURL
	origToken := jiraToken
	origProject := projectKey
	origStatus := epicStatus
	defer func() {
		jiraURL = origURL
		jiraToken = origToken
		projectKey = origProject
		epicStatus = origStatus
	}()

	jiraURL = testJiraURL
	jiraToken = testJiraToken
	projectKey = testProjectKey
	epicStatus = ""

	if err := validateEpicsFlags(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateEpicsFlags_WithStatus(t *testing.T) {
	origURL := jiraURL
	origToken := jiraToken
	origProject := projectKey
	origStatus := epicStatus
	defer func() {
		jiraURL = origURL
		jiraToken = origToken
		projectKey = origProject
		epicStatus = origStatus
	}()

	jiraURL = testJiraURL
	jiraToken = testJiraToken
	projectKey = testProjectKey
	epicStatus = "In Progress"

	if err := validateEpicsFlags(); err != nil {
		t.Fatalf("unexpected error with status set: %v", err)
	}
}

func TestValidateEpicsFlags_MissingURL(t *testing.T) {
	origURL := jiraURL
	origToken := jiraToken
	origProject := projectKey
	origStatus := epicStatus
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_TOKEN", "")
	defer func() {
		jiraURL = origURL
		jiraToken = origToken
		projectKey = origProject
		epicStatus = origStatus
	}()

	jiraURL = ""
	jiraToken = testJiraToken
	projectKey = testProjectKey

	if err := validateEpicsFlags(); err == nil {
		t.Fatal("expected error for missing URL")
	}
}

func TestValidateEpicsFlags_MissingToken(t *testing.T) {
	origURL := jiraURL
	origToken := jiraToken
	origProject := projectKey
	origStatus := epicStatus
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_TOKEN", "")
	defer func() {
		jiraURL = origURL
		jiraToken = origToken
		projectKey = origProject
		epicStatus = origStatus
	}()

	jiraURL = testJiraURL
	jiraToken = ""
	projectKey = testProjectKey

	if err := validateEpicsFlags(); err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestValidateEpicsFlags_MissingProjectKey(t *testing.T) {
	origURL := jiraURL
	origToken := jiraToken
	origProject := projectKey
	origStatus := epicStatus
	defer func() {
		jiraURL = origURL
		jiraToken = origToken
		projectKey = origProject
		epicStatus = origStatus
	}()

	jiraURL = testJiraURL
	jiraToken = testJiraToken
	projectKey = ""

	if err := validateEpicsFlags(); err == nil {
		t.Fatal("expected error for missing project key")
	}
}

func TestValidateEpicsFlags_RejectsConfigFile(t *testing.T) {
	origURL := jiraURL
	origToken := jiraToken
	origProject := projectKey
	origConfigFile := configFile
	defer func() {
		jiraURL = origURL
		jiraToken = origToken
		projectKey = origProject
		configFile = origConfigFile
	}()

	jiraURL = testJiraURL
	jiraToken = testJiraToken
	projectKey = testProjectKey
	configFile = "tickets/epics-new.yaml"

	err := validateEpicsFlags()
	if err == nil {
		t.Fatal("expected error when -f/--file is passed to epics")
	}
	if !strings.Contains(err.Error(), "--output") {
		t.Errorf("error should point to --output, got: %v", err)
	}
}

// --- epicsToYAML ---

func TestEpicsToYAML_BasicOutput(t *testing.T) {
	epics := []jira.Epic{
		{Key: "PROJ-1", Summary: "Epic One", Status: "Open", Description: "Description of epic one."},
		{Key: "PROJ-2", Summary: "Epic Two", Status: "In Progress", Description: "Description of epic two."},
	}

	data, err := epicsToYAML(testProjectKey, epics)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := string(data)

	// Verify schemaVersion
	if !strings.Contains(output, "schemaVersion: \"1.0\"") {
		t.Error("expected schemaVersion: \"1.0\" in output")
	}

	// Verify defaults.projectKey
	if !strings.Contains(output, "projectKey: PROJ") {
		t.Error("expected projectKey: PROJ in output")
	}

	// Verify issues are present
	if !strings.Contains(output, "issueType: Epic") {
		t.Error("expected issueType: Epic in output")
	}

	// Verify descriptions are included
	if !strings.Contains(output, "Description of epic one.") {
		t.Error("expected description of epic one in output")
	}
	if !strings.Contains(output, "Description of epic two.") {
		t.Error("expected description of epic two in output")
	}

	// Verify it unmarshals back correctly
	var cfg epicsYAMLConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to unmarshal generated YAML: %v", err)
	}

	if cfg.SchemaVersion != "1.0" {
		t.Errorf("schemaVersion = %q, want %q", cfg.SchemaVersion, "1.0")
	}
	if cfg.Defaults.ProjectKey != testProjectKey {
		t.Errorf("defaults.projectKey = %q, want %q", cfg.Defaults.ProjectKey, "PROJ")
	}
	if len(cfg.Issues) != 2 {
		t.Fatalf("len(issues) = %d, want 2", len(cfg.Issues))
	}
	if cfg.Issues[0].ID != "PROJ-1" {
		t.Errorf("issues[0].id = %q, want %q", cfg.Issues[0].ID, "PROJ-1")
	}
	if cfg.Issues[0].EpicName != "Epic One" {
		t.Errorf("issues[0].epicName = %q, want %q", cfg.Issues[0].EpicName, "Epic One")
	}
	if cfg.Issues[0].Description != "Description of epic one." {
		t.Errorf("issues[0].description = %q, want %q", cfg.Issues[0].Description, "Description of epic one.")
	}
	if cfg.Issues[1].ID != "PROJ-2" {
		t.Errorf("issues[1].id = %q, want %q", cfg.Issues[1].ID, "PROJ-2")
	}
}

func TestEpicsToYAML_EmptyEpics(t *testing.T) {
	data, err := epicsToYAML(testProjectKey, []jira.Epic{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cfg epicsYAMLConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to unmarshal generated YAML: %v", err)
	}

	if len(cfg.Issues) != 0 {
		t.Errorf("len(issues) = %d, want 0", len(cfg.Issues))
	}
	if cfg.Defaults.ProjectKey != testProjectKey {
		t.Errorf("defaults.projectKey = %q, want %q", cfg.Defaults.ProjectKey, "PROJ")
	}
}

func TestEpicsToYAML_NoDescription(t *testing.T) {
	epics := []jira.Epic{
		{Key: "PROJ-10", Summary: "Epic Without Description", Status: "Done"},
	}

	data, err := epicsToYAML(testProjectKey, epics)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := string(data)

	// Description field should be omitted when empty (omitempty)
	if strings.Contains(output, "description:") {
		t.Error("expected description field to be omitted when empty")
	}

	var cfg epicsYAMLConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to unmarshal generated YAML: %v", err)
	}

	if cfg.Issues[0].Description != "" {
		t.Errorf("issues[0].description = %q, want empty", cfg.Issues[0].Description)
	}
}

func TestEpicsToYAML_MultilineDescription(t *testing.T) {
	desc := "Line one.\nLine two.\nLine three."
	epics := []jira.Epic{
		{Key: "PROJ-5", Summary: "Epic With Multiline", Status: "Open", Description: desc},
	}

	data, err := epicsToYAML(testProjectKey, epics)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cfg epicsYAMLConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to unmarshal generated YAML: %v", err)
	}

	if cfg.Issues[0].Description != desc {
		t.Errorf("issues[0].description = %q, want %q", cfg.Issues[0].Description, desc)
	}
}

// newBadRequestServer answers every request with 400 so a fetch command fails
// fast, after its "from" line has been printed.
func newBadRequestServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		if _, err := w.Write([]byte(`{"errorMessages":["test"]}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRunEpics_FromLineNamesAPIBaseViaConfiguredHost(t *testing.T) {
	resetAuthGlobals(t)
	clearJiraEnv(t)
	server := newBadRequestServer(t)

	origProject, origStatus := projectKey, epicStatus
	t.Cleanup(func() { projectKey, epicStatus = origProject, origStatus })
	jiraToken = "tok"
	jiraEmail = "user@acme.com"
	resolvedPlatform = jira.Platform{Mode: jira.ModeCloud, Source: jira.SourceServerInfo, ConfiguredURL: "https://jira.custom.example", APIBaseURL: server.URL}
	projectKey = "PROJ"
	epicStatus = "NOT:Done"

	var err error
	_, stderr := captureOutput(t, func() { err = runEpics() })
	if err == nil {
		t.Fatal("expected error from 400 response")
	}
	want := "Fetching Epics for project PROJ (status: NOT:Done) from " + server.URL + " (via jira.custom.example)..."
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
}

func TestRunEpics_FromLineWithoutCustomDomain(t *testing.T) {
	resetAuthGlobals(t)
	clearJiraEnv(t)
	server := newBadRequestServer(t)

	origProject, origStatus := projectKey, epicStatus
	t.Cleanup(func() { projectKey, epicStatus = origProject, origStatus })
	jiraToken = "tok"
	jiraEmail = "user@acme.com"
	resolvedPlatform = jira.Platform{Mode: jira.ModeDataCenter, Source: jira.SourceServerInfo, ConfiguredURL: server.URL, APIBaseURL: server.URL}
	projectKey = "PROJ"
	epicStatus = ""

	var err error
	_, stderr := captureOutput(t, func() { err = runEpics() })
	if err == nil {
		t.Fatal("expected error from 400 response")
	}
	want := "Fetching Epics for project PROJ from " + server.URL + "..."
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
	if strings.Contains(stderr, "(via ") {
		t.Errorf("stderr = %q, must not contain \"(via \" without a custom domain", stderr)
	}
}
