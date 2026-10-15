package cmd

import (
	"strings"
	"testing"

	"github.com/kengou/jira-ticket-creator/internal/jira"
)

func TestValidateLinkTypesFlags_NoURL(t *testing.T) {
	origURL := jiraURL
	origToken := jiraToken
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_TOKEN", "")
	defer func() { jiraURL = origURL; jiraToken = origToken }()

	jiraURL = ""
	jiraToken = "tok"

	if err := validateLinkTypesFlags(); err == nil {
		t.Error("expected error for missing URL")
	}
}

func TestValidateLinkTypesFlags_NoToken(t *testing.T) {
	origURL := jiraURL
	origToken := jiraToken
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_TOKEN", "")
	defer func() { jiraURL = origURL; jiraToken = origToken }()

	jiraURL = testJiraURL
	jiraToken = ""

	if err := validateLinkTypesFlags(); err == nil {
		t.Error("expected error for missing token")
	}
}

func TestValidateLinkTypesFlags_OK(t *testing.T) {
	origURL := jiraURL
	origToken := jiraToken
	defer func() { jiraURL = origURL; jiraToken = origToken }()

	jiraURL = testJiraURL
	jiraToken = "tok"

	if err := validateLinkTypesFlags(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMatchLinkTypes_NoFilter(t *testing.T) {
	types := []jira.IssueLinkTypeInfo{
		{ID: "1", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"},
		{ID: "2", Name: "Duplicate", Inward: "is duplicated by", Outward: "duplicates"},
	}
	got := matchLinkTypes(types, "")
	if len(got) != 2 {
		t.Errorf("got %d results, want 2", len(got))
	}
}

func TestMatchLinkTypes_ByName(t *testing.T) {
	types := []jira.IssueLinkTypeInfo{
		{ID: "1", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"},
		{ID: "2", Name: "Duplicate", Inward: "is duplicated by", Outward: "duplicates"},
	}
	got := matchLinkTypes(types, "block")
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	if got[0].Name != "Blocks" {
		t.Errorf("got %q, want Blocks", got[0].Name)
	}
}

func TestMatchLinkTypes_ByInward(t *testing.T) {
	types := []jira.IssueLinkTypeInfo{
		{ID: "1", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"},
		{ID: "2", Name: "Duplicate", Inward: "is duplicated by", Outward: "duplicates"},
	}
	got := matchLinkTypes(types, "duplicated")
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	if got[0].Name != "Duplicate" {
		t.Errorf("got %q, want Duplicate", got[0].Name)
	}
}

func TestMatchLinkTypes_CaseInsensitive(t *testing.T) {
	types := []jira.IssueLinkTypeInfo{
		{ID: "1", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"},
	}
	got := matchLinkTypes(types, "BLOCK")
	if len(got) != 1 {
		t.Errorf("got %d results, want 1", len(got))
	}
}

func TestMatchLinkTypes_NoMatch(t *testing.T) {
	types := []jira.IssueLinkTypeInfo{
		{ID: "1", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"},
	}
	got := matchLinkTypes(types, "nonexistent")
	if len(got) != 0 {
		t.Errorf("got %d results, want 0", len(got))
	}
}

func TestRunLinkTypes_FromLineNamesAPIBaseViaConfiguredHost(t *testing.T) {
	resetAuthGlobals(t)
	clearJiraEnv(t)
	server := newBadRequestServer(t)

	jiraToken = "tok"
	jiraEmail = "user@acme.com"
	resolvedPlatform = jira.Platform{Mode: jira.ModeCloud, Source: jira.SourceServerInfo, ConfiguredURL: "https://jira.custom.example", APIBaseURL: server.URL}

	var err error
	stdout, _ := captureOutput(t, func() { err = runLinkTypes() })
	if err == nil {
		t.Fatal("expected error from 400 response")
	}
	want := "Fetching issue link types from " + server.URL + " (via jira.custom.example)..."
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout, want)
	}
}
