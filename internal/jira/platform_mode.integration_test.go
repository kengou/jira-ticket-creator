// SPDX-License-Identifier: Apache-2.0

package jira

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestIntegration_Mode_DetectPlatform realizes the @mode scenario: atlassian.net
// is Cloud without a probe, a company-hosted server that reports Data Center via
// serverInfo is Data Center, and the deprecated override forces Cloud.
func TestIntegration_Mode_DetectPlatform(t *testing.T) {
	t.Setenv("JIRA_ALLOWED_HOSTS", "")
	var sawAuth atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuth.Store(true)
		}
		encodeJSON(t, w, map[string]string{"deploymentType": "Server"})
	}))
	defer server.Close()

	cases := []struct {
		name       string
		url        string
		force      bool
		opts       []DetectOption
		wantMode   Mode
		wantSource DetectionSource
	}{
		{"atlassian_net_auto_cloud", "https://acme.atlassian.net", false, []DetectOption{WithDetectTransport(noProbeTransport(t))}, ModeCloud, SourceHost},
		{"company_hosted_auto_dc", server.URL, false, nil, ModeDataCenter, SourceServerInfo},
		{"override_forces_cloud", server.URL, true, nil, ModeCloud, SourceOverride},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := DetectPlatform(context.Background(), tc.url, tc.force, tc.opts...)
			if err != nil {
				t.Fatalf("DetectPlatform(%q) error = %v, want nil", tc.url, err)
			}
			if p.Mode != tc.wantMode {
				t.Errorf("DetectPlatform(%q) mode = %v, want %v", tc.url, p.Mode, tc.wantMode)
			}
			if p.Source != tc.wantSource {
				t.Errorf("DetectPlatform(%q) source = %v, want %v", tc.url, p.Source, tc.wantSource)
			}
			if p.APIBaseURL != p.ConfiguredURL {
				t.Errorf("APIBaseURL = %q, want it to equal ConfiguredURL %q", p.APIBaseURL, p.ConfiguredURL)
			}
		})
	}
	if sawAuth.Load() {
		t.Error("serverInfo probe carried an Authorization header")
	}
}

// TestIntegration_Auth_CloudBasic realizes the @auth scenario for Cloud:
// Cloud sends Authorization: Basic base64(email:token) to a v3 path.
func TestIntegration_Auth_CloudBasic(t *testing.T) {
	var gotAuth, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusCreated)
		encodeJSON(t, w, CreateIssueResponse{Key: "CLOUD-1"})
	}))
	defer server.Close()

	c, err := NewClientWithMode(server.URL, ModeCloud, Credentials{Email: "user@acme.com", Token: "tok"})
	if err != nil {
		t.Fatalf("NewClientWithMode: %v", err)
	}
	c.MaxRetries = 0

	if _, err := c.CreateIssue(context.Background(), &CreateIssueRequest{Fields: map[string]any{"summary": "x"}}); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user@acme.com:tok"))
	if gotAuth != wantAuth {
		t.Errorf("Authorization = %q, want %q", gotAuth, wantAuth)
	}
	if gotPath != "/rest/api/3/issue" {
		t.Errorf("path = %q, want /rest/api/3/issue", gotPath)
	}
}

// TestIntegration_Auth_DataCenterBearer realizes the @auth scenario for Data
// Center: Data Center sends Authorization: Bearer <token> to a v2 path.
func TestIntegration_Auth_DataCenterBearer(t *testing.T) {
	var gotAuth, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusCreated)
		encodeJSON(t, w, CreateIssueResponse{Key: "DC-1"})
	}))
	defer server.Close()

	c, err := NewClientWithMode(server.URL, ModeDataCenter, Credentials{Token: "pat-token"})
	if err != nil {
		t.Fatalf("NewClientWithMode: %v", err)
	}
	c.MaxRetries = 0

	if _, err := c.CreateIssue(context.Background(), &CreateIssueRequest{Fields: map[string]any{"summary": "x"}}); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	if gotAuth != "Bearer pat-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer pat-token")
	}
	if gotPath != "/rest/api/2/issue" {
		t.Errorf("path = %q, want /rest/api/2/issue", gotPath)
	}
}
