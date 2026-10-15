package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testBaseURL = "https://jira.example.com"

// mustNewClient creates a Client for testing. All test URLs use https or loopback
// http (httptest.NewServer), so no error is expected; Fatalf on failure.
func mustNewClient(t *testing.T, baseURL, token string, isCloud bool, opts ...ClientOption) *Client {
	t.Helper()
	c, err := NewClient(baseURL, token, isCloud, opts...)
	if err != nil {
		t.Fatalf("NewClient(%q): %v", baseURL, err)
	}
	return c
}

// encodeJSON writes v as JSON to w and fails the test if encoding fails.
func encodeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("json.NewEncoder.Encode: %v", err)
	}
}

// writeBytes writes data to w and fails the test if the write fails.
func writeBytes(t *testing.T, w http.ResponseWriter, data []byte) {
	t.Helper()
	if _, err := w.Write(data); err != nil {
		t.Errorf("w.Write: %v", err)
	}
}

// --- NewClient ---

func TestNewClient_DataCenter(t *testing.T) {
	c := mustNewClient(t, testBaseURL, "my-token", false)
	if c.apiVersion != "2" {
		t.Errorf("apiVersion = %q, want %q for Data Center", c.apiVersion, "2")
	}
	if c.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", c.MaxRetries)
	}
	if c.RetryDelay != time.Second {
		t.Errorf("RetryDelay = %v, want 1s", c.RetryDelay)
	}
}

func TestNewClient_Cloud(t *testing.T) {
	c, err := NewClientWithMode("https://jira.atlassian.net", ModeCloud, Credentials{Email: "user@example.com", Token: "my-token"})
	if err != nil {
		t.Fatalf("NewClientWithMode: %v", err)
	}
	if c.apiVersion != "3" {
		t.Errorf("apiVersion = %q, want %q for Cloud", c.apiVersion, "3")
	}
}

func TestNewClient_WithOptions(t *testing.T) {
	c := mustNewClient(t, testBaseURL, "tok", false,
		WithMaxRetries(5),
		WithTimeout(60*time.Second),
	)
	if c.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d, want 5", c.MaxRetries)
	}
	if c.httpClient.Timeout != 60*time.Second {
		t.Errorf("Timeout = %v, want 60s", c.httpClient.Timeout)
	}
}

func TestNewClient_DefaultMaxRetryAfterSecs(t *testing.T) {
	c := mustNewClient(t, testBaseURL, "tok", false)
	if c.MaxRetryAfterSecs != 60 {
		t.Errorf("MaxRetryAfterSecs = %d, want 60 (default)", c.MaxRetryAfterSecs)
	}
}

func TestNewClient_WithMaxRetryAfterSecs(t *testing.T) {
	c := mustNewClient(t, testBaseURL, "tok", false, WithMaxRetryAfterSecs(120))
	if c.MaxRetryAfterSecs != 120 {
		t.Errorf("MaxRetryAfterSecs = %d, want 120", c.MaxRetryAfterSecs)
	}
}

func TestNewClient_RejectsHTTPNonLoopback(t *testing.T) {
	_, err := NewClient("http://jira.example.com", "token", false)
	if err == nil {
		t.Error("NewClient with http:// non-loopback expected error, got nil")
	}
}

// --- normalizeURL ---

func TestNormalizeURL_AddsScheme(t *testing.T) {
	got, err := normalizeURL("jira.example.com")
	if err != nil || got != testBaseURL {
		t.Errorf("normalizeURL('jira.example.com') = %q, %v; want %q, nil", got, err, testBaseURL)
	}
}

func TestNormalizeURL_PreservesHTTPS(t *testing.T) {
	got, err := normalizeURL(testBaseURL)
	if err != nil || got != testBaseURL {
		t.Errorf("normalizeURL('https://jira.example.com') = %q, %v", got, err)
	}
}

func TestNormalizeURL_RejectsHTTPNonLoopback(t *testing.T) {
	_, err := normalizeURL("http://jira.example.com")
	if err == nil {
		t.Error("normalizeURL('http://jira.example.com') expected error, got nil")
	}
}

func TestNormalizeURL_AllowsHTTPLoopback(t *testing.T) {
	got, err := normalizeURL("http://localhost:8080")
	if err != nil || got != "http://localhost:8080" {
		t.Errorf("normalizeURL('http://localhost:8080') = %q, %v; want 'http://localhost:8080', nil", got, err)
	}
}

func TestNormalizeURL_StripsTrailingSlash(t *testing.T) {
	got, err := normalizeURL(testBaseURL + "/")
	if err != nil || got != testBaseURL {
		t.Errorf("normalizeURL('https://jira.example.com/') = %q, %v", got, err)
	}
}

func TestNormalizeURL_StripsMultipleTrailingSlashes(t *testing.T) {
	got, err := normalizeURL(testBaseURL + "///")
	if err != nil || got != testBaseURL {
		t.Errorf("normalizeURL('https://jira.example.com///') = %q, %v", got, err)
	}
}

func TestNormalizeURL_NoSchemeWithTrailingSlash(t *testing.T) {
	got, err := normalizeURL("jira.example.com/")
	if err != nil || got != testBaseURL {
		t.Errorf("normalizeURL('jira.example.com/') = %q, %v", got, err)
	}
}

func TestNormalizeURL_EmptyString(t *testing.T) {
	got, err := normalizeURL("")
	if err != nil || got != "" {
		t.Errorf("normalizeURL('') = %q, %v; want empty, nil", got, err)
	}
}

func TestNormalizeURL_WhitespaceOnly(t *testing.T) {
	got, err := normalizeURL("  ")
	if err != nil || got != "" {
		t.Errorf("normalizeURL('  ') = %q, %v; want empty, nil", got, err)
	}
}

func TestNormalizeURL_TrimsWhitespace(t *testing.T) {
	got, err := normalizeURL("  jira.example.com  ")
	if err != nil || got != testBaseURL {
		t.Errorf("normalizeURL('  jira.example.com  ') = %q, %v", got, err)
	}
}

func TestNormalizeURL_WithPath(t *testing.T) {
	got, err := normalizeURL("jira.example.com/context-path")
	if err != nil || got != "https://jira.example.com/context-path" {
		t.Errorf("normalizeURL('jira.example.com/context-path') = %q, %v", got, err)
	}
}

func TestNormalizeURL_WithPort(t *testing.T) {
	got, err := normalizeURL("jira.example.com:8443")
	if err != nil || got != "https://jira.example.com:8443" {
		t.Errorf("normalizeURL('jira.example.com:8443') = %q, %v", got, err)
	}
}

// --- normalizeURL security regression tests ---

func TestNormalizeURL_StripsUserinfo(t *testing.T) {
	got, err := normalizeURL("https://admin:secret@jira.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != testBaseURL {
		t.Errorf("normalizeURL with userinfo = %q, want %q", got, testBaseURL)
	}
}

