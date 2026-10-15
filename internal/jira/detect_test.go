// SPDX-License-Identifier: Apache-2.0

package jira

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// roundTripFunc adapts a function to http.RoundTripper (WithDetectTransport seam).
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// cannedServerInfo answers every request with status and body.
func cannedServerInfo(status int, body string) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})
}

// noProbeTransport fails the test if any probe request is made.
func noProbeTransport(t *testing.T) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("unexpected probe request to %s", r.URL)
		return nil, errors.New("probe not allowed in this test")
	})
}

// mustDetect runs DetectPlatform and fails the test when it returns an error.
func mustDetect(t *testing.T, ctx context.Context, rawURL string, forceCloud bool, opts ...DetectOption) Platform {
	t.Helper()
	got, err := DetectPlatform(ctx, rawURL, forceCloud, opts...)
	if err != nil {
		t.Fatalf("DetectPlatform(%q, forceCloud=%v) error = %v, want nil", rawURL, forceCloud, err)
	}
	return got
}

// blockingServer starts a server whose handler blocks until the client goes
// away or the test ends. srv.Close is registered before close(release), and
// cleanups run last-in first-out, so release closes before Close waits on the
// handler.
func blockingServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv
}

const (
	companyURL     = "https://jira.company.example"
	customDomain   = "https://jira.custom.example"
	cloudSiteURL   = "https://acme.atlassian.net"
	serverBody     = `{"deploymentType":"Server"}`
	customCloudDoc = `{"deploymentType":"Cloud","baseUrl":"https://acme.atlassian.net"}`
)

