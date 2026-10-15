// SPDX-License-Identifier: Apache-2.0
//
// Package jira provides a client for the Jira REST API.
package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kengou/jira-ticket-creator/internal/adf"
)

// maxResponseBodySize caps the number of bytes read from a Jira API response
// body to prevent excessive memory consumption on unexpectedly large responses.
const maxResponseBodySize = 10 << 20 // 10 MiB

// DefaultEpicNameField is the Jira Data Center custom field ID for the epic name.
const DefaultEpicNameField = "customfield_10011"

// DefaultEpicLinkField is the Jira Data Center custom field ID for the epic link.
const DefaultEpicLinkField = "customfield_10009"

// projectKeyRE matches a valid Jira project key (e.g. "PROJ", "ABC123").
var projectKeyRE = regexp.MustCompile(`^[A-Z][A-Z0-9]+$`)

// epicFields is the set of Jira fields fetched for every epic. Shared by the
// Cloud and Data Center search paths so both request the same fields.
var epicFields = []string{"summary", "status", "description"}

// Client is a Jira REST API client with retry support.
type Client struct {
	baseURL    string
	mode       Mode
	creds      Credentials
	apiVersion string // "2" for Data Center, "3" for Cloud
	httpClient *http.Client

	// Configurable retry settings
	MaxRetries int
	RetryDelay time.Duration

	// MaxRetryAfterSecs caps the number of seconds to honour in a 429
	// Retry-After header. Default (0) is treated as 60 seconds.
	MaxRetryAfterSecs int
}

// ClientOption configures the client.
type ClientOption func(*Client)

// WithMaxRetries sets the maximum number of retries.
func WithMaxRetries(n int) ClientOption {
	return func(c *Client) {
		c.MaxRetries = n
	}
}

// WithTimeout sets the HTTP client timeout.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) {
		c.httpClient.Timeout = d
	}
}

// WithMaxRetryAfterSecs sets the maximum seconds to honour in a Retry-After header.
func WithMaxRetryAfterSecs(n int) ClientOption {
	return func(c *Client) {
		c.MaxRetryAfterSecs = n
	}
}

// normalizeURL ensures the base URL has an https scheme and no trailing slash.
// If no scheme is present, https:// is prepended. Trailing slashes are stripped.
// Returns an error if an explicit http:// scheme is used, which would send the
// Bearer token in plaintext.
func normalizeURL(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return rawURL, nil
	}

	// Normalize the scheme to lowercase so that HTTP://, Http://, etc. are all caught.
	if schemeEnd := strings.Index(rawURL, "://"); schemeEnd >= 0 {
		rawURL = strings.ToLower(rawURL[:schemeEnd+3]) + rawURL[schemeEnd+3:]
	}

	// Prepend https:// if no scheme is present
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}

	// Parse the URL to strip userinfo (credentials embedded in URL) and validate structure.
	parsed, parseErr := url.Parse(rawURL)
	if parseErr != nil {
		// Do not wrap parseErr: a *url.Error message embeds the raw URL, which
		// may carry a password (userinfo) from JIRA_URL.
		return "", errors.New("invalid URL: could not parse the Jira URL (check scheme, host and port)")
	}

	// Reject explicit http:// for non-loopback addresses — Bearer tokens must
	// not be sent in plaintext over the network.
	if parsed.Scheme == "http" {
		hostname := parsed.Hostname()
		ip := net.ParseIP(hostname)
		if hostname != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", errors.New("http:// is not allowed for non-loopback addresses; use https:// to protect your token")
		}
	}

	if parsed.User != nil {
		fmt.Fprintln(os.Stderr, "Warning: stripping userinfo (credentials) from Jira URL — use JIRA_TOKEN env var instead")
		parsed.User = nil
		rawURL = parsed.String()
	}

	// Enforce JIRA_ALLOWED_HOSTS if set (comma-separated list of allowed hostnames).
	if allowedHosts := os.Getenv("JIRA_ALLOWED_HOSTS"); allowedHosts != "" {
		hostname := parsed.Hostname()
		allowed := false
		for _, h := range strings.Split(allowedHosts, ",") {
			if strings.EqualFold(strings.TrimSpace(h), hostname) {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", fmt.Errorf("host %q is not in JIRA_ALLOWED_HOSTS (%s)", hostname, allowedHosts)
		}
	}

	// Strip trailing slashes
	rawURL = strings.TrimRight(rawURL, "/")

	return rawURL, nil
}