func TestNormalizeURL_StripsUserinfoWithPath(t *testing.T) {
	got, err := normalizeURL("https://user:pass@jira.example.com/context")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://jira.example.com/context" {
		t.Errorf("normalizeURL with userinfo+path = %q, want %q", got, "https://jira.example.com/context")
	}
}

func TestNormalizeURL_AllowedHosts_Enforced(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "jira.corp.com")
	_, err := normalizeURL("https://evil.example.com")
	if err == nil {
		t.Error("expected error for host not in JIRA_ALLOWED_HOSTS, got nil")
	}
}

func TestNormalizeURL_AllowedHosts_Passes(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "jira.corp.com")
	got, err := normalizeURL("https://jira.corp.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://jira.corp.com" {
		t.Errorf("normalizeURL = %q, want %q", got, "https://jira.corp.com")
	}
}

func TestNormalizeURL_AllowedHosts_CaseInsensitive(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "Jira.Corp.Com")
	_, err := normalizeURL("https://jira.corp.com")
	if err != nil {
		t.Errorf("expected case-insensitive match to pass, got error: %v", err)
	}
}

func TestNormalizeURL_AllowedHosts_MultipleHosts(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "jira.corp.com , jira2.corp.com")
	if _, err := normalizeURL("https://jira.corp.com"); err != nil {
		t.Errorf("first host should be allowed: %v", err)
	}
	if _, err := normalizeURL("https://jira2.corp.com"); err != nil {
		t.Errorf("second host should be allowed: %v", err)
	}
	if _, err := normalizeURL("https://evil.com"); err == nil {
		t.Error("unlisted host should be rejected")
	}
}

func TestNormalizeURL_AllowedHosts_NotSet_NoRestriction(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	_, err := normalizeURL("https://any.example.com")
	if err != nil {
		t.Errorf("unset JIRA_ALLOWED_HOSTS should not restrict: %v", err)
	}
}

func TestNormalizeURL_SchemeCaseNormalized(t *testing.T) {
	_, err := normalizeURL("HTTP://jira.example.com")
	if err == nil {
		t.Error("HTTP:// (uppercase) for non-loopback should be rejected after lowercasing scheme")
	}
}

func TestNormalizeURL_ParseErrorDoesNotEchoURL(t *testing.T) {
	// A *url.Error message carries the raw URL, so wrapping it would print a
	// password embedded in JIRA_URL. The parse error must echo no part of it.
	cases := []struct {
		name      string
		raw       string
		forbidden []string
	}{
		{"password_bad_port", "https://alice:S3CRET@jira.corp.example:badport", []string{"S3CRET", "alice", "jira.corp.example", "badport"}},
		// The unencoded '#' starts a fragment, so the parse error would leak the
		// password prefix as the port (":pa").
		{"unencoded_hash_in_password", "https://alice:pa#ss@jira.corp.example", []string{"alice", "pa#", ":pa", "jira.corp.example"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeURL(tc.raw)
			if err == nil {
				t.Fatal("normalizeURL returned nil error, want a parse error")
			}
			msg := err.Error()
			if !strings.Contains(msg, "invalid URL") {
				t.Errorf("error %q does not contain %q", msg, "invalid URL")
			}
			for _, s := range tc.forbidden {
				if strings.Contains(msg, s) {
					t.Errorf("error %q echoes %q from the URL", msg, s)
				}
			}
		})
	}
}

func TestNormalizeURL_HTTPSToHTTPRedirectBlocked(t *testing.T) {
	// Start an HTTP server that redirects to itself (same host, different scheme would
	// be caught by CheckRedirect). We simulate the redirect block by verifying that
	// the client's CheckRedirect rejects HTTPS→HTTP downgrades.
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This server just returns 200; the redirect is simulated below.
		w.WriteHeader(http.StatusOK)
	}))
	defer httpSrv.Close()

	// Create a server that redirects HTTPS→HTTP (simulated with two test servers)
	redirectSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpSrv.URL+"/redirect-target", http.StatusFound)
	}))
	defer redirectSrv.Close()

	c, err := NewClient(redirectSrv.URL, "test-token", false)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.MaxRetries = 0
	// Save the production CheckRedirect before replacing the HTTP client
	originalCheckRedirect := c.httpClient.CheckRedirect
	// Replace the HTTP client with one that trusts the test TLS cert
	c.httpClient = redirectSrv.Client()
	// Restore the production CheckRedirect
	c.httpClient.CheckRedirect = originalCheckRedirect

	err = c.doRequest(context.Background(), http.MethodGet, "/", nil, nil)
	if err == nil {
		t.Error("expected error on HTTPS→HTTP redirect, got nil")
	}
}

func TestNewClient_NormalizesURL(t *testing.T) {
	// Verify that NewClient applies normalizeURL to the base URL
	c := mustNewClient(t, "jira.example.com/", "token", false)
	if c.baseURL != testBaseURL {
		t.Errorf("NewClient baseURL = %q, want 'https://jira.example.com'", c.baseURL)
	}
}

// --- Format helpers ---

func TestFormatIssueType(t *testing.T) {
	got := FormatIssueType("Story")
	if got["name"] != "Story" {
		t.Errorf("FormatIssueType = %v, want name=Story", got)
	}
}

func TestFormatPriority(t *testing.T) {
	got := FormatPriority("High")
	if got["name"] != "High" {
		t.Errorf("FormatPriority = %v, want name=High", got)
	}
}

func TestFormatUser(t *testing.T) {
	got := FormatUser("jdoe")
	if got["name"] != "jdoe" {
		t.Errorf("FormatUser = %v, want name=jdoe", got)
	}
}

func TestFormatComponent(t *testing.T) {
	got := FormatComponent("backend")
	if got["name"] != "backend" {
		t.Errorf("FormatComponent = %v, want name=backend", got)
	}
}

func TestFormatVersion(t *testing.T) {
	got := FormatVersion("v1.0")
	if got["name"] != "v1.0" {
		t.Errorf("FormatVersion = %v, want name=v1.0", got)
	}
}

// --- BuildIssueFields ---

func TestBuildIssueFields_SetsProject(t *testing.T) {
	fields := BuildIssueFields("PROJ", map[string]any{
		"summary": "Test",
	})

	proj, ok := fields["project"].(map[string]any)
	if !ok {
		t.Fatalf("project field not a map: %T", fields["project"])
	}
	if proj["key"] != "PROJ" {
		t.Errorf("project.key = %v, want PROJ", proj["key"])
	}
	if fields["summary"] != "Test" {
		t.Errorf("summary = %v, want Test", fields["summary"])
	}
}

func TestBuildIssueFields_PreservesAllFields(t *testing.T) {
	input := map[string]any{
		"summary":   "Title",
		"issuetype": FormatIssueType("Bug"),
		"labels":    []string{"critical"},
	}
	got := BuildIssueFields("KEY", input)

	if got["summary"] != "Title" {
		t.Error("summary not preserved")
	}
	if _, ok := got["project"]; !ok {
		t.Error("project not set")
	}
	if _, ok := got["labels"]; !ok {
		t.Error("labels not preserved")
	}
}

// --- parseRetryAfter ---