func TestDetectionSource_String(t *testing.T) {
	cases := []struct {
		src  DetectionSource
		want string
	}{
		{SourceHost, "atlassian.net host"},
		{SourceServerInfo, "serverInfo"},
		{SourceOverride, "forced by --cloud/JIRA_CLOUD (deprecated)"},
		{SourceFallback, "not detected, assumed"},
		{DetectionSource(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.src.String(); got != tc.want {
			t.Errorf("DetectionSource(%d).String() = %q, want %q", int(tc.src), got, tc.want)
		}
	}
}

func TestPlatform_CustomDomain(t *testing.T) {
	cases := []struct {
		name string
		p    Platform
		want bool
	}{
		{"zero_platform", Platform{}, false},
		{"same_url", Platform{ConfiguredURL: cloudSiteURL, APIBaseURL: cloudSiteURL}, false},
		{"different_url", Platform{ConfiguredURL: customDomain, APIBaseURL: cloudSiteURL}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.CustomDomain(); got != tc.want {
				t.Errorf("%+v.CustomDomain() = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

func TestIsAtlassianCloudBase(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		want   string
		wantOK bool
	}{
		{"accept_plain", "https://acme.atlassian.net", "https://acme.atlassian.net", true},
		{"accept_mixed_case", "https://ACME.Atlassian.NET", "https://acme.atlassian.net", true},
		{"accept_root_path", "https://x.atlassian.net/", "https://x.atlassian.net", true},
		{"accept_default_port", "https://x.atlassian.net:443", "https://x.atlassian.net", true},
		{"reject_http_scheme", "http://x.atlassian.net", "", false},
		{"reject_other_host", "https://evil.com", "", false},
		{"reject_suffix_then_evil", "https://x.atlassian.net.evil.com", "", false},
		{"reject_atlassian_subdomain_of_example", "https://evil.atlassian.net.example.com", "", false},
		{"reject_missing_dot", "https://xatlassian.net", "", false},
		{"reject_no_site_label", "https://atlassian.net", "", false},
		{"reject_two_labels", "https://a.b.atlassian.net", "", false},
		{"reject_non_default_port", "https://x.atlassian.net:8443", "", false},
		{"reject_userinfo", "https://u:p@x.atlassian.net", "", false},
		{"reject_path", "https://x.atlassian.net/jira", "", false},
		{"reject_query", "https://x.atlassian.net?q=1", "", false},
		{"reject_empty_query", "https://x.atlassian.net?", "", false},
		{"reject_fragment", "https://x.atlassian.net#f", "", false},
		{"reject_punycode_host", "https://xn--80ak6aa92e.com", "", false},
		{"reject_punycode_lookalike", "https://xn--atlassian-9za.net", "", false},
		{"reject_punycode_site_label", "https://xn--80ak6aa92e.atlassian.net", "", false},
		{"reject_cyrillic_a", "https://x.\u0430tlassian.net", "", false},
		{"reject_bidi_override", "https://x\u202e.atlassian.net", "", false},
		{"reject_kelvin_sign_folds_to_ascii", "https://\u212aacme.atlassian.net", "", false},
		{"reject_empty", "", "", false},
		{"reject_not_a_url", "not a url", "", false},
		{"reject_opaque", "https:x", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := isAtlassianCloudBase(tc.raw)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("isAtlassianCloudBase(%q) = (%q, %v), want (%q, %v)", tc.raw, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestDetectPlatform_ProbeSendsNoCredentials(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")

	type seenRequest struct{ method, path, auth, cookie, accept string }
	var (
		mu   sync.Mutex
		seen []seenRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, seenRequest{
			method: r.Method,
			path:   r.URL.Path,
			auth:   r.Header.Get("Authorization"),
			cookie: r.Header.Get("Cookie"),
			accept: r.Header.Get("Accept"),
		})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		writeBytes(t, w, []byte(serverBody))
	}))
	t.Cleanup(srv.Close)

	cases := []struct {
		name       string
		configured string
	}{
		{"configured_url", srv.URL},
		// normalizeURL strips the userinfo (and warns on stderr), so Go's client
		// has no URL userinfo from which to derive Basic auth.
		{"configured_url_with_userinfo", strings.Replace(srv.URL, "http://", "http://u:p@", 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			seen = nil
			mu.Unlock()

			// No options: the production default transport is used.
			got := mustDetect(t, context.Background(), tc.configured, false)

			mu.Lock()
			reqs := append([]seenRequest(nil), seen...)
			mu.Unlock()
			if len(reqs) != 1 {
				t.Fatalf("server saw %d requests, want exactly 1: %+v", len(reqs), reqs)
			}
			r := reqs[0]
			if r.method != http.MethodGet {
				t.Errorf("method = %q, want %q", r.method, http.MethodGet)
			}
			if r.path != "/rest/api/2/serverInfo" {
				t.Errorf("path = %q, want /rest/api/2/serverInfo", r.path)
			}
			if r.auth != "" {
				t.Errorf("Authorization = %q, want none", r.auth)
			}
			if r.cookie != "" {
				t.Errorf("Cookie = %q, want none", r.cookie)
			}
			if r.accept != "application/json" {
				t.Errorf("Accept = %q, want application/json", r.accept)
			}
			want := Platform{Mode: ModeDataCenter, Source: SourceServerInfo, ConfiguredURL: srv.URL, APIBaseURL: srv.URL}
			if got != want {
				t.Errorf("DetectPlatform() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestDetectPlatform_RedirectIsNotFollowed(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	codes := []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect}
	for _, code := range codes {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			var hitsB atomic.Int32
			srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hitsB.Add(1)
				w.Header().Set("Content-Type", "application/json")
				writeBytes(t, w, []byte(`{"deploymentType":"Cloud","baseUrl":"https://x.atlassian.net"}`))
			}))
			t.Cleanup(srvB.Close)
			srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, srvB.URL+"/rest/api/2/serverInfo", code)
			}))
			t.Cleanup(srvA.Close)

			got := mustDetect(t, context.Background(), srvA.URL, false)

			if got.Source != SourceFallback || got.Mode != ModeDataCenter {
				t.Errorf("DetectPlatform() = %+v, want SourceFallback / Data Center", got)
			}
			if !strings.Contains(got.FallbackReason, "redirect") {
				t.Errorf("FallbackReason = %q, want it to mention the redirect", got.FallbackReason)
			}
			if n := hitsB.Load(); n != 0 {
				t.Errorf("redirect target was contacted %d times, want 0", n)
			}
		})
	}
}

func TestDetectPlatform_OversizedBodyFallsBack(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	body := `{"deploymentType":"Cloud","baseUrl":"https://x.atlassian.net","pad":"` + strings.Repeat("a", 64<<10) + `"}`

	got := mustDetect(t, context.Background(), companyURL, false,
		WithDetectTransport(cannedServerInfo(http.StatusOK, body)))

	want := Platform{
		Mode:           ModeDataCenter,
		Source:         SourceFallback,
		ConfiguredURL:  companyURL,
		APIBaseURL:     companyURL,
		FallbackReason: "response larger than 64 KiB",
	}
	if got != want {
		t.Errorf("DetectPlatform() = %+v, want %+v", got, want)
	}
}

func TestDetectPlatform_BadResponsesFallBack(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	const marker = "SECRET-BODY-MARKER"
	cases := []struct {
		name       string
		status     int
		body       string
		wantReason string
	}{
		{"html_200", http.StatusOK, "<html>" + marker + "</html>", "response is not JSON"},
		{"401", http.StatusUnauthorized, marker, "HTTP 401"},
		{"403", http.StatusForbidden, marker, "HTTP 403"},
		{"404", http.StatusNotFound, marker, "HTTP 404"},
		{"500", http.StatusInternalServerError, marker, "HTTP 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mustDetect(t, context.Background(), companyURL, false,
				WithDetectTransport(cannedServerInfo(tc.status, tc.body)))

			if got.Source != SourceFallback || got.Mode != ModeDataCenter {
				t.Errorf("DetectPlatform() = %+v, want SourceFallback / Data Center", got)
			}
			if strings.Contains(got.FallbackReason, marker) {
				t.Errorf("FallbackReason = %q leaks the response body", got.FallbackReason)
			}
			if got.FallbackReason != tc.wantReason {
				t.Errorf("FallbackReason = %q, want %q", got.FallbackReason, tc.wantReason)
			}
		})
	}
}

func TestDetectPlatform_TimeoutFallsBack(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	srv := blockingServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	got := mustDetect(t, ctx, srv.URL, false)
	elapsed := time.Since(start)

	if got.Source != SourceFallback || got.FallbackReason != "timed out" {
		t.Errorf("DetectPlatform() = %+v, want SourceFallback with reason %q", got, "timed out")
	}
	if elapsed >= 3*time.Second {
		t.Errorf("DetectPlatform took %v, want under 3s", elapsed)
	}
}

func TestDetectPlatform_ClientTimeoutFires(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	srv := blockingServer(t)

	// No context deadline: only the probe client's own timeout can end the request.
	start := time.Now()
	got := mustDetect(t, context.Background(), srv.URL, false, withProbeTimeout(200*time.Millisecond))
	elapsed := time.Since(start)

	if got.Source != SourceFallback || got.FallbackReason != "timed out" {
		t.Errorf("DetectPlatform() = %+v, want SourceFallback with reason %q", got, "timed out")
	}
	if elapsed >= 3*time.Second {
		t.Errorf("DetectPlatform took %v, want under 3s", elapsed)
	}
}

func TestDetectPlatform_ConnectionFailureFallsBack(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	srv := httptest.NewServer(http.NotFoundHandler())
	closedURL := srv.URL
	srv.Close()

	got := mustDetect(t, context.Background(), closedURL, false)

	if got.Source != SourceFallback || got.FallbackReason != "connection failed" {
		t.Errorf("DetectPlatform() = %+v, want SourceFallback with reason %q", got, "connection failed")
	}
}

func TestDetectPlatform_NonHTTPSchemeFallsBack(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	const configured = "ftp://jira.company.example"

	got := mustDetect(t, context.Background(), configured, false, WithDetectTransport(noProbeTransport(t)))

	want := Platform{
		Mode:           ModeDataCenter,
		Source:         SourceFallback,
		ConfiguredURL:  configured,
		APIBaseURL:     configured,
		FallbackReason: "invalid probe URL",
	}
	if got != want {
		t.Errorf("DetectPlatform() = %+v, want %+v", got, want)
	}
}

func TestDetectPlatform_ProbeLimits(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	if probeTimeout != 5*time.Second {
		t.Errorf("probeTimeout = %v, want 5s", probeTimeout)
	}
	if maxServerInfoBody != 64*1024 {
		t.Errorf("maxServerInfoBody = %d, want %d", maxServerInfoBody, 64*1024)
	}
	if got := newDetectConfig().timeout; got != probeTimeout {
		t.Errorf("newDetectConfig().timeout = %v, want probeTimeout (%v)", got, probeTimeout)
	}
}

func TestDetectPlatform_CloudCustomDomain(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	cases := []struct {
		name           string
		forceCloud     bool
		deploymentType string
		wantSource     DetectionSource
	}{
		{"detected", false, "Cloud", SourceServerInfo},
		{"forced", true, "Cloud", SourceOverride},
		{"lowercase_cloud", false, "cloud", SourceServerInfo},
		{"uppercase_cloud", false, "CLOUD", SourceServerInfo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"deploymentType":"` + tc.deploymentType + `","baseUrl":"https://acme.atlassian.net"}`
			got := mustDetect(t, context.Background(), customDomain, tc.forceCloud,
				WithDetectTransport(cannedServerInfo(http.StatusOK, body)))

			want := Platform{Mode: ModeCloud, Source: tc.wantSource, ConfiguredURL: customDomain, APIBaseURL: cloudSiteURL}
			if got != want {
				t.Errorf("DetectPlatform() = %+v, want %+v", got, want)
			}
			if !got.CustomDomain() {
				t.Errorf("CustomDomain() = false, want true")
			}
		})
	}
}

func TestDetectPlatform_CloudRejectedBaseURLFailsClosed(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	bodies := []struct {
		name    string
		body    string
		baseURL string // raw advertised baseUrl, which must never be echoed
	}{
		{"foreign_host", `{"deploymentType":"Cloud","baseUrl":"https://evil.example.com"}`, "https://evil.example.com"},
		{"base_url_absent", `{"deploymentType":"Cloud"}`, ""},
		{"http_scheme", `{"deploymentType":"Cloud","baseUrl":"http://x.atlassian.net"}`, "http://x.atlassian.net"},
	}
	for _, forceCloud := range []bool{false, true} {
		for _, b := range bodies {
			t.Run(b.name+"/forced="+strconv.FormatBool(forceCloud), func(t *testing.T) {
				got, err := DetectPlatform(context.Background(), customDomain, forceCloud,
					WithDetectTransport(cannedServerInfo(http.StatusOK, b.body)))
				if err == nil {
					t.Fatalf("DetectPlatform() = %+v, nil; want a fail-closed error", got)
				}
				for _, want := range []string{"did not advertise a usable *.atlassian.net API address", "jira.custom.example"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error = %q, want it to contain %q", err, want)
					}
				}
				if b.baseURL != "" && strings.Contains(err.Error(), b.baseURL) {
					t.Errorf("error = %q echoes the untrusted baseUrl %q", err, b.baseURL)
				}
				if got != (Platform{}) {
					t.Errorf("Platform = %+v, want the zero Platform on error", got)
				}
			})
		}
	}
}

func TestDetectPlatform_DerivedHostNotAllowedFailsClosed(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")

	t.Run("derived_host_not_allowed", func(t *testing.T) {
		t.Setenv("JIRA_ALLOWED_HOSTS", "jira.custom.example")
		got, err := DetectPlatform(context.Background(), customDomain, false,
			WithDetectTransport(cannedServerInfo(http.StatusOK, customCloudDoc)))
		if err == nil {
			t.Fatalf("DetectPlatform() = %+v, nil; want a JIRA_ALLOWED_HOSTS error", got)
		}
		for _, want := range []string{"acme.atlassian.net", "JIRA_ALLOWED_HOSTS", "jira.custom.example"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to contain %q", err, want)
			}
		}
		if got != (Platform{}) {
			t.Errorf("Platform = %+v, want the zero Platform on error", got)
		}
	})

	t.Run("derived_host_allowed", func(t *testing.T) {
		t.Setenv("JIRA_ALLOWED_HOSTS", "jira.custom.example,acme.atlassian.net")
		got := mustDetect(t, context.Background(), customDomain, false,
			WithDetectTransport(cannedServerInfo(http.StatusOK, customCloudDoc)))
		want := Platform{Mode: ModeCloud, Source: SourceServerInfo, ConfiguredURL: customDomain, APIBaseURL: cloudSiteURL}
		if got != want {
			t.Errorf("DetectPlatform() = %+v, want %+v", got, want)
		}
	})
}

func TestDetectPlatform_AtlassianHostSkipsProbe(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	const siteURL = "https://acme.atlassian.net"
	cases := []struct {
		name       string
		forceCloud bool
		wantSource DetectionSource
	}{
		{"detected", false, SourceHost},
		{"forced", true, SourceOverride},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mustDetect(t, context.Background(), siteURL, tc.forceCloud, WithDetectTransport(noProbeTransport(t)))
			want := Platform{Mode: ModeCloud, Source: tc.wantSource, ConfiguredURL: siteURL, APIBaseURL: siteURL}
			if got != want {
				t.Errorf("DetectPlatform() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestDetectPlatform_DataCenterDeploymentTypes(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	cases := []struct {
		name string
		body string
	}{
		{"server", serverBody},
		{"datacenter", `{"deploymentType":"DataCenter"}`},
		{"deployment_type_absent", `{"baseUrl":"https://jira.company.example"}`},
		// A non-Cloud site advertising an atlassian.net baseUrl must NOT redirect
		// credentials (plan gate P4a).
		{"server_with_atlassian_base_url", `{"deploymentType":"Server","baseUrl":"https://attacker.atlassian.net"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mustDetect(t, context.Background(), companyURL, false,
				WithDetectTransport(cannedServerInfo(http.StatusOK, tc.body)))
			want := Platform{Mode: ModeDataCenter, Source: SourceServerInfo, ConfiguredURL: companyURL, APIBaseURL: companyURL}
			if got != want {
				t.Errorf("DetectPlatform() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestDetectPlatform_ForcedProbeFailureIsCloudOverride(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	got := mustDetect(t, context.Background(), companyURL, true,
		WithDetectTransport(cannedServerInfo(http.StatusNotFound, "not found")))
	want := Platform{Mode: ModeCloud, Source: SourceOverride, ConfiguredURL: companyURL, APIBaseURL: companyURL}
	if got != want {
		t.Errorf("DetectPlatform() = %+v, want %+v", got, want)
	}
}

func TestDetectPlatform_ForcedNonCloudIsCloudOverride(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	cases := []struct {
		name string
		body string
	}{
		{"server", serverBody},
		// The configured URL stays the API base, never the advertised host (P4a).
		{"server_with_atlassian_base_url", `{"deploymentType":"Server","baseUrl":"https://attacker.atlassian.net"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mustDetect(t, context.Background(), companyURL, true,
				WithDetectTransport(cannedServerInfo(http.StatusOK, tc.body)))
			want := Platform{Mode: ModeCloud, Source: SourceOverride, ConfiguredURL: companyURL, APIBaseURL: companyURL}
			if got != want {
				t.Errorf("DetectPlatform() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestDetectPlatform_InvalidConfiguredURL(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	cases := []struct {
		name string
		url  string
	}{
		{"non_loopback_http", "http://jira.company.example"},
		{"empty", ""},
		{"control_char", "https://evil\x01acme.atlassian.net"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DetectPlatform(context.Background(), tc.url, false, WithDetectTransport(noProbeTransport(t)))
			if err == nil {
				t.Fatalf("DetectPlatform(%q) = %+v, nil; want an error", tc.url, got)
			}
			if got != (Platform{}) {
				t.Errorf("Platform = %+v, want the zero Platform on error", got)
			}
		})
	}
}

// TestDetectPlatform_HostRules carries over the host-rule cases of the former
// TestResolveMode. Its override_false_forces_dc case is intentionally dropped:
// DetectPlatform has no Data Center override (design: no --datacenter flag).
// The canned transport means none of the probed hosts is ever contacted.
func TestDetectPlatform_HostRules(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	canned := cannedServerInfo(http.StatusOK, serverBody)
	cases := []struct {
		name        string
		url         string
		forceCloud  bool
		probe       http.RoundTripper // nil: noProbeTransport
		wantMode    Mode
		wantSource  DetectionSource
		wantAPIBase string
	}{
		{"atlassian_net_auto_cloud", "https://acme.atlassian.net", false, nil, ModeCloud, SourceHost, "https://acme.atlassian.net"},
		{"atlassian_net_no_scheme", "acme.atlassian.net", false, nil, ModeCloud, SourceHost, "https://acme.atlassian.net"},
		{"atlassian_net_with_port", "https://acme.atlassian.net:443", false, nil, ModeCloud, SourceHost, "https://acme.atlassian.net:443"},
		{"atlassian_net_case_insensitive", "https://ACME.ATLASSIAN.NET", false, nil, ModeCloud, SourceHost, "https://ACME.ATLASSIAN.NET"},
		{"override_true_on_atlassian", "https://acme.atlassian.net", true, nil, ModeCloud, SourceOverride, "https://acme.atlassian.net"},
		{"company_hosted_probed_dc", companyURL, false, canned, ModeDataCenter, SourceServerInfo, companyURL},
		{"override_true_forces_cloud", companyURL, true, canned, ModeCloud, SourceOverride, companyURL},
		// Trailing-dot FQDN: host "acme.atlassian.net." does not end in
		// ".atlassian.net", so it is probed rather than host-matched.
		{"trailing_dot_fqdn", "https://acme.atlassian.net.", false, canned, ModeDataCenter, SourceServerInfo, "https://acme.atlassian.net."},
		// Suffix spoof: "evil-atlassian.net" ends in "-atlassian.net", not ".atlassian.net".
		{"suffix_spoof_dash", "https://evil-atlassian.net", false, canned, ModeDataCenter, SourceServerInfo, "https://evil-atlassian.net"},
		// Subdomain spoof: "atlassian.net.evil.example" does not end in ".atlassian.net".
		{"suffix_spoof_dot_evil", "https://atlassian.net.evil.example", false, canned, ModeDataCenter, SourceServerInfo, "https://atlassian.net.evil.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := tc.probe
			if rt == nil {
				rt = noProbeTransport(t)
			}
			got := mustDetect(t, context.Background(), tc.url, tc.forceCloud, WithDetectTransport(rt))
			if got.Mode != tc.wantMode || got.Source != tc.wantSource || got.APIBaseURL != tc.wantAPIBase {
				t.Errorf("DetectPlatform(%q, forceCloud=%v) = %+v, want mode %v, source %v, APIBaseURL %q",
					tc.url, tc.forceCloud, got, tc.wantMode, tc.wantSource, tc.wantAPIBase)
			}
		})
	}
}