// NewClient creates a new Jira client using the legacy boolean surface.
// Set isCloud to true for Jira Cloud (API v3), false for Data Center (API v2).
// It is a thin wrapper over NewClientWithMode kept for backward compatibility:
// Data Center callers (isCloud=false) get byte-for-byte identical behavior
// (Bearer auth, /rest/api/2/ paths).
// isCloud=true is retained for signature compatibility but returns an error because Jira Cloud
// requires an email; Cloud callers must use NewClientWithMode.
// Returns an error if baseURL uses a non-loopback http:// scheme.
func NewClient(baseURL, token string, isCloud bool, opts ...ClientOption) (*Client, error) {
	mode := ModeDataCenter
	if isCloud {
		mode = ModeCloud
	}
	return NewClientWithMode(baseURL, mode, Credentials{Token: token}, opts...)
}

// NewClientWithMode creates a new Jira client with an explicitly resolved
// platform mode and credentials. This is the primary construction surface:
// mode + credentials are supplied as one coherent input so downstream features
// branch on the client's stored mode without re-inspecting URLs or env vars.
// Returns an error if baseURL uses a non-loopback http:// scheme.
func NewClientWithMode(baseURL string, mode Mode, creds Credentials, opts ...ClientOption) (*Client, error) {
	normalizedURL, err := normalizeURL(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Jira URL: %w", err)
	}

	if mode == ModeCloud && strings.TrimSpace(creds.Email) == "" {
		return nil, errors.New("jira Cloud mode requires an email for Basic auth (email + API token); use NewClientWithMode with Credentials.Email set")
	}

	c := &Client{
		baseURL:    normalizedURL,
		mode:       mode,
		creds:      creds,
		apiVersion: mode.APIVersion(),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			// Go 1.8+ strips the Authorization header on cross-domain redirects,
			// but we also explicitly block HTTPS→HTTP downgrades to prevent token leakage.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme == "http" {
					return errors.New("redirect from https to http refused: Bearer token would be sent in plaintext")
				}
				return nil
			},
		},
		MaxRetries:        3,
		RetryDelay:        time.Second,
		MaxRetryAfterSecs: 60,
	}

	for _, opt := range opts {
		opt(c)
	}

	return c, nil
}

// CreateIssueRequest represents a create issue request.
type CreateIssueRequest struct {
	Fields map[string]any `json:"fields"`
}

// CreateIssueResponse represents a create issue response.
type CreateIssueResponse struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Self string `json:"self"`
}

// SearchResult represents a search result.
type SearchResult struct {
	Issues []SearchIssue `json:"issues"`
	Total  int           `json:"total"`
}

// SearchIssue represents an issue in search results.
type SearchIssue struct {
	ID     string         `json:"id"`
	Key    string         `json:"key"`
	Self   string         `json:"self"`
	Fields map[string]any `json:"fields"`
}

// cloudSearchResult represents a Jira Cloud POST /rest/api/3/search/jql response.
// The Cloud endpoint carries NO "total" — pagination is driven by NextPageToken
// and IsLast, so the fetch loop must never rely on a count.
type cloudSearchResult struct {
	Issues        []SearchIssue `json:"issues"`
	NextPageToken string        `json:"nextPageToken"`
	IsLast        bool          `json:"isLast"`
}