func TestParseRetryAfter_Empty(t *testing.T) {
	c := &Client{}
	if got := c.parseRetryAfter(""); got != 1 {
		t.Errorf("parseRetryAfter('') = %d, want 1", got)
	}
}

func TestParseRetryAfter_Seconds(t *testing.T) {
	c := &Client{}
	if got := c.parseRetryAfter("30"); got != 30 {
		t.Errorf("parseRetryAfter('30') = %d, want 30", got)
	}
}

func TestParseRetryAfter_OutOfRange(t *testing.T) {
	c := &Client{} // MaxRetryAfterSecs == 0 → defaults to 60
	// Negative values fall back to 1
	if got := c.parseRetryAfter("-5"); got != 1 {
		t.Errorf("parseRetryAfter('-5') = %d, want 1 (fallback)", got)
	}
	// Values exceeding MaxRetryAfterSecs (60) are capped
	if got := c.parseRetryAfter("99999"); got != 60 {
		t.Errorf("parseRetryAfter('99999') = %d, want 60 (cap)", got)
	}
}

func TestParseRetryAfter_CustomCap(t *testing.T) {
	c := &Client{MaxRetryAfterSecs: 120}
	if got := c.parseRetryAfter("200"); got != 120 {
		t.Errorf("parseRetryAfter('200') with cap 120 = %d, want 120", got)
	}
	if got := c.parseRetryAfter("100"); got != 100 {
		t.Errorf("parseRetryAfter('100') with cap 120 = %d, want 100", got)
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	c := &Client{}
	// Future date
	future := time.Now().Add(30 * time.Second).UTC().Format(time.RFC1123)
	got := c.parseRetryAfter(future)
	// Allow some tolerance (28-32 seconds)
	if got < 28 || got > 32 {
		t.Errorf("parseRetryAfter(future) = %d, want ~30", got)
	}
}

func TestParseRetryAfter_PastHTTPDate(t *testing.T) {
	c := &Client{}
	past := time.Now().Add(-10 * time.Second).UTC().Format(time.RFC1123)
	got := c.parseRetryAfter(past)
	if got != 1 {
		t.Errorf("parseRetryAfter(past) = %d, want 1 (past date)", got)
	}
}

func TestParseRetryAfter_InvalidString(t *testing.T) {
	c := &Client{}
	if got := c.parseRetryAfter("not-a-number"); got != 1 {
		t.Errorf("parseRetryAfter('not-a-number') = %d, want 1", got)
	}
}

// --- APIError ---

func TestAPIError_ErrorString(t *testing.T) {
	e := &APIError{StatusCode: 400, Body: `{"error":"bad request"}`}
	got := e.Error()
	if got != `API error (400): {"error":"bad request"}` {
		t.Errorf("Error() = %q", got)
	}
}

// --- HTTP integration tests using httptest ---

func TestCreateIssue_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/rest/api/2/issue" {
			t.Errorf("path = %s, want /rest/api/2/issue", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}

		w.WriteHeader(http.StatusCreated)
		encodeJSON(t, w, CreateIssueResponse{
			ID:   "10001",
			Key:  "PROJ-1",
			Self: "https://jira.example.com/rest/api/2/issue/10001",
		})
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "test-token", false)
	c.MaxRetries = 0

	resp, err := c.CreateIssue(context.Background(), &CreateIssueRequest{
		Fields: map[string]any{
			"summary":   "Test issue",
			"issuetype": FormatIssueType("Story"),
		},
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if resp.Key != "PROJ-1" {
		t.Errorf("Key = %q, want PROJ-1", resp.Key)
	}
}

func TestCreateIssue_ClientError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeBytes(t, w, []byte(`{"errors":{"summary":"required"}}`))
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 0

	_, err := c.CreateIssue(context.Background(), &CreateIssueRequest{Fields: map[string]any{}})
	if err == nil {
		t.Fatal("expected error for 400 response")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want 400", apiErr.StatusCode)
	}
}

func TestCreateIssue_ServerError_RetriesAndFails(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
		writeBytes(t, w, []byte("internal error"))
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 1
	c.RetryDelay = time.Millisecond // speed up test

	_, err := c.CreateIssue(context.Background(), &CreateIssueRequest{Fields: map[string]any{"summary": "test"}})
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}

	got := atomic.LoadInt32(&attempts)
	if got != 2 { // initial + 1 retry
		t.Errorf("attempts = %d, want 2", got)
	}
}

func TestCreateIssue_ServerError_RetriesAndSucceeds(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			writeBytes(t, w, []byte("temporary failure"))
			return
		}
		w.WriteHeader(http.StatusCreated)
		encodeJSON(t, w, CreateIssueResponse{Key: "PROJ-1"})
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 2
	c.RetryDelay = time.Millisecond

	resp, err := c.CreateIssue(context.Background(), &CreateIssueRequest{Fields: map[string]any{"summary": "test"}})
	if err != nil {
		t.Fatalf("expected success on retry: %v", err)
	}
	if resp.Key != "PROJ-1" {
		t.Errorf("Key = %q, want PROJ-1", resp.Key)
	}
}

func TestUpdateIssue_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/rest/api/2/issue/PROJ-1" {
			t.Errorf("path = %q, want /rest/api/2/issue/PROJ-1", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 0

	err := c.UpdateIssue(context.Background(), "PROJ-1", &UpdateIssueRequest{
		Fields: map[string]any{"summary": "updated"},
	})
	if err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
}

func TestUpdateIssue_InvalidKey(t *testing.T) {
	c := mustNewClient(t, testBaseURL, "token", false)
	err := c.UpdateIssue(context.Background(), "not-a-jira-key", &UpdateIssueRequest{Fields: map[string]any{}})
	if err == nil {
		t.Error("UpdateIssue with invalid key should return error")
	}
}

func TestCreateIssueLink_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/rest/api/2/issueLink" {
			t.Errorf("path = %q, want /rest/api/2/issueLink", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 0

	err := c.CreateIssueLink(context.Background(), &IssueLinkRequest{
		Type:         IssueLinkType{Name: "Blocks"},
		InwardIssue:  IssueRef{Key: "PROJ-2"},
		OutwardIssue: IssueRef{Key: "PROJ-1"},
	})
	if err != nil {
		t.Fatalf("CreateIssueLink: %v", err)
	}
}

func TestCreateIssue_CloudUsesAPIv3(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue" {
			t.Errorf("path = %q, want /rest/api/3/issue (Cloud)", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		encodeJSON(t, w, CreateIssueResponse{Key: "CLOUD-1"})
	}))
	defer server.Close()

	c, err := NewClientWithMode(server.URL, ModeCloud, Credentials{Email: "user@example.com", Token: "token"})
	if err != nil {
		t.Fatalf("NewClientWithMode: %v", err)
	}
	c.MaxRetries = 0

	resp, err := c.CreateIssue(context.Background(), &CreateIssueRequest{Fields: map[string]any{"summary": "cloud test"}})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if resp.Key != "CLOUD-1" {
		t.Errorf("Key = %q, want CLOUD-1", resp.Key)
	}
}

