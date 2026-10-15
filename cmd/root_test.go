package cmd

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kengou/jira-ticket-creator/internal/jira"
)

// --- TestMain ---

func TestMain_ClearsJiraEnv(t *testing.T) {
	for _, k := range []string{"JIRA_URL", "JIRA_TOKEN", "JIRA_EMAIL", "JIRA_CLOUD", "JIRA_ALLOWED_HOSTS"} {
		if _, set := os.LookupEnv(k); set {
			t.Errorf("%s is set at test start; TestMain must unset it so no test depends on the developer's shell", k)
		}
	}
}

// --- maskToken ---

func TestMaskToken(t *testing.T) {
	cases := []struct {
		token string
		want  string
	}{
		{"", "****"},
		{"abc", "****"},
		{"abcd", "****"},
		{"abcde", "****"},                // 5 chars < 12, fully masked
		{"abcdefgh1234", "****1234"},     // exactly 12, show last 4
		{"abcdefgh12345678", "****5678"}, // 16 chars, show last 4
	}
	for _, tc := range cases {
		if got := maskToken(tc.token); got != tc.want {
			t.Errorf("maskToken(%q) = %q, want %q", tc.token, got, tc.want)
		}
	}
}

// --- validateFlags ---

func TestValidateFlags_MissingConfigFile(t *testing.T) {
	// Save and restore the global
	orig := configFile
	defer func() { configFile = orig }()

	configFile = ""
	err := validateFlags(false)
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestValidateFlags_NoAuthRequired(t *testing.T) {
	orig := configFile
	defer func() { configFile = orig }()

	configFile = "config.yaml"
	err := validateFlags(false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateFlags_AuthRequired_MissingURL(t *testing.T) {
	origCfg := configFile
	origURL := jiraURL
	origToken := jiraToken
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_TOKEN", "")
	defer func() {
		configFile = origCfg
		jiraURL = origURL
		jiraToken = origToken
	}()

	configFile = "config.yaml"
	jiraURL = ""
	jiraToken = "token"

	err := validateFlags(true)
	if err == nil {
		t.Fatal("expected error for missing Jira URL")
	}
}

func TestValidateFlags_AuthRequired_MissingToken(t *testing.T) {
	origCfg := configFile
	origURL := jiraURL
	origToken := jiraToken
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_TOKEN", "")
	defer func() {
		configFile = origCfg
		jiraURL = origURL
		jiraToken = origToken
	}()

	configFile = "config.yaml"
	jiraURL = "https://jira.example.com"
	jiraToken = ""

	err := validateFlags(true)
	if err == nil {
		t.Fatal("expected error for missing Jira token")
	}
}

func TestValidateFlags_AuthRequired_AllPresent(t *testing.T) {
	origCfg := configFile
	origURL := jiraURL
	origToken := jiraToken
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_TOKEN", "")
	defer func() {
		configFile = origCfg
		jiraURL = origURL
		jiraToken = origToken
	}()

	configFile = "config.yaml"
	jiraURL = "https://jira.example.com"
	jiraToken = "my-token"

	err := validateFlags(true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- email precedence and mode resolution ---

// resetAuthGlobals saves the auth-related package globals and restores them when
// the test ends.
func resetAuthGlobals(t *testing.T) {
	t.Helper()
	origURL, origToken, origEmail, origCloud := jiraURL, jiraToken, jiraEmail, isCloud
	origVerbose, origCfg, origPlatform := verbose, configFile, resolvedPlatform
	t.Cleanup(func() {
		jiraURL, jiraToken, jiraEmail, isCloud = origURL, origToken, origEmail, origCloud
		verbose, configFile, resolvedPlatform = origVerbose, origCfg, origPlatform
	})
}

// clearJiraEnv blanks every JIRA_* variable that requireAuth or the client
// reads, so a test never depends on the developer's shell. A test that needs
// one of them calls t.Setenv for it afterwards.
func clearJiraEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"JIRA_URL", "JIRA_TOKEN", "JIRA_EMAIL", "JIRA_CLOUD", "JIRA_ALLOWED_HOSTS"} {
		t.Setenv(name, "")
	}
}

// customDomainPlatform is a Cloud site on a custom domain whose serverInfo
// resolved it to its *.atlassian.net API base.
func customDomainPlatform() jira.Platform {
	return jira.Platform{
		Mode:          jira.ModeCloud,
		Source:        jira.SourceServerInfo,
		ConfiguredURL: "https://jira.custom.example",
		APIBaseURL:    "https://acme.atlassian.net",
	}
}

func TestRequireAuth_EmailFlagWinsOverEnv(t *testing.T) {
	resetAuthGlobals(t)
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_TOKEN", "")
	t.Setenv("JIRA_EMAIL", "env@example.com")
	t.Setenv("JIRA_CLOUD", "")

	jiraURL = "https://jira.company.com"
	jiraToken = "tok"
	jiraEmail = "flag@example.com"
	isCloud = false

	if err := requireAuth(); err != nil {
		t.Fatalf("requireAuth: %v", err)
	}
	if jiraEmail != "flag@example.com" {
		t.Errorf("jiraEmail = %q, want flag@example.com (flag wins)", jiraEmail)
	}
}

func TestRequireAuth_EmailEnvUsedWhenFlagEmpty(t *testing.T) {
	resetAuthGlobals(t)
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_TOKEN", "")
	t.Setenv("JIRA_EMAIL", "env@example.com")
	t.Setenv("JIRA_CLOUD", "")

	jiraURL = "https://acme.atlassian.net"
	jiraToken = "tok"
	jiraEmail = ""
	isCloud = false

	if err := requireAuth(); err != nil {
		t.Fatalf("requireAuth: %v", err)
	}
	if jiraEmail != "env@example.com" {
		t.Errorf("jiraEmail = %q, want env@example.com (env fallback)", jiraEmail)
	}
}

func TestRequireAuth_CloudAutoDetectedMissingEmailFailsFast(t *testing.T) {
	resetAuthGlobals(t)
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_TOKEN", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_CLOUD", "")

	jiraURL = "https://acme.atlassian.net" // auto-detects Cloud
	jiraToken = "tok"
	jiraEmail = ""
	isCloud = false

	err := requireAuth()
	if err == nil {
		t.Fatal("expected fail-fast error for Cloud without email")
	}
	if !strings.Contains(err.Error(), "JIRA_EMAIL") {
		t.Errorf("error = %q, want it to name JIRA_EMAIL", err.Error())
	}
}

func TestRequireAuth_CloudViaEnvOverrideMissingEmailFailsFast(t *testing.T) {
	resetAuthGlobals(t)
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_TOKEN", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_CLOUD", "true") // forces Cloud on a non-atlassian host

	jiraURL = "https://jira.company.com"
	jiraToken = "tok"
	jiraEmail = ""
	isCloud = false

	err := requireAuth()
	if err == nil {
		t.Fatal("expected fail-fast error for Cloud (env override) without email")
	}
	if resolvedPlatform.Mode != jira.ModeCloud {
		t.Errorf("resolvedPlatform.Mode = %v, want ModeCloud (JIRA_CLOUD override)", resolvedPlatform.Mode)
	}
}

func TestRequireAuth_CloudWithEmailSucceeds(t *testing.T) {
	resetAuthGlobals(t)
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_TOKEN", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_CLOUD", "")

	jiraURL = "https://acme.atlassian.net"
	jiraToken = "tok"
	jiraEmail = "user@acme.com"
	isCloud = false

	if err := requireAuth(); err != nil {
		t.Fatalf("requireAuth: %v", err)
	}
	if resolvedPlatform.Mode != jira.ModeCloud {
		t.Errorf("resolvedPlatform.Mode = %v, want ModeCloud", resolvedPlatform.Mode)
	}
}

