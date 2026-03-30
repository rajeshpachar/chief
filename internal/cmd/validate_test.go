package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/minicodemonkey/chief/internal/config"
)

// ─── RunValidate error cases ──────────────────────────────────────────────────

func TestValidateNoTestCommandNoAPITests(t *testing.T) {
	dir := t.TempDir()
	// Write minimal .chief/config.yaml with no testCommand and no apiTests
	os.MkdirAll(filepath.Join(dir, ".chief"), 0755)
	os.WriteFile(filepath.Join(dir, ".chief", "config.yaml"), []byte("project: {}\n"), 0644)

	err := RunValidate(ValidateOptions{
		BaseDir: dir,
		CLIPath: "claude",
	})
	if err == nil {
		t.Fatal("expected error when no test command and no API tests configured")
	}
	if !strings.Contains(err.Error(), "no test command configured") {
		t.Errorf("expected 'no test command configured' error, got: %v", err)
	}
}

// ─── runOneApiTest ────────────────────────────────────────────────────────────

func TestRunOneApiTestPass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok","interactions":[{"drug":"warfarin"}]}`))
	}))
	defer srv.Close()

	test := config.ApiTestConfig{
		Name:               "drug interaction search",
		Method:             "GET",
		Path:               "/api/drugs/interactions",
		ExpectStatus:       200,
		ExpectBodyContains: `"interactions"`,
	}
	result := runOneApiTest(srv.URL, "", test)
	if !result.Passed {
		t.Errorf("expected pass, got failure: %s", result.Failure)
	}
	if result.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", result.StatusCode)
	}
}

func TestRunOneApiTestWrongStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"internal server error"}`))
	}))
	defer srv.Close()

	test := config.ApiTestConfig{
		Name:         "health check",
		Path:         "/health",
		ExpectStatus: 200,
	}
	result := runOneApiTest(srv.URL, "", test)
	if result.Passed {
		t.Error("expected failure for 500 response")
	}
	if !strings.Contains(result.Failure, "expected status 200, got 500") {
		t.Errorf("unexpected failure message: %s", result.Failure)
	}
}

func TestRunOneApiTestMissingBodyContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	test := config.ApiTestConfig{
		Name:               "search has results",
		Path:               "/api/search",
		ExpectStatus:       200,
		ExpectBodyContains: `"results":`,
	}
	result := runOneApiTest(srv.URL, "", test)
	if result.Passed {
		t.Error("expected failure when body missing required content")
	}
	if !strings.Contains(result.Failure, `"results":`) {
		t.Errorf("failure should mention missing content, got: %s", result.Failure)
	}
}

func TestRunOneApiTestBodyNotContains(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok","error":"something"}`))
	}))
	defer srv.Close()

	test := config.ApiTestConfig{
		Name:                  "no error in response",
		Path:                  "/api/status",
		ExpectStatus:          200,
		ExpectBodyNotContains: `"error"`,
	}
	result := runOneApiTest(srv.URL, "", test)
	if result.Passed {
		t.Error("expected failure when forbidden content appears in body")
	}
}

func TestRunOneApiTestAuthHeaderSent(t *testing.T) {
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	test := config.ApiTestConfig{
		Name:         "auth test",
		Path:         "/api/protected",
		ExpectStatus: 200,
	}
	runOneApiTest(srv.URL, "Authorization: Bearer test-token-123", test)
	if receivedAuth != "Bearer test-token-123" {
		t.Errorf("expected auth header 'Bearer test-token-123', got %q", receivedAuth)
	}
}