func TestDoRequest_RateLimit429(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusCreated)
		encodeJSON(t, w, CreateIssueResponse{Key: "PROJ-1"})
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 2
	c.RetryDelay = time.Millisecond

	resp, err := c.CreateIssue(context.Background(), &CreateIssueRequest{Fields: map[string]any{"summary": "test"}})
	if err != nil {
		t.Fatalf("expected success after 429 retry: %v", err)
	}

	got := atomic.LoadInt32(&attempts)
	if got < 2 {
		t.Errorf("expected at least 2 attempts, got %d", got)
	}
	_ = resp
}

// --- Benchmark parseRetryAfter ---

func BenchmarkParseRetryAfter_Seconds(b *testing.B) {
	c := &Client{}
	for b.Loop() {
		c.parseRetryAfter("30")
	}
}

func BenchmarkParseRetryAfter_HTTPDate(b *testing.B) {
	c := &Client{}
	future := time.Now().Add(30 * time.Second).UTC().Format(time.RFC1123)
	for b.Loop() {
		c.parseRetryAfter(future)
	}
}

// --- jqlQuote ---

func TestJQLQuote_Plain(t *testing.T) {
	if got := jqlQuote("PROJ"); got != `"PROJ"` {
		t.Errorf("jqlQuote(%q) = %q, want %q", "PROJ", got, `"PROJ"`)
	}
}

func TestJQLQuote_EmbeddedQuote(t *testing.T) {
	// A double-quote inside the value must be escaped with a backslash.
	if got := jqlQuote(`a"b`); got != `"a\"b"` {
		t.Errorf("jqlQuote(%q) = %q, want %q", `a"b`, got, `"a\"b"`)
	}
}

func TestJQLQuote_Backslash(t *testing.T) {
	// A backslash inside the value must be doubled.
	if got := jqlQuote(`a\b`); got != `"a\\b"` {
		t.Errorf("jqlQuote(%q) = %q, want %q", `a\b`, got, `"a\\b"`)
	}
}

func TestJQLQuote_BackslashAndQuote(t *testing.T) {
	// Combined: backslash then quote → \\ then \"
	if got := jqlQuote(`a\"b`); got != `"a\\\"b"` {
		t.Errorf("jqlQuote(%q) = %q, want %q", `a\"b`, got, `"a\\\"b"`)
	}
}

// --- FetchEpics ---

func TestFetchEpics_InvalidProjectKey(t *testing.T) {
	c := mustNewClient(t, testBaseURL, "token", false)
	_, err := c.FetchEpics(context.Background(), "invalid-key", "")
	if err == nil {
		t.Error("FetchEpics with invalid project key should return error")
	}
}

func TestFetchEpics_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}

		jql := r.URL.Query().Get("jql")
		if jql != `project = "PROJ" AND issuetype = Epic ORDER BY created DESC` {
			t.Errorf("jql = %q", jql)
		}

		// Verify the fields parameter is set
		fields := r.URL.Query().Get("fields")
		if fields != "summary,status,description" {
			t.Errorf("fields = %q, want summary,status,description", fields)
		}

		w.WriteHeader(http.StatusOK)
		encodeJSON(t, w, SearchResult{
			Total: 2,
			Issues: []SearchIssue{
				{
					Key: "PROJ-10",
					Fields: map[string]any{
						"summary": "Epic Alpha",
						"status":  map[string]any{"name": "In Progress"},
					},
				},
				{
					Key: "PROJ-5",
					Fields: map[string]any{
						"summary": "Epic Beta",
						"status":  map[string]any{"name": "Done"},
					},
				},
			},
		})
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 0

	epics, err := c.FetchEpics(context.Background(), "PROJ", "")
	if err != nil {
		t.Fatalf("FetchEpics: %v", err)
	}
	if len(epics) != 2 {
		t.Fatalf("len(epics) = %d, want 2", len(epics))
	}

	if epics[0].Key != "PROJ-10" {
		t.Errorf("epics[0].Key = %q, want PROJ-10", epics[0].Key)
	}
	if epics[0].Summary != "Epic Alpha" {
		t.Errorf("epics[0].Summary = %q, want Epic Alpha", epics[0].Summary)
	}
	if epics[0].Status != "In Progress" {
		t.Errorf("epics[0].Status = %q, want In Progress", epics[0].Status)
	}

	if epics[1].Key != "PROJ-5" {
		t.Errorf("epics[1].Key = %q, want PROJ-5", epics[1].Key)
	}
	if epics[1].Summary != "Epic Beta" {
		t.Errorf("epics[1].Summary = %q", epics[1].Summary)
	}
	if epics[1].Status != "Done" {
		t.Errorf("epics[1].Status = %q", epics[1].Status)
	}
}

func TestFetchEpics_WithStatusFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jql := r.URL.Query().Get("jql")
		expectedJQL := `project = "PROJ" AND issuetype = Epic AND status = "In Progress" ORDER BY created DESC`
		if jql != expectedJQL {
			t.Errorf("jql = %q, want %q", jql, expectedJQL)
		}

		w.WriteHeader(http.StatusOK)
		encodeJSON(t, w, SearchResult{
			Total: 1,
			Issues: []SearchIssue{
				{
					Key: "PROJ-10",
					Fields: map[string]any{
						"summary": "Epic Alpha",
						"status":  map[string]any{"name": "In Progress"},
					},
				},
			},
		})
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 0

	epics, err := c.FetchEpics(context.Background(), "PROJ", "In Progress")
	if err != nil {
		t.Fatalf("FetchEpics: %v", err)
	}
	if len(epics) != 1 {
		t.Fatalf("len(epics) = %d, want 1", len(epics))
	}
	if epics[0].Key != "PROJ-10" {
		t.Errorf("epics[0].Key = %q, want PROJ-10", epics[0].Key)
	}
	if epics[0].Status != "In Progress" {
		t.Errorf("epics[0].Status = %q, want In Progress", epics[0].Status)
	}
}

func TestFetchEpics_StatusFilterNoResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jql := r.URL.Query().Get("jql")
		expectedJQL := `project = "PROJ" AND issuetype = Epic AND status = "Done" ORDER BY created DESC`
		if jql != expectedJQL {
			t.Errorf("jql = %q, want %q", jql, expectedJQL)
		}

		w.WriteHeader(http.StatusOK)
		encodeJSON(t, w, SearchResult{Total: 0, Issues: nil})
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 0

	epics, err := c.FetchEpics(context.Background(), "PROJ", "Done")
	if err != nil {
		t.Fatalf("FetchEpics: %v", err)
	}
	if len(epics) != 0 {
		t.Errorf("expected 0 epics, got %d", len(epics))
	}
}