// cloudSearchRequest is the POST body for the Cloud search/jql endpoint. The
// NextPageToken is omitted on the first request and set to the previous page's
// token thereafter.
type cloudSearchRequest struct {
	JQL           string   `json:"jql"`
	MaxResults    int      `json:"maxResults"`
	Fields        []string `json:"fields"`
	NextPageToken string   `json:"nextPageToken,omitempty"`
}

// IssueLinkRequest represents an issue link creation request.
type IssueLinkRequest struct {
	Type         IssueLinkType `json:"type"`
	InwardIssue  IssueRef      `json:"inwardIssue"`
	OutwardIssue IssueRef      `json:"outwardIssue"`
	Comment      *LinkComment  `json:"comment,omitempty"`
}

// IssueLinkType represents the type of link.
type IssueLinkType struct {
	Name string `json:"name"`
}

// IssueRef represents an issue reference.
type IssueRef struct {
	Key string `json:"key"`
}

// LinkComment represents a comment on a link. Body is a string for Jira Data
// Center (wiki markup) and an ADF document (map[string]any) for Jira Cloud.
type LinkComment struct {
	Body any `json:"body"`
}

// CreateIssue creates a new issue in Jira.
func (c *Client) CreateIssue(ctx context.Context, req *CreateIssueRequest) (*CreateIssueResponse, error) {
	endpoint := fmt.Sprintf("/rest/api/%s/issue", c.apiVersion)

	var resp CreateIssueResponse
	if err := c.doRequest(ctx, http.MethodPost, endpoint, req, &resp); err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}

	return &resp, nil
}

// UpdateIssueRequest represents an update issue request.
type UpdateIssueRequest struct {
	Fields map[string]any `json:"fields"`
}

// UpdateIssue updates an existing issue by key.
// Returns an error if issueKey is not a valid Jira issue key.
func (c *Client) UpdateIssue(ctx context.Context, issueKey string, req *UpdateIssueRequest) error {
	if !IsJiraKey(issueKey) {
		return fmt.Errorf("invalid issue key %q: must match PROJECT-NUMBER format (e.g. PROJ-123)", issueKey)
	}
	endpoint := fmt.Sprintf("/rest/api/%s/issue/%s", c.apiVersion, issueKey)
	if err := c.doRequest(ctx, http.MethodPut, endpoint, req, nil); err != nil {
		return fmt.Errorf("update issue %s: %w", issueKey, err)
	}
	return nil
}

// CreateIssueLink creates a link between two issues.
func (c *Client) CreateIssueLink(ctx context.Context, req *IssueLinkRequest) error {
	endpoint := fmt.Sprintf("/rest/api/%s/issueLink", c.apiVersion)
	if err := c.doRequest(ctx, http.MethodPost, endpoint, req, nil); err != nil {
		return fmt.Errorf("create issue link: %w", err)
	}
	return nil
}