func TestRunOneApiTestPostBody(t *testing.T) {
	var receivedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])
		w.WriteHeader(200)
		w.Write([]byte(`{"token":"abc123"}`))
	}))
	defer srv.Close()

	test := config.ApiTestConfig{
		Name:               "login",
		Method:             "POST",
		Path:               "/api/auth/login",
		Body:               `{"email":"test@example.com","password":"test123"}`,
		ExpectStatus:       200,
		ExpectBodyContains: `"token"`,
	}
	result := runOneApiTest(srv.URL, "", test)
	if !result.Passed {
		t.Errorf("expected pass, got: %s", result.Failure)
	}
	if !strings.Contains(receivedBody, "test@example.com") {
		t.Errorf("expected POST body to be sent, got: %q", receivedBody)
	}
}

func TestRunOneApiTestDefaultMethodIsGET(t *testing.T) {
	var receivedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	test := config.ApiTestConfig{
		Name: "default method",
		Path: "/api/test",
		// Method intentionally omitted — should default to GET
	}
	runOneApiTest(srv.URL, "", test)
	if receivedMethod != "GET" {
		t.Errorf("expected default method GET, got %q", receivedMethod)
	}
}

func TestRunOneApiTestDefaultExpectStatusIs200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	test := config.ApiTestConfig{
		Name: "default status",
		Path: "/api/test",
		// ExpectStatus intentionally omitted — should default to 200
	}
	result := runOneApiTest(srv.URL, "", test)
	if !result.Passed {
		t.Errorf("expected pass with default 200 status, got: %s", result.Failure)
	}
}

func TestRunOneApiTestConnectionError(t *testing.T) {
	test := config.ApiTestConfig{
		Name: "unreachable server",
		Path: "/api/test",
	}
	// Port 1 is always refused on macOS/Linux
	result := runOneApiTest("http://localhost:1", "", test)
	if result.Passed {
		t.Error("expected failure for unreachable server")
	}
	if result.Failure == "" {
		t.Error("expected non-empty failure message")
	}
}

// ─── runApiTests ──────────────────────────────────────────────────────────────

func TestRunApiTestsAllPass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tests := []config.ApiTestConfig{
		{Name: "test 1", Path: "/one"},
		{Name: "test 2", Path: "/two"},
		{Name: "test 3", Path: "/three"},
	}
	results := runApiTests(srv.URL, "", tests)
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}
	for _, r := range results {
		if !r.Passed {
			t.Errorf("test %q should have passed, got: %s", r.Test.Name, r.Failure)
		}
	}
}

func TestRunApiTestsPartialFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	tests := []config.ApiTestConfig{
		{Name: "pass", Path: "/ok"},
		{Name: "fail", Path: "/fail", ExpectStatus: 200},
		{Name: "pass2", Path: "/ok2"},
	}
	results := runApiTests(srv.URL, "", tests)
	passed := 0
	failed := 0
	for _, r := range results {
		if r.Passed {
			passed++
		} else {
			failed++
		}
	}
	if passed != 2 || failed != 1 {
		t.Errorf("expected 2 pass 1 fail, got %d pass %d fail", passed, failed)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func TestLastNLines(t *testing.T) {
	input := "line1\nline2\nline3\nline4\nline5"
	got := lastNLines(input, 3)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d: %q", len(lines), got)
	}
	if lines[0] != "line3" {
		t.Errorf("expected first line to be 'line3', got %q", lines[0])
	}
}

func TestLastNLinesFewerThanN(t *testing.T) {
	input := "line1\nline2"
	got := lastNLines(input, 10)
	if got != "line1\nline2" {
		t.Errorf("expected original content when fewer than N lines, got %q", got)
	}
}