func TestFetchEpics_NegatedStatusFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jql := r.URL.Query().Get("jql")
		expectedJQL := `project = "PROJ" AND issuetype = Epic AND status != "Done" ORDER BY created DESC`
		if jql != expectedJQL {
			t.Errorf("jql = %q, want %q", jql, expectedJQL)
		}

		w.WriteHeader(http.StatusOK)
		encodeJSON(t, w, SearchResult{
			Total: 2,
			Issues: []SearchIssue{
				{
					Key: "PROJ-1",
					Fields: map[string]any{
						"summary": "Epic Alpha",
						"status":  map[string]any{"name": "To Do"},
					},
				},
				{
					Key: "PROJ-2",
					Fields: map[string]any{
						"summary": "Epic Beta",
						"status":  map[string]any{"name": "In Progress"},
					},
				},
			},
		})
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 0

	epics, err := c.FetchEpics(context.Background(), "PROJ", "NOT:Done")
	if err != nil {
		t.Fatalf("FetchEpics: %v", err)
	}
	if len(epics) != 2 {
		t.Fatalf("len(epics) = %d, want 2", len(epics))
	}
	if epics[0].Status != "To Do" {
		t.Errorf("epics[0].Status = %q, want To Do", epics[0].Status)
	}
	if epics[1].Status != "In Progress" {
		t.Errorf("epics[1].Status = %q, want In Progress", epics[1].Status)
	}
}

func TestFetchEpics_Empty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		encodeJSON(t, w, SearchResult{Total: 0, Issues: nil})
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 0

	epics, err := c.FetchEpics(context.Background(), "EMPTY", "")
	if err != nil {
		t.Fatalf("FetchEpics: %v", err)
	}
	if len(epics) != 0 {
		t.Errorf("expected 0 epics, got %d", len(epics))
	}
}

func TestFetchEpics_Pagination(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := atomic.AddInt32(&requestCount, 1)
		startAt := r.URL.Query().Get("startAt")

		w.WriteHeader(http.StatusOK)

		switch {
		case page == 1 && startAt == "0":
			// First page: 50 results
			issues := make([]SearchIssue, 50)
			for i := range issues {
				issues[i] = SearchIssue{
					Key: fmt.Sprintf("PROJ-%d", i+1),
					Fields: map[string]any{
						"summary": fmt.Sprintf("Epic %d", i+1),
						"status":  map[string]any{"name": "To Do"},
					},
				}
			}
			encodeJSON(t, w, SearchResult{Total: 75, Issues: issues})

		case page == 2 && startAt == "50":
			// Second page: 25 results
			issues := make([]SearchIssue, 25)
			for i := range issues {
				issues[i] = SearchIssue{
					Key: fmt.Sprintf("PROJ-%d", 51+i),
					Fields: map[string]any{
						"summary": fmt.Sprintf("Epic %d", 51+i),
						"status":  map[string]any{"name": "To Do"},
					},
				}
			}
			encodeJSON(t, w, SearchResult{Total: 75, Issues: issues})

		default:
			t.Errorf("unexpected request: page=%d, startAt=%s", page, startAt)
			encodeJSON(t, w, SearchResult{Total: 0, Issues: nil})
		}
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 0

	epics, err := c.FetchEpics(context.Background(), "PROJ", "")
	if err != nil {
		t.Fatalf("FetchEpics: %v", err)
	}
	if len(epics) != 75 {
		t.Errorf("len(epics) = %d, want 75", len(epics))
	}

	pages := atomic.LoadInt32(&requestCount)
	if pages != 2 {
		t.Errorf("expected 2 paginated requests, got %d", pages)
	}

	// Verify first and last
	if epics[0].Key != "PROJ-1" {
		t.Errorf("first epic key = %q, want PROJ-1", epics[0].Key)
	}
	if epics[74].Key != "PROJ-75" {
		t.Errorf("last epic key = %q, want PROJ-75", epics[74].Key)
	}
}

func TestFetchEpics_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeBytes(t, w, []byte(`{"errorMessages":["invalid JQL"]}`))
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 0

	_, err := c.FetchEpics(context.Background(), "BAD", "")
	if err == nil {
		t.Fatal("expected error for API failure")
	}
}

func TestFetchEpics_MissingStatusField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		encodeJSON(t, w, SearchResult{
			Total: 1,
			Issues: []SearchIssue{
				{
					Key: "PROJ-1",
					Fields: map[string]any{
						"summary": "Epic without status",
						// no "status" field
					},
				},
			},
		})
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 0

	epics, err := c.FetchEpics(context.Background(), "PROJ", "")
	if err != nil {
		t.Fatalf("FetchEpics: %v", err)
	}
	if len(epics) != 1 {
		t.Fatalf("len(epics) = %d, want 1", len(epics))
	}
	if epics[0].Status != "" {
		t.Errorf("Status = %q, want empty for missing status", epics[0].Status)
	}
	if epics[0].Summary != "Epic without status" {
		t.Errorf("Summary = %q", epics[0].Summary)
	}
}

func TestFetchEpics_CloudUsesV3(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !testing.Short() {
			// Verify path uses v3 search/jql (Cloud endpoint, not legacy /search)
			expectedPath := "/rest/api/3/search/jql"
			if r.URL.Path != expectedPath {
				t.Errorf("path = %q, want %q (Cloud v3 search/jql endpoint)", r.URL.Path, expectedPath)
			}
		}
		w.WriteHeader(http.StatusOK)
		writeBytes(t, w, []byte(`{"issues":[],"isLast":true}`))
	}))
	defer server.Close()

	c, err := NewClientWithMode(server.URL, ModeCloud, Credentials{Email: "user@example.com", Token: "token"})
	if err != nil {
		t.Fatalf("NewClientWithMode: %v", err)
	}
	c.MaxRetries = 0

	if _, err := c.FetchEpics(context.Background(), "PROJ", ""); err != nil {
		t.Fatalf("FetchEpics: %v", err)
	}
}

// Jira answers some requests with bad credentials as 200 + an anonymous (empty)
// result, flagging the failed login only in X-Seraph-LoginReason. doRequest must
// fail closed with a 401 APIError, not decode the anonymous body, and not retry.
func TestDoRequest_SeraphLoginFailureOn2xxIs401(t *testing.T) {
	for _, reason := range []string{"AUTHENTICATED_FAILED", "AUTHENTICATION_DENIED"} {
		t.Run(reason, func(t *testing.T) {
			var attempts int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&attempts, 1)
				w.Header().Set("X-Seraph-LoginReason", reason)
				w.WriteHeader(http.StatusOK)
				writeBytes(t, w, []byte(`[{"id":"summary","name":"Summary"}]`))
			}))
			defer server.Close()

			c := mustNewClient(t, server.URL, "token", false)
			c.MaxRetries = 3
			c.RetryDelay = time.Millisecond

			fields, err := c.FetchFields(context.Background())
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
				t.Fatalf("err = %v, want *APIError with status 401", err)
			}
			if !strings.Contains(apiErr.Body, reason) {
				t.Errorf("error body %q does not name the login reason %q", apiErr.Body, reason)
			}
			if fields != nil {
				t.Errorf("fields = %v, want nil (anonymous result must not be decoded)", fields)
			}
			if got := atomic.LoadInt32(&attempts); got != 1 {
				t.Errorf("attempts = %d, want 1 (auth failure must not be retried)", got)
			}
		})
	}
}