// doRequest performs an HTTP request with retry logic for transient failures.
func (c *Client) doRequest(ctx context.Context, method, endpoint string, body any, result any) error {
	var bodyReader io.Reader
	var bodyBytes []byte

	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
	}

	// Parse and validate the URL before the retry loop. Re-serialising via
	// url.URL.String() produces a sanitised value that breaks the taint chain
	// gosec tracks from the user-supplied baseURL, satisfying G107/G704.
	parsedURL, urlErr := url.Parse(c.baseURL + endpoint)
	if urlErr != nil {
		return fmt.Errorf("invalid request URL: %w", urlErr)
	}
	if parsedURL.Scheme != "https" && parsedURL.Scheme != "http" {
		return fmt.Errorf("disallowed URL scheme %q: only https (and loopback http) are permitted", parsedURL.Scheme)
	}
	safeURL := parsedURL.String()

	authHeader := c.authHeader()
	var lastErr error
	rateLimited := false // true when previous attempt got a 429 and already slept

	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 && !rateLimited {
			// Exponential backoff: 1s, 4s, 9s
			delay := time.Duration(attempt*attempt) * c.RetryDelay
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		rateLimited = false

		// Reset body reader for each attempt
		if bodyBytes != nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}

		req, err := http.NewRequestWithContext(ctx, method, safeURL, bodyReader)
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", authHeader)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			// Per Go docs, resp may be non-nil even when err is non-nil (e.g.
			// redirect errors). Close the body to avoid leaking file descriptors.
			if resp != nil && resp.Body != nil {
				if closeErr := resp.Body.Close(); closeErr != nil {
					// Absorb secondary close error into the request error.
					err = fmt.Errorf("%w (close body: %w)", err, closeErr)
				}
			}
			lastErr = fmt.Errorf("execute request: %w", err)
			continue
		}

		// resp.Body is read and closed explicitly (not deferred) because this
		// retry loop may create multiple responses; defer would leak bodies across attempts.
		// io.LimitReader prevents excessive memory use on unexpectedly large responses.
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
		if closeErr := resp.Body.Close(); closeErr != nil && readErr == nil {
			readErr = closeErr
		}
		_ = readErr // error reading response body is non-fatal; we use what we have

		// Success
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			// Jira does not always reject bad credentials with a 401: some endpoints
			// (e.g. Cloud search/jql) answer 200 and run the request anonymously,
			// flagging the failed login only in X-Seraph-LoginReason. Fail closed so
			// an anonymous (typically empty) result is never mistaken for real data.
			if seraphLoginFailed(resp.Header) {
				return &APIError{
					StatusCode: http.StatusUnauthorized,
					Body:       "Jira rejected the credentials and answered the request anonymously (X-Seraph-LoginReason: " + resp.Header.Get("X-Seraph-LoginReason") + ")",
				}
			}
			if result != nil && len(respBody) > 0 {
				if err := json.Unmarshal(respBody, result); err != nil {
					return fmt.Errorf("parse response: %w", err)
				}
			}
			return nil
		}

		// Rate limiting - retry using server's Retry-After guidance only
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := c.parseRetryAfter(resp.Header.Get("Retry-After"))
			delay := time.Duration(retryAfter) * time.Second
			lastErr = fmt.Errorf("rate limited (429); waiting %d seconds", retryAfter)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
			rateLimited = true
			continue
		}

		// Client errors - don't retry
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return &APIError{
				StatusCode: resp.StatusCode,
				Body:       string(respBody),
			}
		}

		// Server errors - retry
		if resp.StatusCode >= 500 {
			body := string(respBody)
			if len(body) > 500 {
				body = body[:500] + "... (truncated)"
			}
			lastErr = fmt.Errorf("server error (%d): %s", resp.StatusCode, body)
			continue
		}
	}

	return fmt.Errorf("request failed after %d attempts: %w", c.MaxRetries+1, lastErr)
}

// seraphLoginFailed reports whether Jira's X-Seraph-LoginReason response header
// says the supplied credentials were rejected (AUTHENTICATED_FAILED) or not
// accepted (AUTHENTICATION_DENIED, e.g. CAPTCHA required).
func seraphLoginFailed(h http.Header) bool {
	reason := strings.ToUpper(strings.Join(h.Values("X-Seraph-LoginReason"), ","))
	return strings.Contains(reason, "AUTHENTICATED_FAILED") || strings.Contains(reason, "AUTHENTICATION_DENIED")
}

// authHeader returns the Authorization header value for the client's mode.
// Cloud mode uses Basic base64(email:token); Data Center mode uses Bearer token.
func (c *Client) authHeader() string {
	if c.mode == ModeCloud {
		raw := c.creds.Email + ":" + c.creds.Token
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(raw))
	}
	return "Bearer " + c.creds.Token
}

// APIError represents a Jira API error.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	body := e.Body
	if len(body) > 500 {
		body = body[:500] + "... (truncated)"
	}
	return fmt.Sprintf("API error (%d): %s", e.StatusCode, body)
}

// BuildIssueFields builds the fields map for issue creation.
func BuildIssueFields(projectKey string, fields map[string]any) map[string]any {
	result := make(map[string]any)

	result["project"] = map[string]any{
		"key": projectKey,
	}

	maps.Copy(result, fields)

	return result
}

