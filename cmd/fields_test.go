package cmd

import (
	"strings"
	"testing"

	"github.com/kengou/jira-ticket-creator/internal/jira"
)

// --- validateFieldsFlags ---

func TestValidateFieldsFlags_AllPresent(t *testing.T) {
	origURL := jiraURL
	origToken := jiraToken
	defer func() {
		jiraURL = origURL
		jiraToken = origToken
	}()

	jiraURL = "https://jira.example.com"
	jiraToken = "my-token"

	if err := validateFieldsFlags(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateFieldsFlags_MissingURL(t *testing.T) {
	origURL := jiraURL
	origToken := jiraToken
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_TOKEN", "")
	defer func() {
		jiraURL = origURL
		jiraToken = origToken
	}()

	jiraURL = ""
	jiraToken = "my-token"

	if err := validateFieldsFlags(); err == nil {
		t.Fatal("expected error for missing URL")
	}
}

func TestValidateFieldsFlags_MissingToken(t *testing.T) {
	origURL := jiraURL
	origToken := jiraToken
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_TOKEN", "")
	defer func() {
		jiraURL = origURL
		jiraToken = origToken
	}()

	jiraURL = "https://jira.example.com"
	jiraToken = ""

	if err := validateFieldsFlags(); err == nil {
		t.Fatal("expected error for missing token")
	}
}

// --- matchFields ---

func TestMatchFields_NoFilter(t *testing.T) {
	origSearch := fieldSearch
	origCustom := fieldCustom
	defer func() {
		fieldSearch = origSearch
		fieldCustom = origCustom
	}()

	fieldSearch = ""
	fieldCustom = false

	fields := []jira.Field{
		{ID: "summary", Name: "Summary", Custom: false},
		{ID: "customfield_10009", Name: "Epic Link", Custom: true},
		{ID: "priority", Name: "Priority", Custom: false},
	}

	matched := matchFields(fields)
	if len(matched) != 3 {
		t.Errorf("len(matched) = %d, want 3", len(matched))
	}
}

func TestMatchFields_SearchFilter(t *testing.T) {
	origSearch := fieldSearch
	origCustom := fieldCustom
	defer func() {
		fieldSearch = origSearch
		fieldCustom = origCustom
	}()

	fieldSearch = "epic"
	fieldCustom = false

	fields := []jira.Field{
		{ID: "summary", Name: "Summary", Custom: false},
		{ID: "customfield_10009", Name: "Epic Link", Custom: true},
		{ID: "customfield_10010", Name: "Epic Name", Custom: true},
		{ID: "priority", Name: "Priority", Custom: false},
	}

	matched := matchFields(fields)
	if len(matched) != 2 {
		t.Fatalf("len(matched) = %d, want 2", len(matched))
	}
	if matched[0].ID != "customfield_10009" {
		t.Errorf("matched[0].ID = %q, want customfield_10009", matched[0].ID)
	}
	if matched[1].ID != "customfield_10010" {
		t.Errorf("matched[1].ID = %q, want customfield_10010", matched[1].ID)
	}
}

func TestMatchFields_SearchCaseInsensitive(t *testing.T) {
	origSearch := fieldSearch
	origCustom := fieldCustom
	defer func() {
		fieldSearch = origSearch
		fieldCustom = origCustom
	}()

	fieldSearch = "EPIC"
	fieldCustom = false

	fields := []jira.Field{
		{ID: "customfield_10009", Name: "Epic Link", Custom: true},
		{ID: "summary", Name: "Summary", Custom: false},
	}

	matched := matchFields(fields)
	if len(matched) != 1 {
		t.Fatalf("len(matched) = %d, want 1", len(matched))
	}
	if matched[0].ID != "customfield_10009" {
		t.Errorf("matched[0].ID = %q, want customfield_10009", matched[0].ID)
	}
}

func TestMatchFields_CustomOnly(t *testing.T) {
	origSearch := fieldSearch
	origCustom := fieldCustom
	defer func() {
		fieldSearch = origSearch
		fieldCustom = origCustom
	}()

	fieldSearch = ""
	fieldCustom = true

	fields := []jira.Field{
		{ID: "summary", Name: "Summary", Custom: false},
		{ID: "customfield_10009", Name: "Epic Link", Custom: true},
		{ID: "priority", Name: "Priority", Custom: false},
		{ID: "customfield_10010", Name: "Epic Name", Custom: true},
	}

	matched := matchFields(fields)
	if len(matched) != 2 {
		t.Fatalf("len(matched) = %d, want 2", len(matched))
	}
	if matched[0].ID != "customfield_10009" {
		t.Errorf("matched[0].ID = %q, want customfield_10009", matched[0].ID)
	}
	if matched[1].ID != "customfield_10010" {
		t.Errorf("matched[1].ID = %q, want customfield_10010", matched[1].ID)
	}
}

func TestMatchFields_SearchAndCustomOnly(t *testing.T) {
	origSearch := fieldSearch
	origCustom := fieldCustom
	defer func() {
		fieldSearch = origSearch
		fieldCustom = origCustom
	}()

	fieldSearch = "epic"
	fieldCustom = true

	fields := []jira.Field{
		{ID: "summary", Name: "Summary", Custom: false},
		{ID: "customfield_10009", Name: "Epic Link", Custom: true},
		{ID: "priority", Name: "Priority", Custom: false},
		{ID: "customfield_99999", Name: "Some Other Custom", Custom: true},
	}

	matched := matchFields(fields)
	if len(matched) != 1 {
		t.Fatalf("len(matched) = %d, want 1", len(matched))
	}
	if matched[0].ID != "customfield_10009" {
		t.Errorf("matched[0].ID = %q, want customfield_10009", matched[0].ID)
	}
}

func TestMatchFields_NoResults(t *testing.T) {
	origSearch := fieldSearch
	origCustom := fieldCustom
	defer func() {
		fieldSearch = origSearch
		fieldCustom = origCustom
	}()

	fieldSearch = "nonexistent"
	fieldCustom = false

	fields := []jira.Field{
		{ID: "summary", Name: "Summary", Custom: false},
		{ID: "priority", Name: "Priority", Custom: false},
	}

	matched := matchFields(fields)
	if len(matched) != 0 {
		t.Errorf("len(matched) = %d, want 0", len(matched))
	}
}

func TestMatchFields_EmptyInput(t *testing.T) {
	origSearch := fieldSearch
	origCustom := fieldCustom
	defer func() {
		fieldSearch = origSearch
		fieldCustom = origCustom
	}()

	fieldSearch = ""
	fieldCustom = false

	matched := matchFields(nil)
	if len(matched) != 0 {
		t.Errorf("len(matched) = %d, want 0 for nil input", len(matched))
	}
}

func TestRunFields_FromLineNamesAPIBaseViaConfiguredHost(t *testing.T) {
	resetAuthGlobals(t)
	clearJiraEnv(t)
	server := newBadRequestServer(t)

	jiraToken = "tok"
	jiraEmail = "user@acme.com"
	resolvedPlatform = jira.Platform{Mode: jira.ModeCloud, Source: jira.SourceServerInfo, ConfiguredURL: "https://jira.custom.example", APIBaseURL: server.URL}

	var err error
	stdout, _ := captureOutput(t, func() { err = runFields() })
	if err == nil {
		t.Fatal("expected error from 400 response")
	}
	want := "Fetching fields from " + server.URL + " (via jira.custom.example)..."
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout, want)
	}
}