// A successful login (X-Seraph-LoginReason: OK) is a normal 2xx response.
func TestDoRequest_SeraphLoginOKIsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seraph-LoginReason", "OK")
		w.WriteHeader(http.StatusOK)
		writeBytes(t, w, []byte(`[{"id":"summary","name":"Summary"}]`))
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 0

	fields, err := c.FetchFields(context.Background())
	if err != nil {
		t.Fatalf("FetchFields: %v", err)
	}
	if len(fields) != 1 || fields[0].ID != "summary" {
		t.Errorf("fields = %+v, want one field with ID summary", fields)
	}
}

// Regression: on Cloud, search/jql with rejected credentials returns 200 +
// {"issues":[],"isLast":true}. FetchEpics must report the auth failure instead
// of an empty epic list ("No Epics found").
func TestFetchEpics_CloudAnonymousResultIsAuthError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seraph-LoginReason", "AUTHENTICATED_FAILED")
		w.WriteHeader(http.StatusOK)
		writeBytes(t, w, []byte(`{"issues":[],"isLast":true}`))
	}))
	defer server.Close()

	c, err := NewClientWithMode(server.URL, ModeCloud, Credentials{Email: "user@example.com", Token: "token"})
	if err != nil {
		t.Fatalf("NewClientWithMode: %v", err)
	}
	c.MaxRetries = 0

	epics, err := c.FetchEpics(context.Background(), "PROJ", "NOT:Done")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("err = %v, want *APIError with status 401", err)
	}
	if epics != nil {
		t.Errorf("epics = %v, want nil", epics)
	}
}

// Test that doRequest doesn't retry on 4xx errors (except 429)
func TestDoRequest_NoRetryOn4xx(t *testing.T) {
	statusCodes := []int{400, 401, 403, 404, 409}
	for _, code := range statusCodes {
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			var attempts int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&attempts, 1)
				w.WriteHeader(code)
				writeBytes(t, w, []byte(`{"error":"client error"}`))
			}))
			defer server.Close()

			c := mustNewClient(t, server.URL, "token", false)
			c.MaxRetries = 3
			c.RetryDelay = time.Millisecond

			_, err := c.CreateIssue(context.Background(), &CreateIssueRequest{Fields: map[string]any{"summary": "test"}})
			if err == nil {
				t.Fatal("expected error")
			}

			got := atomic.LoadInt32(&attempts)
			if got != 1 {
				t.Errorf("4xx should not retry: attempts = %d, want 1", got)
			}
		})
	}
}

// --- FetchFields ---

func TestFetchFields_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/rest/api/2/field" {
			t.Errorf("path = %q, want /rest/api/2/field", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		encodeJSON(t, w, []Field{
			{ID: "summary", Name: "Summary", Custom: false},
			{ID: "customfield_10009", Name: "Epic Link", Custom: true, Schema: &FieldSchema{
				Type: "any", Custom: "com.pyxis.greenhopper.jira:gh-epic-link", CustomID: 10009,
			}},
			{ID: "customfield_10010", Name: "Epic Name", Custom: true, Schema: &FieldSchema{
				Type: "string", Custom: "com.pyxis.greenhopper.jira:gh-epic-label", CustomID: 10010,
			}},
			{ID: "priority", Name: "Priority", Custom: false},
		})
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 0

	fields, err := c.FetchFields(context.Background())
	if err != nil {
		t.Fatalf("FetchFields: %v", err)
	}
	if len(fields) != 4 {
		t.Fatalf("len(fields) = %d, want 4", len(fields))
	}

	if fields[0].ID != "summary" || fields[0].Name != "Summary" || fields[0].Custom != false {
		t.Errorf("fields[0] = %+v, unexpected", fields[0])
	}
	if fields[1].ID != "customfield_10009" || fields[1].Name != "Epic Link" || !fields[1].Custom {
		t.Errorf("fields[1] = %+v, unexpected", fields[1])
	}
	if fields[1].Schema == nil {
		t.Fatal("fields[1].Schema should not be nil")
	}
	if fields[1].Schema.Custom != "com.pyxis.greenhopper.jira:gh-epic-link" {
		t.Errorf("fields[1].Schema.Custom = %q, unexpected", fields[1].Schema.Custom)
	}
}

func TestFetchFields_Empty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		encodeJSON(t, w, []Field{})
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 0

	fields, err := c.FetchFields(context.Background())
	if err != nil {
		t.Fatalf("FetchFields: %v", err)
	}
	if len(fields) != 0 {
		t.Errorf("len(fields) = %d, want 0", len(fields))
	}
}

func TestFetchFields_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		writeBytes(t, w, []byte(`{"errorMessages":["unauthorized"]}`))
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 0

	_, err := c.FetchFields(context.Background())
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

func TestFetchFields_CloudUsesV3(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/field" {
			t.Errorf("path = %q, want /rest/api/3/field (Cloud v3)", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		encodeJSON(t, w, []Field{})
	}))
	defer server.Close()

	c, err := NewClientWithMode(server.URL, ModeCloud, Credentials{Email: "user@example.com", Token: "token"})
	if err != nil {
		t.Fatalf("NewClientWithMode: %v", err)
	}
	c.MaxRetries = 0

	if _, err := c.FetchFields(context.Background()); err != nil {
		t.Fatalf("FetchFields: %v", err)
	}
}

func TestFetchFields_NoSchema(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Some fields (like "issuekey") have no schema at all
		writeBytes(t, w, []byte(`[{"id":"issuekey","name":"Key","custom":false}]`))
	}))
	defer server.Close()

	c := mustNewClient(t, server.URL, "token", false)
	c.MaxRetries = 0

	fields, err := c.FetchFields(context.Background())
	if err != nil {
		t.Fatalf("FetchFields: %v", err)
	}
	if len(fields) != 1 {
		t.Fatalf("len(fields) = %d, want 1", len(fields))
	}
	if fields[0].Schema != nil {
		t.Errorf("expected nil Schema for issuekey field")
	}
}

// --- FetchIssueLinkTypes ---

func TestFetchIssueLinkTypes_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/issueLinkType" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		encodeJSON(t, w, map[string]any{
			"issueLinkTypes": []map[string]any{
				{"id": "10000", "name": "Blocks", "inward": "is blocked by", "outward": "blocks"},
				{"id": "10001", "name": "Duplicate", "inward": "is duplicated by", "outward": "duplicates"},
				{"id": "10002", "name": "Relates", "inward": "relates to", "outward": "relates to"},
			},
		})
	}))
	defer ts.Close()

	c := mustNewClient(t, ts.URL, "tok", false)
	types, err := c.FetchIssueLinkTypes(context.Background())
	if err != nil {
		t.Fatalf("FetchIssueLinkTypes: %v", err)
	}
	if len(types) != 3 {
		t.Fatalf("len(types) = %d, want 3", len(types))
	}
	if types[0].Name != "Blocks" {
		t.Errorf("types[0].Name = %q, want Blocks", types[0].Name)
	}
	if types[0].Inward != "is blocked by" {
		t.Errorf("types[0].Inward = %q, want 'is blocked by'", types[0].Inward)
	}
	if types[0].Outward != "blocks" {
		t.Errorf("types[0].Outward = %q, want 'blocks'", types[0].Outward)
	}
}