// nameField is the JSON key Jira uses to reference a named entity
// (issue type, priority, user, component, version) by its name.
const nameField = "name"

// FormatIssueType formats an issue type for the Jira API.
func FormatIssueType(name string) map[string]any {
	return map[string]any{nameField: name}
}

// FormatPriority formats a priority for the Jira API.
func FormatPriority(name string) map[string]any {
	return map[string]any{nameField: name}
}

// FormatUser formats a user for the Jira API.
func FormatUser(username string) map[string]any {
	return map[string]any{nameField: username}
}

// FormatComponent formats a component for the Jira API.
func FormatComponent(name string) map[string]any {
	return map[string]any{nameField: name}
}

// FormatVersion formats a version for the Jira API.
func FormatVersion(name string) map[string]any {
	return map[string]any{nameField: name}
}

// Epic represents a Jira Epic with its key fields.
type Epic struct {
	Key         string `json:"key"`
	Summary     string `json:"summary"`
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
}

// Field represents a Jira field definition returned by the /rest/api/2/field endpoint.
type Field struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Custom bool         `json:"custom"`
	Schema *FieldSchema `json:"schema,omitempty"`
}

// FieldSchema describes the schema of a Jira field.
type FieldSchema struct {
	Type     string `json:"type"`
	Custom   string `json:"custom,omitempty"`
	CustomID int    `json:"customId,omitempty"`
}

// FetchFields retrieves all field definitions from the Jira instance.
// This is useful for discovering custom field IDs (e.g. finding the correct
// epic link field ID for your Jira Data Center configuration).
func (c *Client) FetchFields(ctx context.Context) ([]Field, error) {
	endpoint := fmt.Sprintf("/rest/api/%s/field", c.apiVersion)

	var fields []Field
	if err := c.doRequest(ctx, http.MethodGet, endpoint, nil, &fields); err != nil {
		return nil, fmt.Errorf("fetch fields: %w", err)
	}

	return fields, nil
}

// IssueLinkTypeInfo represents a link type returned by the Jira REST API.
// Each link type has an official name (e.g. "Blocks") and two directional
// descriptions: inward (e.g. "is blocked by") and outward (e.g. "blocks").
type IssueLinkTypeInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Inward  string `json:"inward"`
	Outward string `json:"outward"`
}

// issueLinkTypesResponse wraps the response from GET /rest/api/2/issueLinkType.
type issueLinkTypesResponse struct {
	IssueLinkTypes []IssueLinkTypeInfo `json:"issueLinkTypes"`
}

// FetchIssueLinkTypes retrieves all issue link types available on the instance.
// This is useful for discovering the correct link type names and their
// inward/outward descriptions before creating issue links.
func (c *Client) FetchIssueLinkTypes(ctx context.Context) ([]IssueLinkTypeInfo, error) {
	endpoint := fmt.Sprintf("/rest/api/%s/issueLinkType", c.apiVersion)

	var resp issueLinkTypesResponse
	if err := c.doRequest(ctx, http.MethodGet, endpoint, nil, &resp); err != nil {
		return nil, fmt.Errorf("fetch issue link types: %w", err)
	}

	return resp.IssueLinkTypes, nil
}