func TestTruncate(t *testing.T) {
	s := strings.Repeat("a", 300)
	got := truncate(s, 100)
	if len(got) > 103 { // 100 + "..."
		t.Errorf("expected max ~103 chars, got %d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Error("expected truncated string to end with '...'")
	}
}

func TestTruncateShortString(t *testing.T) {
	s := "short"
	got := truncate(s, 100)
	if got != s {
		t.Errorf("expected unchanged short string, got %q", got)
	}
}

func TestShortSummary(t *testing.T) {
	s := "First line\nSecond line\nThird line"
	got := shortSummary(s)
	if strings.Contains(got, "\n") {
		t.Error("shortSummary should return only the first line")
	}
	if got != "First line" {
		t.Errorf("expected 'First line', got %q", got)
	}
}

// ─── buildValidateConfig ──────────────────────────────────────────────────────

func TestBuildValidateConfig(t *testing.T) {
	cfg := buildValidateConfig("https://staging.example.com", "Authorization: Bearer ${TOKEN}", "pytest -x")
	checks := []string{
		"https://staging.example.com",
		"Authorization: Bearer ${TOKEN}",
		"pytest -x",
		"apiTests:",
		"expectStatus:",
		"expectBodyContains:",
		"cicd:",
		"provider:",
	}
	for _, check := range checks {
		if !strings.Contains(cfg, check) {
			t.Errorf("config missing %q", check)
		}
	}
}

// ─── parseGitHubCIStatus ──────────────────────────────────────────────────

func TestParseGitHubCIStatus(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		expect string
	}{
		{
			name:   "success",
			raw:    `[{"status":"completed","conclusion":"success"}]`,
			expect: "success",
		},
		{
			name:   "failure",
			raw:    `[{"status":"completed","conclusion":"failure"}]`,
			expect: "failed",
		},
		{
			name:   "timed out",
			raw:    `[{"status":"completed","conclusion":"timed_out"}]`,
			expect: "failed",
		},
		{
			name:   "in progress",
			raw:    `[{"status":"in_progress","conclusion":""}]`,
			expect: "running",
		},
		{
			name:   "queued",
			raw:    `[{"status":"queued","conclusion":""}]`,
			expect: "running",
		},
		{
			name:   "completed no conclusion",
			raw:    `[{"status":"completed","conclusion":""}]`,
			expect: "failed",
		},
		{
			name:   "empty array",
			raw:    `[]`,
			expect: "unknown",
		},
		{
			name:   "invalid JSON",
			raw:    `not json`,
			expect: "unknown",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseGitHubCIStatus(tc.raw)
			if got != tc.expect {
				t.Errorf("parseGitHubCIStatus(%q) = %q, want %q", tc.raw, got, tc.expect)
			}
		})
	}
}

// ─── parseGitLabCIStatus ──────────────────────────────────────────────────

func TestParseGitLabCIStatus(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		expect string
	}{
		{
			name:   "json success",
			raw:    `{"status":"success"}`,
			expect: "success",
		},
		{
			name:   "json passed",
			raw:    `{"status":"passed"}`,
			expect: "success",
		},
		{
			name:   "json failed",
			raw:    `{"status":"failed"}`,
			expect: "failed",
		},
		{
			name:   "json running",
			raw:    `{"status":"running"}`,
			expect: "running",
		},
		{
			name:   "plaintext passed fallback",
			raw:    "Pipeline #123 passed",
			expect: "success",
		},
		{
			name:   "plaintext failed fallback",
			raw:    "Pipeline #123 failed",
			expect: "failed",
		},
		{
			name:   "plaintext running fallback",
			raw:    "Pipeline #123 is running",
			expect: "running",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseGitLabCIStatus(tc.raw)
			if got != tc.expect {
				t.Errorf("parseGitLabCIStatus(%q) = %q, want %q", tc.raw, got, tc.expect)
			}
		})
	}
}

// ─── extractJSON (from orchestrate.go — used by validate coordinator too) ────

func TestExtractJSONValidateCoordinator(t *testing.T) {
	// Coordinator response typically wrapped in markdown by Claude
	input := "Based on the failures, I recommend fixing the worker.\n```json\n{\"summary\":\"fix worker\",\"services_to_fix\":[\"worker\"],\"abort\":false}\n```"
	got := extractJSON(input)
	if !strings.Contains(got, `"summary"`) {
		t.Errorf("expected JSON extraction, got: %q", got)
	}
}