func TestFetchIssueLinkTypes_Empty(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		encodeJSON(t, w, map[string]any{"issueLinkTypes": []any{}})
	}))
	defer ts.Close()

	c := mustNewClient(t, ts.URL, "tok", false)
	types, err := c.FetchIssueLinkTypes(context.Background())
	if err != nil {
		t.Fatalf("FetchIssueLinkTypes: %v", err)
	}
	if len(types) != 0 {
		t.Errorf("len(types) = %d, want 0", len(types))
	}
}

func TestFetchIssueLinkTypes_APIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		writeBytes(t, w, []byte(`{"errorMessages":["Forbidden"]}`))
	}))
	defer ts.Close()

	c := mustNewClient(t, ts.URL, "tok", false)
	_, err := c.FetchIssueLinkTypes(context.Background())
	if err == nil {
		t.Fatal("expected error for 403")
	}
}

func TestFetchIssueLinkTypes_CloudUsesV3(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issueLinkType" {
			t.Errorf("expected v3 path, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		encodeJSON(t, w, map[string]any{"issueLinkTypes": []any{}})
	}))
	defer ts.Close()

	c, err := NewClientWithMode(ts.URL, ModeCloud, Credentials{Email: "user@example.com", Token: "tok"})
	if err != nil {
		t.Fatalf("NewClientWithMode: %v", err)
	}
	_, err = c.FetchIssueLinkTypes(context.Background())
	if err != nil {
		t.Fatalf("FetchIssueLinkTypes: %v", err)
	}
}

// --- SearchUsers (Cloud user-search endpoint) ---

func TestSearchUsers_QueriesUserSearchEndpoint(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		w.WriteHeader(http.StatusOK)
		writeBytes(t, w, []byte(`[{"accountId":"acc-1","emailAddress":"a@x.com","displayName":"Alice"}]`))
	}))
	defer srv.Close()

	c, err := NewClientWithMode(srv.URL, ModeCloud, Credentials{Email: "e@x.com", Token: "tok"})
	if err != nil {
		t.Fatalf("NewClientWithMode: %v", err)
	}

	users, err := c.SearchUsers(context.Background(), "a@x.com")
	if err != nil {
		t.Fatalf("SearchUsers: %v", err)
	}
	if gotPath != "/rest/api/3/user/search" {
		t.Errorf("path = %q, want /rest/api/3/user/search", gotPath)
	}
	if gotQuery != "a@x.com" {
		t.Errorf("query = %q, want a@x.com", gotQuery)
	}
	if len(users) != 1 {
		t.Fatalf("len(users) = %d, want 1", len(users))
	}
	if users[0].AccountID != "acc-1" {
		t.Errorf("AccountID = %q, want acc-1", users[0].AccountID)
	}
	if users[0].EmailAddress != "a@x.com" {
		t.Errorf("EmailAddress = %q, want a@x.com", users[0].EmailAddress)
	}
}

func TestSearchUsers_ZeroMatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		writeBytes(t, w, []byte(`[]`))
	}))
	defer srv.Close()

	c, err := NewClientWithMode(srv.URL, ModeCloud, Credentials{Email: "e@x.com", Token: "tok"})
	if err != nil {
		t.Fatalf("NewClientWithMode: %v", err)
	}

	users, err := c.SearchUsers(context.Background(), "ghost@x.com")
	if err != nil {
		t.Fatalf("SearchUsers: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("len(users) = %d, want 0", len(users))
	}
}

func TestSearchUsers_MultipleMatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		writeBytes(t, w, []byte(`[{"accountId":"acc-1"},{"accountId":"acc-2"}]`))
	}))
	defer srv.Close()

	c, err := NewClientWithMode(srv.URL, ModeCloud, Credentials{Email: "e@x.com", Token: "tok"})
	if err != nil {
		t.Fatalf("NewClientWithMode: %v", err)
	}

	users, err := c.SearchUsers(context.Background(), "dup@x.com")
	if err != nil {
		t.Fatalf("SearchUsers: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("len(users) = %d, want 2", len(users))
	}
	if users[0].AccountID != "acc-1" || users[1].AccountID != "acc-2" {
		t.Errorf("account IDs = [%q %q], want [acc-1 acc-2]", users[0].AccountID, users[1].AccountID)
	}
}

func TestSearchUsers_APIErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		writeBytes(t, w, []byte(`{"errorMessages":["no permission"]}`))
	}))
	defer srv.Close()

	c, err := NewClientWithMode(srv.URL, ModeCloud, Credentials{Email: "e@x.com", Token: "tok"})
	if err != nil {
		t.Fatalf("NewClientWithMode: %v", err)
	}

	_, err = c.SearchUsers(context.Background(), "a@x.com")
	if err == nil {
		t.Fatal("SearchUsers should return an error on a 403 response")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want a *APIError in the chain", err)
	}
	if apiErr.StatusCode != http.StatusForbidden {
		t.Errorf("StatusCode = %d, want 403", apiErr.StatusCode)
	}
}

// --- FetchEpics: Cloud search/jql path ---

// cloudPageServer serves an ordered sequence of raw JSON page bodies from the
// Cloud search/jql endpoint, advancing by the incoming nextPageToken. It also
// records the JQL of the first request so tests can assert JQL equivalence.
func cloudPageServer(t *testing.T, pages []string, gotJQL *string) *httptest.Server {
	t.Helper()
	first := true
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/search/jql") {
			t.Errorf("Cloud must call /search/jql, got %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			JQL           string `json:"jql"`
			NextPageToken string `json:"nextPageToken"`
		}
		if r.Body != nil {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read search/jql body: %v", err)
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Errorf("unmarshal search/jql body: %v", err)
				}
			}
		}
		if first && gotJQL != nil {
			*gotJQL = body.JQL
			first = false
		}
		idx := 0
		switch body.NextPageToken {
		case "":
			idx = 0
		case "tok-1":
			idx = 1
		case "tok-2":
			idx = 2
		default:
			t.Errorf("unexpected nextPageToken %q", body.NextPageToken)
		}
		if idx >= len(pages) {
			t.Errorf("no page for token %q", body.NextPageToken)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		writeBytes(t, w, []byte(pages[idx]))
	}))
}

func newCloudClient(t *testing.T, url string) *Client {
	t.Helper()
	c, err := NewClientWithMode(url, ModeCloud, Credentials{Email: "e@x.com", Token: "tok"})
	if err != nil {
		t.Fatalf("NewClientWithMode Cloud: %v", err)
	}
	c.MaxRetries = 0
	return c
}

