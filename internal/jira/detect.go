// SPDX-License-Identifier: Apache-2.0

package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Probe policy (design D5/D6): the unauthenticated endpoint, the bound on the
// whole request, and the response body cap.
const (
	serverInfoPath    = "/rest/api/2/serverInfo"
	probeTimeout      = 5 * time.Second
	maxServerInfoBody = 64 << 10 // 64 KiB
)

// atlassianCloudHostRE is necessary but not sufficient: isAtlassianCloudBase also
// rejects a site label starting with "xn--" (punycode / IDN look-alike).
var atlassianCloudHostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?\.atlassian\.net$`)

// DetectionSource says how the platform mode was decided.
type DetectionSource int

const (
	// SourceHost means the configured host is *.atlassian.net; no probe was made.
	SourceHost DetectionSource = iota
	// SourceServerInfo means the unauthenticated /rest/api/2/serverInfo probe decided.
	SourceServerInfo
	// SourceOverride means the deprecated --cloud / JIRA_CLOUD override forced Cloud.
	SourceOverride
	// SourceFallback means the probe failed and Data Center was assumed.
	SourceFallback
)

// String returns a short human-readable description of the source, used in
// verbose and check output.
func (s DetectionSource) String() string {
	switch s {
	case SourceHost:
		return "atlassian.net host"
	case SourceServerInfo:
		return "serverInfo"
	case SourceOverride:
		return "forced by --cloud/JIRA_CLOUD (deprecated)"
	case SourceFallback:
		return "not detected, assumed"
	default:
		return "unknown"
	}
}

// Platform is the result of detection, resolved once per command run.
type Platform struct {
	Mode           Mode
	Source         DetectionSource
	ConfiguredURL  string // normalized JIRA_URL as given by the user
	APIBaseURL     string // base URL every authenticated request uses
	FallbackReason string // why the probe failed; set only for SourceFallback
}

// CustomDomain reports whether APIBaseURL differs from ConfiguredURL.
func (p Platform) CustomDomain() bool { return p.APIBaseURL != p.ConfiguredURL }

// DetectOption configures DetectPlatform's probe.
type DetectOption func(*detectConfig)

// detectConfig holds the probe's knobs. timeout defaults to probeTimeout.
type detectConfig struct {
	transport http.RoundTripper
	timeout   time.Duration
}

// newDetectConfig returns the defaults ({transport: nil, timeout: probeTimeout})
// with opts applied. DetectPlatform builds its config ONLY through this.
func newDetectConfig(opts ...DetectOption) detectConfig {
	cfg := detectConfig{timeout: probeTimeout}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// WithDetectTransport injects the RoundTripper used by the probe (test seam).
// DetectPlatform still builds its own http.Client around it: no redirects, no
// cookie jar, and the probe timeout.
func WithDetectTransport(rt http.RoundTripper) DetectOption {
	return func(c *detectConfig) {
		c.transport = rt
	}
}

// withProbeTimeout overrides the probe client's timeout. Unexported and used only
// by tests, so production code cannot shorten or disable the 5 s limit.
func withProbeTimeout(d time.Duration) DetectOption {
	return func(c *detectConfig) {
		c.timeout = d
	}
}

// DetectPlatform resolves the platform mode and the API base URL for rawURL.
// forceCloud is the deprecated --cloud / JIRA_CLOUD override.
//
// A *.atlassian.net host is Cloud without any network call. Any other host is
// probed with one unauthenticated GET /rest/api/2/serverInfo. When it reports
// Cloud, credentials may only follow its baseUrl after isAtlassianCloudBase and
// JIRA_ALLOWED_HOSTS accept it; otherwise the configured URL stays the API base.
// A failed probe is never an error: it yields Data Center (or Cloud when forced).
// Errors come only from an invalid configured URL and a fail-closed Cloud site.
func DetectPlatform(ctx context.Context, rawURL string, forceCloud bool, opts ...DetectOption) (Platform, error) {
	configured, err := normalizeURL(rawURL)
	if err != nil {
		return Platform{}, fmt.Errorf("invalid Jira URL: %w", err)
	}
	if configured == "" {
		return Platform{}, errors.New("jira URL is required")
	}
	host := hostOf(configured)

	same := Platform{ConfiguredURL: configured, APIBaseURL: configured}

	if strings.HasSuffix(host, cloudHostSuffix) {
		same.Mode = ModeCloud
		same.Source = sourceOr(forceCloud, SourceHost)
		return same, nil
	}

	info, reason := probeServerInfo(ctx, configured, newDetectConfig(opts...))

	switch {
	case reason == "" && strings.EqualFold(info.DeploymentType, "Cloud"):
		canonical, ok := isAtlassianCloudBase(info.BaseURL)
		if !ok {
			// Never echo the raw baseUrl: it is untrusted input.
			return Platform{}, fmt.Errorf("site %s reports Jira Cloud but did not advertise a usable *.atlassian.net API address; set JIRA_URL to your https://<site>.atlassian.net address", host)
		}
		checked, nerr := normalizeURL(canonical)
		if nerr != nil {
			return Platform{}, fmt.Errorf("site %s reports Cloud API host %s (from its serverInfo), which is not in JIRA_ALLOWED_HOSTS; add it or set JIRA_URL to that host: %w", host, hostOf(canonical), nerr)
		}
		return Platform{
			Mode:          ModeCloud,
			Source:        sourceOr(forceCloud, SourceServerInfo),
			ConfiguredURL: configured,
			APIBaseURL:    checked,
		}, nil
	case forceCloud:
		same.Mode = ModeCloud
		same.Source = SourceOverride
		return same, nil
	case reason == "":
		same.Mode = ModeDataCenter
		same.Source = SourceServerInfo
		return same, nil
	default:
		same.Mode = ModeDataCenter
		same.Source = SourceFallback
		same.FallbackReason = reason
		return same, nil
	}
}

// sourceOr returns SourceOverride when the deprecated override is set, and
// detected otherwise.
func sourceOr(forceCloud bool, detected DetectionSource) DetectionSource {
	if forceCloud {
		return SourceOverride
	}
	return detected
}

// serverInfo holds the only two serverInfo fields that are decoded (Boundary B).
type serverInfo struct {
	DeploymentType string `json:"deploymentType"`
	BaseURL        string `json:"baseUrl"`
}

// probeServerInfo performs the unauthenticated GET <base>/rest/api/2/serverInfo.
// It returns the decoded fields and an empty reason on success, or a short
// failure reason that never contains response-body text. The request carries
// only an Accept header: no Authorization, no cookies, and no redirect is
// followed.
func probeServerInfo(ctx context.Context, base string, cfg detectConfig) (serverInfo, string) {
	rt := cfg.transport
	if rt == nil {
		// Read at call time so a test-wide guard on DefaultTransport applies.
		rt = http.DefaultTransport
	}
	client := &http.Client{
		Transport: rt,
		Timeout:   cfg.timeout,
		Jar:       nil,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Re-serialising via url.URL.String() mirrors doRequest's gosec sanitiser.
	u, err := url.Parse(base + serverInfoPath)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return serverInfo{}, "invalid probe URL"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return serverInfo{}, "invalid probe URL"
	}
	req.Header.Set("Accept", "application/json")

	// With CheckRedirect returning ErrUseLastResponse, Do never returns a
	// non-nil response together with an error.
	resp, err := client.Do(req)
	if err != nil {
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return serverInfo{}, "timed out"
		}
		return serverInfo{}, "connection failed"
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxServerInfoBody+1))
	if closeErr := resp.Body.Close(); closeErr != nil && readErr == nil {
		readErr = closeErr
	}

	switch code := resp.StatusCode; {
	case code >= 300 && code < 400:
		return serverInfo{}, fmt.Sprintf("redirect (HTTP %d) refused", code)
	case code < 200 || code >= 300:
		return serverInfo{}, fmt.Sprintf("HTTP %d", code)
	case readErr != nil:
		return serverInfo{}, "connection failed"
	case len(body) > maxServerInfoBody:
		return serverInfo{}, "response larger than 64 KiB"
	}

	var info serverInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return serverInfo{}, "response is not JSON"
	}
	return info, ""
}

// isAtlassianCloudBase validates a serverInfo baseUrl and returns the canonical
// origin "https://<host>" (rebuilt, never the raw string). It accepts only an
// https URL whose host is exactly one non-punycode label under .atlassian.net,
// with no userinfo, query, fragment, non-root path or non-default port. It is a
// host-shape check, not a tenant-ownership check.
func isAtlassianCloudBase(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", false
	}
	if u.Scheme != "https" {
		return "", false
	}
	if port := u.Port(); port != "" && port != "443" {
		return "", false
	}
	if u.Path != "" && u.Path != "/" {
		return "", false
	}
	// Reject any non-ASCII byte before lowercasing: strings.ToLower folds some
	// non-ASCII runes to ASCII (the Kelvin sign U+212A becomes 'k').
	hostname := u.Hostname()
	for i := range len(hostname) {
		if hostname[i] >= utf8.RuneSelf {
			return "", false
		}
	}
	host := strings.ToLower(hostname)
	if !atlassianCloudHostRE.MatchString(host) {
		return "", false
	}
	if strings.HasPrefix(strings.TrimSuffix(host, cloudHostSuffix), "xn--") {
		return "", false
	}
	return "https://" + host, true
}