func TestRequireAuth_DataCenterNoEmailRequired(t *testing.T) {
	resetAuthGlobals(t)
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_TOKEN", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_CLOUD", "")

	jiraURL = "https://jira.company.com"
	jiraToken = "tok"
	jiraEmail = ""
	isCloud = false

	if err := requireAuth(); err != nil {
		t.Fatalf("requireAuth (Data Center, no email): %v", err)
	}
	if resolvedPlatform.Mode != jira.ModeDataCenter {
		t.Errorf("resolvedPlatform.Mode = %v, want ModeDataCenter", resolvedPlatform.Mode)
	}
}

// --- newJiraClient: credentials go to the resolved API base ---

func TestNewJiraClient_UsesAPIBaseURL(t *testing.T) {
	resetAuthGlobals(t)
	clearJiraEnv(t)

	var gotAuth, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusCreated)
		if _, err := w.Write([]byte(`{"key":"CLOUD-1"}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer server.Close()

	// The configured URL must NOT be used: the offline transport would block it.
	jiraURL = "https://jira.custom.example"
	jiraToken = "tok"
	jiraEmail = "user@acme.com"
	resolvedPlatform = jira.Platform{
		Mode:          jira.ModeCloud,
		Source:        jira.SourceServerInfo,
		ConfiguredURL: "https://jira.custom.example",
		APIBaseURL:    server.URL,
	}

	c, err := newJiraClient()
	if err != nil {
		t.Fatalf("newJiraClient: %v", err)
	}
	c.MaxRetries = 0

	if _, err := c.CreateIssue(context.Background(), &jira.CreateIssueRequest{Fields: map[string]any{"summary": "x"}}); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("user@acme.com:tok"))
	if gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
	if gotPath != "/rest/api/3/issue" {
		t.Errorf("path = %q, want /rest/api/3/issue", gotPath)
	}
}

func TestNewJiraClient_RefusesUnresolvedPlatform(t *testing.T) {
	resetAuthGlobals(t)
	clearJiraEnv(t)

	jiraURL = "https://jira.company.example"
	jiraToken = "tok"
	resolvedPlatform = jira.Platform{}

	_, err := newJiraClient()
	if err == nil || !strings.Contains(err.Error(), "platform not resolved") {
		t.Fatalf("newJiraClient with an unresolved platform: err = %v, want it to contain %q", err, "platform not resolved")
	}
}

// --- requireAuth: platform detection ---

func TestRequireAuth_StoresDetectedPlatform(t *testing.T) {
	resetAuthGlobals(t)
	clearJiraEnv(t)
	want := customDomainPlatform()
	calls := stubDetect(t, want, nil)

	jiraURL = "https://jira.custom.example"
	jiraToken = "tok"
	jiraEmail = "user@acme.com"
	isCloud = false

	if err := requireAuth(); err != nil {
		t.Fatalf("requireAuth: %v", err)
	}
	if resolvedPlatform != want {
		t.Errorf("resolvedPlatform = %+v, want %+v", resolvedPlatform, want)
	}
	if len(*calls) != 1 {
		t.Fatalf("detectPlatform calls = %d, want 1", len(*calls))
	}
	if got := (*calls)[0]; got.rawURL != "https://jira.custom.example" || got.forceCloud {
		t.Errorf("detectPlatform call = %+v, want rawURL https://jira.custom.example and forceCloud false", got)
	}
}

func TestRequireAuth_ForceCloudFromFlag(t *testing.T) {
	resetAuthGlobals(t)
	clearJiraEnv(t)
	calls := stubDetect(t, jira.Platform{
		Mode:          jira.ModeCloud,
		Source:        jira.SourceOverride,
		ConfiguredURL: "https://jira.custom.example",
		APIBaseURL:    "https://jira.custom.example",
	}, nil)

	jiraURL = "https://jira.custom.example"
	jiraToken = "tok"
	jiraEmail = "user@acme.com"
	isCloud = true

	var err error
	_, stderr := captureOutput(t, func() { err = requireAuth() })
	if err != nil {
		t.Fatalf("requireAuth: %v", err)
	}
	if len(*calls) != 1 || !(*calls)[0].forceCloud {
		t.Errorf("detectPlatform calls = %+v, want one call with forceCloud true (--cloud)", *calls)
	}
	if strings.Contains(stderr, "JIRA_CLOUD is deprecated") {
		t.Errorf("stderr = %q, must not warn about JIRA_CLOUD when only --cloud is set", stderr)
	}
}

func TestRequireAuth_ForceCloudFromEnvWarns(t *testing.T) {
	resetAuthGlobals(t)
	clearJiraEnv(t)
	t.Setenv("JIRA_CLOUD", "true")
	calls := stubDetect(t, jira.Platform{
		Mode:          jira.ModeCloud,
		Source:        jira.SourceOverride,
		ConfiguredURL: "https://jira.custom.example",
		APIBaseURL:    "https://jira.custom.example",
	}, nil)

	jiraURL = "https://jira.custom.example"
	jiraToken = "tok"
	jiraEmail = "user@acme.com"
	isCloud = false

	var err error
	_, stderr := captureOutput(t, func() { err = requireAuth() })
	if err != nil {
		t.Fatalf("requireAuth: %v", err)
	}
	if len(*calls) != 1 || !(*calls)[0].forceCloud {
		t.Errorf("detectPlatform calls = %+v, want one call with forceCloud true (JIRA_CLOUD)", *calls)
	}
	if !strings.Contains(stderr, "JIRA_CLOUD is deprecated") {
		t.Errorf("stderr = %q, want the JIRA_CLOUD deprecation warning", stderr)
	}
}

func TestRequireAuth_FallbackWarnsWithoutVerbose(t *testing.T) {
	resetAuthGlobals(t)
	clearJiraEnv(t)
	stubDetect(t, jira.Platform{
		Mode:           jira.ModeDataCenter,
		Source:         jira.SourceFallback,
		ConfiguredURL:  "https://jira.company.example",
		APIBaseURL:     "https://jira.company.example",
		FallbackReason: "HTTP 404",
	}, nil)

	jiraURL = "https://jira.company.example"
	jiraToken = "tok"
	isCloud = false
	verbose = false

	var err error
	_, stderr := captureOutput(t, func() { err = requireAuth() })
	if err != nil {
		t.Fatalf("requireAuth: %v", err)
	}
	const want = "Warning: could not detect the Jira platform for jira.company.example (HTTP 404); assuming Data Center. If this is Jira Cloud, set JIRA_URL to your https://<site>.atlassian.net address."
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
}

func TestRequireAuth_NoFallbackWarningWhenDetected(t *testing.T) {
	resetAuthGlobals(t)
	clearJiraEnv(t)
	stubDetect(t, jira.Platform{
		Mode:          jira.ModeDataCenter,
		Source:        jira.SourceServerInfo,
		ConfiguredURL: "https://jira.company.example",
		APIBaseURL:    "https://jira.company.example",
	}, nil)

	jiraURL = "https://jira.company.example"
	jiraToken = "tok"
	isCloud = false

	var err error
	_, stderr := captureOutput(t, func() { err = requireAuth() })
	if err != nil {
		t.Fatalf("requireAuth: %v", err)
	}
	if strings.Contains(stderr, "could not detect") {
		t.Errorf("stderr = %q, must not warn when serverInfo detected the platform", stderr)
	}
}

func TestRequireAuth_VerboseShowsSourceAndAPIBase(t *testing.T) {
	resetAuthGlobals(t)
	clearJiraEnv(t)
	stubDetect(t, customDomainPlatform(), nil)

	jiraURL = "https://jira.custom.example"
	jiraToken = "tok"
	jiraEmail = "user@acme.com"
	isCloud = false
	verbose = true

	var err error
	_, stderr := captureOutput(t, func() { err = requireAuth() })
	if err != nil {
		t.Fatalf("requireAuth: %v", err)
	}
	for _, want := range []string{
		"Jira platform mode: Cloud (serverInfo)",
		"jira.custom.example → API base https://acme.atlassian.net",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
	}
}

func TestRequireAuth_DetectionErrorSurfaces(t *testing.T) {
	resetAuthGlobals(t)
	clearJiraEnv(t)
	stubErr := errors.New("site jira.custom.example reports Jira Cloud but did not advertise a usable *.atlassian.net API address; set JIRA_URL to your https://<site>.atlassian.net address")
	stubDetect(t, jira.Platform{}, stubErr)

	jiraURL = "https://jira.custom.example"
	jiraToken = "tok"
	jiraEmail = "user@acme.com"
	isCloud = false
	// A platform left over from an earlier call must not survive a failed detection.
	resolvedPlatform = jira.Platform{
		Mode:          jira.ModeCloud,
		Source:        jira.SourceHost,
		ConfiguredURL: "https://stale.example",
		APIBaseURL:    "https://stale.example",
	}

	err := requireAuth()
	if !errors.Is(err, stubErr) {
		t.Fatalf("requireAuth error = %v, want the detection error", err)
	}
	if resolvedPlatform != (jira.Platform{}) {
		t.Errorf("resolvedPlatform = %+v, want the zero Platform after a failed detection", resolvedPlatform)
	}
	if _, clientErr := newJiraClient(); clientErr == nil {
		t.Error("newJiraClient after a failed detection: want an error, got a client")
	}
}

func TestCheckCmd_DetectionErrorBuildsNoClient(t *testing.T) {
	resetAuthGlobals(t)
	clearJiraEnv(t)

	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer server.Close()

	stubErr := errors.New("site 127.0.0.1 reports Cloud API host acme.atlassian.net (from its serverInfo), which is not in JIRA_ALLOWED_HOSTS; add it or set JIRA_URL to that host")
	stubDetect(t, jira.Platform{}, stubErr)

	configFile = ""
	jiraURL = server.URL
	jiraToken = "tok"
	isCloud = false

	err := checkCmd.RunE(checkCmd, nil)
	if !errors.Is(err, stubErr) {
		t.Fatalf("check error = %v, want the detection error", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("server hits = %d, want 0 (no client may be built after a failed detection)", n)
	}
}

// --- deprecation, target label and help text ---

func TestCloudFlagIsDeprecated(t *testing.T) {
	f := rootCmd.PersistentFlags().Lookup("cloud")
	if f == nil {
		t.Fatal("--cloud flag is not defined")
	}
	if !strings.Contains(f.Deprecated, "detected automatically") {
		t.Errorf("--cloud Deprecated = %q, want it to contain %q", f.Deprecated, "detected automatically")
	}
}

func TestTargetLabel(t *testing.T) {
	resetAuthGlobals(t)

	resolvedPlatform = customDomainPlatform()
	if got, want := targetLabel(), "https://acme.atlassian.net (via jira.custom.example)"; got != want {
		t.Errorf("targetLabel() for a custom domain = %q, want %q", got, want)
	}

	resolvedPlatform = jira.Platform{
		Mode:          jira.ModeDataCenter,
		Source:        jira.SourceServerInfo,
		ConfiguredURL: "https://jira.company.example",
		APIBaseURL:    "https://jira.company.example",
	}
	if got, want := targetLabel(), "https://jira.company.example"; got != want {
		t.Errorf("targetLabel() without a custom domain = %q, want %q", got, want)
	}
}

func TestRootHelpIsCloudFirst(t *testing.T) {
	long := rootCmd.Long
	cloud := strings.Index(long, "Jira Cloud")
	dataCenter := strings.Index(long, "Jira Data Center")
	if cloud < 0 || dataCenter < 0 || cloud > dataCenter {
		t.Errorf("rootCmd.Long: index of %q = %d, index of %q = %d; want Cloud listed first", "Jira Cloud", cloud, "Jira Data Center", dataCenter)
	}
	for _, want := range []string{"custom domain", "JIRA_ALLOWED_HOSTS"} {
		if !strings.Contains(long, want) {
			t.Errorf("rootCmd.Long does not contain %q", want)
		}
	}
}