// FetchEpics retrieves all existing Epics for the given project key.
// If statusFilter is non-empty, only epics matching that status are returned.
// Prefix the status with "NOT:" to negate it (e.g. "NOT:Done" means status != Done).
// It paginates through results automatically and returns all epics found.
// On Jira Cloud it uses the search/jql endpoint with nextPageToken pagination and
// flattens ADF descriptions to plain text; on Data Center it uses the legacy
// /search endpoint with startAt/total pagination and string descriptions.
// Returns an error if projectKey is not a valid Jira project key.
func (c *Client) FetchEpics(ctx context.Context, projectKey, statusFilter string) ([]Epic, error) {
	if !projectKeyRE.MatchString(projectKey) {
		return nil, fmt.Errorf("invalid project key %q: must match ^[A-Z][A-Z0-9]+$", projectKey)
	}

	jql := buildEpicJQL(projectKey, statusFilter)

	// Deliberate two-path dispatch: Cloud uses the POST search/jql endpoint with
	// nextPageToken pagination and ADF description flattening; Data Center uses the
	// legacy GET /search endpoint with startAt/total pagination and string
	// descriptions. These paths diverge enough to warrant separate helpers rather
	// than a shared abstraction. If additional endpoints diverge by mode, extract
	// a mode-specific search strategy interface instead.
	if c.mode == ModeCloud {
		return c.fetchEpicsCloud(ctx, jql)
	}

	const pageSize = 50

	// Pre-compute the endpoint and base query parameters; only startAt changes per page.
	endpoint := fmt.Sprintf("/rest/api/%s/search", c.apiVersion)
	params := url.Values{}
	params.Set("jql", jql)
	params.Set("maxResults", strconv.Itoa(pageSize))
	params.Set("fields", strings.Join(epicFields, ","))

	epics := make([]Epic, 0, pageSize)
	startAt := 0

	for {
		params.Set("startAt", strconv.Itoa(startAt))
		fullURL := endpoint + "?" + params.Encode()

		var result SearchResult
		if err := c.doRequest(ctx, http.MethodGet, fullURL, nil, &result); err != nil {
			return nil, fmt.Errorf("fetch epics: %w", err)
		}

		for _, issue := range result.Issues {
			epic := Epic{Key: issue.Key}

			if s, ok := issue.Fields["summary"].(string); ok {
				epic.Summary = s
			}
			if status, ok := issue.Fields["status"].(map[string]any); ok {
				if name, ok := status["name"].(string); ok {
					epic.Status = name
				}
			}
			if desc, ok := issue.Fields["description"].(string); ok {
				epic.Description = desc
			}

			epics = append(epics, epic)
		}

		startAt += len(result.Issues)
		if startAt >= result.Total || len(result.Issues) == 0 {
			break
		}
	}

	return epics, nil
}

// jqlQuote escapes a string for use as a JQL string literal and returns it
// double-quoted. It escapes backslashes first, then double-quote characters, per
// the Jira JQL string rules. Example: jqlQuote(`a"b\c`) → `"a\"b\\c"`.
func jqlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// buildEpicJQL constructs the JQL used to list a project's epics, shared by the
// Cloud and Data Center paths so both platforms issue identical queries. The
// statusFilter accepts a plain status ("Done" → status = "Done") or a "NOT:"
// prefixed status ("NOT:Done" → status != "Done"); an empty filter omits the
// status clause. Results are ordered by created date, descending.
func buildEpicJQL(projectKey, statusFilter string) string {
	jql := `project = ` + jqlQuote(projectKey) + ` AND issuetype = Epic`
	if statusFilter != "" {
		if after, ok := strings.CutPrefix(statusFilter, "NOT:"); ok {
			jql += ` AND status != ` + jqlQuote(after)
		} else {
			jql += ` AND status = ` + jqlQuote(statusFilter)
		}
	}
	jql += ` ORDER BY created DESC`
	return jql
}