// ─── extractTokenField ────────────────────────────────────────────────────────

func TestExtractTokenField(t *testing.T) {
	cases := []struct {
		name    string
		jsonStr string
		field   string
		want    string
		wantErr bool
	}{
		{"top-level", `{"token":"abc123"}`, "token", "abc123", false},
		{"nested", `{"data":{"accessToken":"jwt.tok.en"}}`, "data.accessToken", "jwt.tok.en", false},
		{"three levels", `{"auth":{"tokens":{"access":"deep"}}}`, "auth.tokens.access", "deep", false},
		{"field not found", `{"user":"test"}`, "token", "", true},
		{"empty field", `{"token":"abc"}`, "", "", true},
		{"invalid json", `not json`, "token", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractTokenField([]byte(tc.jsonStr), tc.field)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// ─── loadCredsFile ────────────────────────────────────────────────────────────

func TestLoadCredsFile(t *testing.T) {
	dir := t.TempDir()
	content := "CREDS_EMAIL=test@example.com\nCREDS_PASSWORD=secret123\n# comment\n"
	if err := os.WriteFile(filepath.Join(dir, "staging.creds"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("CREDS_EMAIL")
	os.Unsetenv("CREDS_PASSWORD")

	if err := loadCredsFile(dir, "staging.creds"); err != nil {
		t.Fatalf("loadCredsFile failed: %v", err)
	}
	if got := os.Getenv("CREDS_EMAIL"); got != "test@example.com" {
		t.Errorf("CREDS_EMAIL = %q, want %q", got, "test@example.com")
	}
	if got := os.Getenv("CREDS_PASSWORD"); got != "secret123" {
		t.Errorf("CREDS_PASSWORD = %q, want %q", got, "secret123")
	}
}

func TestLoadCredsFileDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "staging.creds"), []byte("OVERWRITE_TEST=from_file\n"), 0600)
	os.Setenv("OVERWRITE_TEST", "from_env")
	defer os.Unsetenv("OVERWRITE_TEST")

	loadCredsFile(dir, "staging.creds")
	if got := os.Getenv("OVERWRITE_TEST"); got != "from_env" {
		t.Errorf("env var should not be overwritten, got %q", got)
	}
}

// ─── performLogin ─────────────────────────────────────────────────────────────

func TestPerformLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"accessToken":"jwt.abc.123"}}`))
	}))
	defer srv.Close()

	lc := config.LoginConfig{
		Path:       "/api/auth/login",
		Body:       `{"email":"test@example.com","password":"secret"}`,
		TokenField: "data.accessToken",
	}
	token, err := performLogin(srv.URL, lc)
	if err != nil {
		t.Fatalf("performLogin failed: %v", err)
	}
	if token != "jwt.abc.123" {
		t.Errorf("expected 'jwt.abc.123', got %q", token)
	}
}

func TestPerformLoginBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"invalid credentials"}`))
	}))
	defer srv.Close()

	lc := config.LoginConfig{
		Path:       "/api/auth/login",
		Body:       `{"email":"wrong","password":"wrong"}`,
		TokenField: "token",
	}
	_, err := performLogin(srv.URL, lc)
	if err == nil {
		t.Error("expected error for 401")
	}
}

func TestPerformLoginEnvExpansion(t *testing.T) {
	os.Setenv("LOGIN_TEST_EMAIL", "envuser@example.com")
	defer os.Unsetenv("LOGIN_TEST_EMAIL")

	var receivedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])
		w.Write([]byte(`{"token":"ok"}`))
	}))
	defer srv.Close()

	lc := config.LoginConfig{
		Path:       "/login",
		Body:       `{"email":"${LOGIN_TEST_EMAIL}"}`,
		TokenField: "token",
	}
	performLogin(srv.URL, lc)
	if !strings.Contains(receivedBody, "envuser@example.com") {
		t.Errorf("env var not expanded in body, got: %q", receivedBody)
	}
}