func TestFetchEpics_Cloud_MultiPage_NextPageToken(t *testing.T) {
	pages := []string{
		`{"issues":[{"key":"PROJ-1","fields":{"summary":"E1","status":{"name":"To Do"}}}],"nextPageToken":"tok-1","isLast":false}`,
		`{"issues":[{"key":"PROJ-2","fields":{"summary":"E2","status":{"name":"Done"}}}],"isLast":true}`,
	}
	server := cloudPageServer(t, pages, nil)
	defer server.Close()

	epics, err := newCloudClient(t, server.URL).FetchEpics(context.Background(), "PROJ", "")
	if err != nil {
		t.Fatalf("FetchEpics: %v", err)
	}
	if len(epics) != 2 {
		t.Fatalf("len(epics) = %d, want 2 (%v)", len(epics), epics)
	}
	if epics[0].Key != "PROJ-1" || epics[1].Key != "PROJ-2" {
		t.Errorf("keys = %q,%q want PROJ-1,PROJ-2", epics[0].Key, epics[1].Key)
	}
	if epics[0].Summary != "E1" || epics[1].Status != "Done" {
		t.Errorf("epics = %+v", epics)
	}
}

func TestFetchEpics_Cloud_TerminatesOnIsLastWithToken(t *testing.T) {
	// isLast=true MUST terminate even though a nextPageToken is still present.
	// If the loop followed the token it would request tok-1 (no such page) and
	// the server would 500.
	pages := []string{
		`{"issues":[{"key":"PROJ-1","fields":{"summary":"E1","status":{"name":"To Do"}}}],"nextPageToken":"tok-1","isLast":true}`,
	}
	server := cloudPageServer(t, pages, nil)
	defer server.Close()

	epics, err := newCloudClient(t, server.URL).FetchEpics(context.Background(), "PROJ", "")
	if err != nil {
		t.Fatalf("FetchEpics: %v", err)
	}
	if len(epics) != 1 || epics[0].Key != "PROJ-1" {
		t.Fatalf("epics = %v, want exactly [PROJ-1]", epics)
	}
}

func TestFetchEpics_Cloud_TerminatesOnEmptyPage(t *testing.T) {
	// A page with a token but zero issues MUST terminate (no count to rely on).
	pages := []string{
		`{"issues":[{"key":"PROJ-1","fields":{"summary":"E1","status":{"name":"To Do"}}}],"nextPageToken":"tok-1","isLast":false}`,
		`{"issues":[],"nextPageToken":"tok-2","isLast":false}`,
	}
	server := cloudPageServer(t, pages, nil)
	defer server.Close()

	epics, err := newCloudClient(t, server.URL).FetchEpics(context.Background(), "PROJ", "")
	if err != nil {
		t.Fatalf("FetchEpics: %v", err)
	}
	if len(epics) != 1 || epics[0].Key != "PROJ-1" {
		t.Fatalf("epics = %v, want exactly [PROJ-1]", epics)
	}
}

func TestFetchEpics_Cloud_TerminatesOnRepeatedPage(t *testing.T) {
	// Endpoint quirk guard: the second page repeats the first page's issue key and
	// keeps handing back a token. Without a seen-key guard this loops forever;
	// with it, the loop terminates and the repeated epic is NOT double-counted.
	pages := []string{
		`{"issues":[{"key":"PROJ-1","fields":{"summary":"E1","status":{"name":"To Do"}}}],"nextPageToken":"tok-1","isLast":false}`,
		`{"issues":[{"key":"PROJ-1","fields":{"summary":"E1","status":{"name":"To Do"}}}],"nextPageToken":"tok-2","isLast":false}`,
	}
	server := cloudPageServer(t, pages, nil)
	defer server.Close()

	epics, err := newCloudClient(t, server.URL).FetchEpics(context.Background(), "PROJ", "")
	if err != nil {
		t.Fatalf("FetchEpics: %v", err)
	}
	if len(epics) != 1 || epics[0].Key != "PROJ-1" {
		t.Fatalf("epics = %v, want exactly [PROJ-1] (no duplicates, loop terminated)", epics)
	}
}

func TestFetchEpics_Cloud_ADFDescriptionFlattened(t *testing.T) {
	pages := []string{
		`{"issues":[{"key":"PROJ-1","fields":{
			"summary":"Rich","status":{"name":"To Do"},
			"description":{"version":1,"type":"doc","content":[
				{"type":"paragraph","content":[
					{"type":"text","text":"Hello "},
					{"type":"text","text":"world","marks":[{"type":"strong"}]}
				]}
			]}
		}}],"isLast":true}`,
	}
	server := cloudPageServer(t, pages, nil)
	defer server.Close()

	epics, err := newCloudClient(t, server.URL).FetchEpics(context.Background(), "PROJ", "")
	if err != nil {
		t.Fatalf("FetchEpics: %v", err)
	}
	if len(epics) != 1 {
		t.Fatalf("len(epics) = %d, want 1", len(epics))
	}
	if epics[0].Description != "Hello world" {
		t.Errorf("description = %q, want %q (ADF flattened)", epics[0].Description, "Hello world")
	}
}

func TestFetchEpics_Cloud_StringDescriptionStillWorks(t *testing.T) {
	// Defensive: if a Cloud response ever carries a plain-string description, it
	// must pass through unchanged (no panic, no empty string).
	pages := []string{
		`{"issues":[{"key":"PROJ-1","fields":{"summary":"S","status":{"name":"To Do"},"description":"plain text"}}],"isLast":true}`,
	}
	server := cloudPageServer(t, pages, nil)
	defer server.Close()

	epics, err := newCloudClient(t, server.URL).FetchEpics(context.Background(), "PROJ", "")
	if err != nil {
		t.Fatalf("FetchEpics: %v", err)
	}
	if len(epics) != 1 || epics[0].Description != "plain text" {
		t.Fatalf("epics = %v, want description %q", epics, "plain text")
	}
}

func TestFetchEpics_Cloud_JQLMatchesDataCenter(t *testing.T) {
	// Shared JQL: Cloud must build the exact same JQL string as Data Center,
	// including the NOT: prefix handling.
	cases := []struct {
		statusFilter string
		wantJQL      string
	}{
		{"", `project = "PROJ" AND issuetype = Epic ORDER BY created DESC`},
		{"Done", `project = "PROJ" AND issuetype = Epic AND status = "Done" ORDER BY created DESC`},
		{"NOT:Done", `project = "PROJ" AND issuetype = Epic AND status != "Done" ORDER BY created DESC`},
	}
	for _, tc := range cases {
		t.Run(tc.statusFilter, func(t *testing.T) {
			var gotJQL string
			pages := []string{`{"issues":[],"isLast":true}`}
			server := cloudPageServer(t, pages, &gotJQL)
			defer server.Close()

			if _, err := newCloudClient(t, server.URL).FetchEpics(context.Background(), "PROJ", tc.statusFilter); err != nil {
				t.Fatalf("FetchEpics: %v", err)
			}
			if gotJQL != tc.wantJQL {
				t.Errorf("Cloud JQL = %q, want %q", gotJQL, tc.wantJQL)
			}
		})
	}
}