// fetchEpicsCloud fetches epics from the Jira Cloud search/jql endpoint. It
// paginates with nextPageToken and terminates on the FIRST of: IsLast true, an
// absent/empty next token, an empty page, or a page that yields no new issue keys
// (a guard against the endpoint's known page-repeat quirk — there is no "total"
// to rely on). Cloud descriptions arrive as ADF objects and are flattened to
// plain text.
func (c *Client) fetchEpicsCloud(ctx context.Context, jql string) ([]Epic, error) {
	const pageSize = 50
	endpoint := fmt.Sprintf("/rest/api/%s/search/jql", c.apiVersion)

	epics := make([]Epic, 0, pageSize)
	seen := make(map[string]bool)
	token := ""

	for {
		reqBody := cloudSearchRequest{
			JQL:           jql,
			MaxResults:    pageSize,
			Fields:        epicFields,
			NextPageToken: token,
		}

		var result cloudSearchResult
		if err := c.doRequest(ctx, http.MethodPost, endpoint, reqBody, &result); err != nil {
			return nil, fmt.Errorf("fetch epics (cloud): %w", err)
		}

		newKeys := 0
		for _, issue := range result.Issues {
			if seen[issue.Key] {
				continue
			}
			seen[issue.Key] = true
			newKeys++

			epic := Epic{Key: issue.Key}
			if s, ok := issue.Fields["summary"].(string); ok {
				epic.Summary = s
			}
			if status, ok := issue.Fields["status"].(map[string]any); ok {
				if name, ok := status["name"].(string); ok {
					epic.Status = name
				}
			}
			epic.Description = cloudDescriptionText(issue.Fields["description"])

			epics = append(epics, epic)
		}

		// Terminate on: last page, no next token, empty page, or no new keys
		// (page-repeat guard). Never rely on a count — Cloud returns none.
		if result.IsLast || result.NextPageToken == "" || len(result.Issues) == 0 || newKeys == 0 {
			break
		}
		token = result.NextPageToken
	}

	return epics, nil
}

// cloudDescriptionText renders a Cloud issue's description field to plain text.
// Cloud (v3) returns an ADF object (map[string]any), which is flattened via
// adf.ADFToText; a plain string is returned unchanged; anything else yields "".
func cloudDescriptionText(desc any) string {
	switch d := desc.(type) {
	case string:
		return d
	case map[string]any:
		return adf.ADFToText(d)
	default:
		return ""
	}
}

// User represents a Jira user as returned by the Cloud user-search endpoint.
// AccountID is the Cloud account identifier sent as {"id": accountId} in issue
// fields; EmailAddress and DisplayName are informational.
type User struct {
	AccountID    string `json:"accountId"`
	EmailAddress string `json:"emailAddress"`
	DisplayName  string `json:"displayName"`
}

// SearchUsers queries the Jira Cloud user-search endpoint for users matching the
// given query (typically an email address) and returns the matching users. It
// follows the client's standard retry/error conventions via doRequest; a non-2xx
// response surfaces as a *APIError. This endpoint is Cloud-only (API v3); Data
// Center callers must not call it.
func (c *Client) SearchUsers(ctx context.Context, query string) ([]User, error) {
	endpoint := fmt.Sprintf("/rest/api/%s/user/search", c.apiVersion)
	params := url.Values{}
	params.Set("query", query)
	fullURL := endpoint + "?" + params.Encode()

	var users []User
	if err := c.doRequest(ctx, http.MethodGet, fullURL, nil, &users); err != nil {
		return nil, fmt.Errorf("search users: %w", err)
	}

	return users, nil
}

// parseRetryAfter parses the Retry-After header (RFC 7231).
// Returns seconds to wait (1 to MaxRetryAfterSecs), or 1 if unparseable.
// MaxRetryAfterSecs defaults to 60 when zero or negative.
func (c *Client) parseRetryAfter(header string) int {
	maxWait := c.MaxRetryAfterSecs
	if maxWait <= 0 {
		maxWait = 60
	}

	if header == "" {
		return 1 // default retry delay
	}

	// Try parsing as decimal seconds
	if n, err := strconv.Atoi(header); err == nil && n > 0 {
		if n > maxWait {
			return maxWait
		}
		return n
	}

	// Try parsing as HTTP-date (e.g., "Wed, 21 Oct 2026 07:28:00 GMT")
	if t, err := time.Parse(time.RFC1123, header); err == nil {
		delta := int(time.Until(t).Seconds())
		if delta <= 0 {
			return 1
		}
		if delta > maxWait {
			return maxWait
		}
		return delta
	}

	// Fallback
	return 1
}
