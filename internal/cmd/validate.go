package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/minicodemonkey/chief/internal/config"
)

// ValidateOptions configures a validation + fix + deploy cycle.
type ValidateOptions struct {
	// BaseDir is the repo root (default: cwd)
	BaseDir string

	// CLIPath is the Claude binary (default: "claude")
	CLIPath string

	// Fix enables the auto-fix agent when tests fail (default: false = report only)
	Fix bool

	// Push commits and pushes after a fix, then waits for CI/CD (default: false)
	Push bool

	// MaxCycles is the maximum fix→push→retest cycles (default: 3)
	MaxCycles int

	// LocalTestCmd overrides project.testCommand for the local test run
	LocalTestCmd string
}

// ApiTestResult records the outcome of one API use-case test.
type ApiTestResult struct {
	Test       config.ApiTestConfig
	StatusCode int
	Body       string
	Passed     bool
	Failure    string // human-readable reason for failure
	DurationMs int64
}

// ValidationRun records the outcome of one validate cycle.
type ValidationRun struct {
	Cycle            int
	LocalTestOutput  string
	LocalTestPassed  bool
	ApiResults       []ApiTestResult
	AllApiPassed     bool
	FixApplied       string
	CIPassed         bool
	CISkipped        bool
}

// RunValidate runs the full validate → fix → push → CI → re-test loop.
//
// Flow per cycle:
//  1. Run local tests (pytest / go test / etc.)
//  2. Run API use-case tests against staging URL
//  3. If anything failed and --fix: spawn fix agent with (failure + API response + code)
//  4. If --push: commit, push, poll CI/CD until green
//  5. Re-run API tests against staging post-deploy
//  6. Repeat until all pass or MaxCycles reached
func RunValidate(opts ValidateOptions) error {
	if opts.BaseDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get cwd: %w", err)
		}
		opts.BaseDir = cwd
	}
	if opts.CLIPath == "" {
		opts.CLIPath = "claude"
	}
	if opts.MaxCycles <= 0 {
		opts.MaxCycles = 3
	}

	cfg, err := config.Load(opts.BaseDir)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	stagingURL := cfg.Project.Validation.StagingUrl
	if stagingURL == "" {
		stagingURL = cfg.Project.StagingURL
	}
	if stagingURL == "" {
		stagingURL = os.Getenv("CHIEF_STAGING_URL")
	}

	// apiBase is the URL used for API tests.
	// CHIEF_API_URL env var overrides everything (use for localhost vs staging switching).
	apiBase := os.Getenv("CHIEF_API_URL")
	if apiBase == "" {
		apiBase = cfg.Project.Validation.ApiURL
	}
	if apiBase == "" {
		apiBase = stagingURL
	}

	testCmd := opts.LocalTestCmd
	if testCmd == "" {
		testCmd = cfg.Project.TestCommand
	}
	if testCmd == "" && len(cfg.Project.Validation.ApiTests) == 0 {
		return fmt.Errorf("no test command configured — set project.testCommand in .chief/config.yaml or pass --test-cmd")
	}

	// Load credentials file before expanding env vars (so ${VAR} in body/header works)
	if cf := cfg.Project.Validation.CredentialsFile; cf != "" {
		if err := loadCredsFile(opts.BaseDir, cf); err != nil {
			fmt.Printf("Warning: could not load credentialsFile %q: %v\n", cf, err)
		}
	}

	// Resolve auth header — login takes precedence over static authHeader
	authHeader := os.ExpandEnv(cfg.Project.Validation.AuthHeader)
	if lc := cfg.Project.Validation.Login; lc.Path != "" {
		token, err := performLogin(apiBase, lc)
		if err != nil {
			fmt.Printf("  ✗ Login failed: %v\n", err)
			if opts.Fix {
				fmt.Println("  Asking Claude to diagnose and fix the login config...")
				if fixErr := runLoginFixAgent(opts.CLIPath, opts.BaseDir, apiBase, lc, err.Error(), cfg); fixErr != nil {
					fmt.Printf("  Fix agent error: %v\n", fixErr)
				} else {
					fmt.Println("  Config updated — re-run 'chief validate' to try again.")
				}
			} else {
				fmt.Println("  Run 'chief validate --fix' to let Claude diagnose and fix the login config.")
			}
			return fmt.Errorf("login step failed: %w", err)
		}
		headerName := lc.HeaderName
		if headerName == "" {
			headerName = "Authorization"
		}
		prefix := lc.HeaderPrefix
		if prefix == "" {
			prefix = "Bearer "
		}
		authHeader = headerName + ": " + prefix + token
		fmt.Printf("  ✓ Login succeeded — token obtained\n")
	}

	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Printf("Chief Validate — %d API test(s) | fix=%v push=%v\n",
		len(cfg.Project.Validation.ApiTests), opts.Fix, opts.Push)
	if stagingURL != "" {
		fmt.Printf("Staging: %s\n", stagingURL)
	}
	if apiBase != "" && apiBase != stagingURL {
		fmt.Printf("API:     %s\n", apiBase)
	}
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println()

	for cycle := 1; cycle <= opts.MaxCycles; cycle++ {
		run := ValidationRun{Cycle: cycle}
		fmt.Printf("── Cycle %d/%d ────────────────────────────────────────\n", cycle, opts.MaxCycles)

		// ── Step 1: Local tests ────────────────────────────────
		if testCmd != "" && testCmd != "none" {
			fmt.Printf("Running local tests: %s\n", testCmd)
			localOut, localErr := runShellCommand(opts.BaseDir, testCmd)
			run.LocalTestOutput = localOut
			run.LocalTestPassed = localErr == nil
			if run.LocalTestPassed {
				fmt.Println("  ✓ Local tests passed")
			} else {
				fmt.Println("  ✗ Local tests failed")
				printIndented(lastNLines(localOut, 10))
			}
		} else {
			run.LocalTestPassed = true
		}

		// ── Step 2: API use-case tests ─────────────────────────
		if apiBase != "" && len(cfg.Project.Validation.ApiTests) > 0 {
			fmt.Printf("\nRunning %d API use-case test(s) against %s\n",
				len(cfg.Project.Validation.ApiTests), apiBase)
			var lc *config.LoginConfig
			if cfg.Project.Validation.Login.Path != "" {
				lcp := cfg.Project.Validation.Login
				lc = &lcp
			}
			run.ApiResults = runApiTestsWithLogin(apiBase, authHeader, lc, cfg.Project.Validation.ApiTests)
			run.AllApiPassed = true
			for _, r := range run.ApiResults {
				icon := "✓"
				if !r.Passed {
					icon = "✗"
					run.AllApiPassed = false
				}
				fmt.Printf("  %s [%dms] %s\n", icon, r.DurationMs, r.Test.Name)
				if !r.Passed {
					fmt.Printf("      → %s\n", r.Failure)
				}
			}
		} else {
			run.AllApiPassed = true
			if apiBase == "" {
				fmt.Println("(no stagingUrl or apiUrl configured — skipping API tests)")
			}
		}

		// All good?
		if run.LocalTestPassed && run.AllApiPassed {
			fmt.Println("\n✓ All tests passed.")
			return nil
		}

		if !opts.Fix {
			fmt.Println("\nUse --fix to auto-fix failures.")
			return fmt.Errorf("validation failed")
		}

		// ── Step 3: Fix agent ──────────────────────────────────
		fmt.Println("\nSpawning fix agent...")
		fix, fixErr := runValidationFixAgent(opts.CLIPath, opts.BaseDir, run, cfg)
		if fixErr != nil {
			fmt.Printf("  Fix agent error: %v\n", fixErr)
		} else {
			run.FixApplied = fix
			preview := fix
			if len(preview) > 300 {
				preview = preview[:300] + "..."
			}
			fmt.Printf("  Fix: %s\n", preview)
		}

		if !opts.Push {
			fmt.Println("\nUse --push to commit, push, and re-test against CI/CD.")
			continue
		}

		// ── Step 4: Commit + push ──────────────────────────────
		commitMsg := fmt.Sprintf("fix(validate): cycle %d — %s", cycle, shortSummary(run.FixApplied))
		fmt.Printf("\nCommitting: %s\n", commitMsg)
		if err := gitCommitAndPush(opts.BaseDir, commitMsg, cfg.Project.Validation.CICD.Branch); err != nil {
			fmt.Printf("  Push failed: %v\n", err)
			continue
		}
		fmt.Println("  ✓ Pushed")

		// ── Step 5: Poll CI/CD ─────────────────────────────────
		ciPassed, ciSkipped := pollCICD(opts.BaseDir, cfg.Project.Validation.CICD)
		run.CIPassed = ciPassed
		run.CISkipped = ciSkipped
		if ciSkipped {
			fmt.Println("  CI/CD: skipped (no provider configured)")
		} else if ciPassed {
			fmt.Println("  ✓ CI/CD passed")
		} else {
			fmt.Println("  ✗ CI/CD failed — will retry in next cycle")
		}

		// Brief pause for deploy to propagate
		if ciPassed {
			fmt.Println("Waiting 10s for deploy to propagate...")
			time.Sleep(10 * time.Second)
		}
	}

	return fmt.Errorf("validation did not pass after %d cycle(s)", opts.MaxCycles)
}

// runApiTests executes all API use-case tests against the staging server.
// Each test is independent; failures do not stop subsequent tests.
func runApiTests(baseURL, authHeader string, tests []config.ApiTestConfig) []ApiTestResult {
	return runApiTestsWithLogin(baseURL, authHeader, nil, tests)
}

// runApiTestsWithLogin runs all tests, honouring per-test loginBody overrides.
// lc is the global login config used to do per-test re-logins when loginBody is set.
// Tokens are cached by expanded login body so each distinct user logs in only once.
func runApiTestsWithLogin(baseURL, defaultAuthHeader string, lc *config.LoginConfig, tests []config.ApiTestConfig) []ApiTestResult {
	tokenCache := map[string]string{} // expanded body → "HeaderName: prefix token"
	results := make([]ApiTestResult, len(tests))
	for i, t := range tests {
		authHeader := defaultAuthHeader
		if t.LoginBody != "" && lc != nil {
			expanded := os.ExpandEnv(t.LoginBody)
			if cached, ok := tokenCache[expanded]; ok {
				authHeader = cached
			} else {
				override := *lc
				override.Body = t.LoginBody
				token, err := performLogin(baseURL, override)
				if err != nil {
					results[i] = ApiTestResult{
						Test:    t,
						Passed:  false,
						Failure: fmt.Sprintf("login for test failed: %v", err),
					}
					continue
				}
				headerName := lc.HeaderName
				if headerName == "" {
					headerName = "Authorization"
				}
				prefix := lc.HeaderPrefix
				if prefix == "" {
					prefix = "Bearer "
				}
				authHeader = headerName + ": " + prefix + token
				tokenCache[expanded] = authHeader
			}
		}
		results[i] = runOneApiTest(baseURL, authHeader, t)
	}
	return results
}

// runOneApiTest runs a single API use-case test and returns the result.
func runOneApiTest(baseURL, authHeader string, test config.ApiTestConfig) ApiTestResult {
	r := ApiTestResult{Test: test}

	method := test.Method
	if method == "" {
		method = "GET"
	}
	expectStatus := test.ExpectStatus
	if expectStatus == 0 {
		expectStatus = 200
	}

	url := baseURL + test.Path
	var bodyReader io.Reader
	if test.Body != "" {
		bodyReader = strings.NewReader(test.Body)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		r.Failure = fmt.Sprintf("build request: %v", err)
		return r
	}

	// Apply shared auth header
	if authHeader != "" {
		parts := strings.SplitN(authHeader, ": ", 2)
		if len(parts) == 2 {
			req.Header.Set(parts[0], parts[1])
		}
	}
	// Apply per-test headers
	for k, v := range test.Headers {
		req.Header.Set(k, os.ExpandEnv(v))
	}
	if test.Body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	r.DurationMs = time.Since(start).Milliseconds()

	if err != nil {
		r.Failure = fmt.Sprintf("request failed: %v", err)
		return r
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 256_000))
	r.StatusCode = resp.StatusCode
	r.Body = string(bodyBytes)

	// Validate status
	if r.StatusCode != expectStatus {
		r.Failure = fmt.Sprintf("expected status %d, got %d. Body: %s",
			expectStatus, r.StatusCode, truncate(r.Body, 200))
		return r
	}

	// Validate body contains
	if test.ExpectBodyContains != "" && !strings.Contains(r.Body, test.ExpectBodyContains) {
		r.Failure = fmt.Sprintf("body missing %q. Got: %s",
			test.ExpectBodyContains, truncate(r.Body, 200))
		return r
	}

	// Validate body does not contain
	if test.ExpectBodyNotContains != "" && strings.Contains(r.Body, test.ExpectBodyNotContains) {
		r.Failure = fmt.Sprintf("body should not contain %q. Got: %s",
			test.ExpectBodyNotContains, truncate(r.Body, 200))
		return r
	}

	r.Passed = true
	return r
}

// runValidationFixAgent spawns a Claude fix agent with full context:
// local test failures + API responses + relevant source code.
func runValidationFixAgent(cliPath, baseDir string, run ValidationRun, cfg *config.Config) (string, error) {
	var sb strings.Builder

	sb.WriteString("Fix the following test failures. Read the source code, identify the root cause, and apply a minimal targeted fix.\n\n")

	// Local test failures
	if !run.LocalTestPassed {
		sb.WriteString("## Local test failures\n")
		sb.WriteString(lastNLines(run.LocalTestOutput, 30))
		sb.WriteString("\n\n")
	}

	// API test failures — include actual HTTP responses as context
	var apiFailures []ApiTestResult
	for _, r := range run.ApiResults {
		if !r.Passed {
			apiFailures = append(apiFailures, r)
		}
	}
	if len(apiFailures) > 0 {
		sb.WriteString("## API use-case test failures\n\n")
		for _, r := range apiFailures {
			sb.WriteString(fmt.Sprintf("### %s\n", r.Test.Name))
			sb.WriteString(fmt.Sprintf("Request:  %s %s\n", r.Test.Method, r.Test.Path))
			sb.WriteString(fmt.Sprintf("Status:   %d (expected %d)\n", r.StatusCode, r.Test.ExpectStatus))
			sb.WriteString(fmt.Sprintf("Failure:  %s\n", r.Failure))
			if r.Body != "" {
				sb.WriteString(fmt.Sprintf("Response body:\n%s\n", truncate(r.Body, 500)))
			}
			sb.WriteString("\n")
		}
	}

	// Architecture context
	if claudeMd := readCLAUDEMd(baseDir); claudeMd != "" {
		sb.WriteString("## Architecture rules (CLAUDE.md)\n")
		sb.WriteString(claudeMd)
		sb.WriteString("\n\n")
	}

	// Let Claude pick which skills are relevant to these specific failures
	failureContext := sb.String() // failures already written above
	if skills := selectRelevantSkills(cliPath, failureContext, discoverAllSkills(baseDir)); skills != "" {
		sb.WriteString("## Relevant skills\n")
		sb.WriteString("The following skills have been identified as relevant to these failures.\n")
		sb.WriteString("Apply their guidance when fixing.\n\n")
		sb.WriteString(skills)
		sb.WriteString("\n")
	}

	sb.WriteString("## Fix rules\n")
	sb.WriteString("- Fix ONLY the failing tests/endpoints shown above\n")
	sb.WriteString("- Do NOT change API contracts, rename fields, or break other endpoints\n")
	sb.WriteString("- Write the failing test case FIRST if it doesn't exist, then fix the implementation\n")
	sb.WriteString("- Commit format: fix(<area>): <one sentence describing the fix>\n")
	sb.WriteString("- After fixing, state: root cause, files changed, why this won't break other flows\n")

	return runClaudeNonInteractive(cliPath, baseDir, sb.String())
}

// pollCICD pushes and waits for the CI/CD pipeline to complete.
// Returns (passed, skipped).
func pollCICD(baseDir string, cicd config.CICDConfig) (passed, skipped bool) {
	if cicd.Provider == "" || cicd.Provider == "none" {
		return false, true
	}

	pollInterval := time.Duration(cicd.PollIntervalSec) * time.Second
	if pollInterval <= 0 {
		pollInterval = 30 * time.Second
	}
	maxWait := time.Duration(cicd.MaxWaitSec) * time.Second
	if maxWait <= 0 {
		maxWait = 600 * time.Second
	}

	deadline := time.Now().Add(maxWait)
	fmt.Printf("  Polling %s CI/CD (max %s)...\n", cicd.Provider, maxWait)

	for time.Now().Before(deadline) {
		status := checkCIStatus(baseDir, cicd)
		switch status {
		case "success":
			return true, false
		case "failed", "cancelled":
			fmt.Printf("  CI status: %s\n", status)
			return false, false
		default:
			fmt.Printf("  CI status: %s — waiting %s...\n", status, pollInterval)
			time.Sleep(pollInterval)
		}
	}

	fmt.Printf("  CI/CD timed out after %s\n", maxWait)
	return false, false
}

// checkCIStatus returns "success", "failed", "running", "pending", or "unknown".
func checkCIStatus(baseDir string, cicd config.CICDConfig) string {
	switch cicd.Provider {
	case "github":
		args := []string{"run", "list", "--limit", "1", "--json", "status,conclusion"}
		if cicd.Workflow != "" {
			args = append(args, "--workflow", cicd.Workflow)
		}
		if cicd.Branch != "" {
			args = append(args, "--branch", cicd.Branch)
		}
		out, err := runCommandInDir(baseDir, "gh", args...)
		if err != nil {
			return "unknown"
		}
		return parseGitHubCIStatus(strings.TrimSpace(out))

	case "gitlab":
		out, err := runCommandInDir(baseDir, "glab", "ci", "status", "--output", "json")
		if err != nil {
			return "unknown"
		}
		return parseGitLabCIStatus(strings.TrimSpace(out))

	default:
		return "unknown"
	}
}

// parseGitHubCIStatus decodes the JSON from `gh run list --json status,conclusion`.
func parseGitHubCIStatus(raw string) string {
	var runs []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	}
	if err := json.Unmarshal([]byte(raw), &runs); err != nil || len(runs) == 0 {
		return "unknown"
	}
	r := runs[0]
	switch r.Conclusion {
	case "success":
		return "success"
	case "failure", "timed_out", "cancelled", "action_required":
		return "failed"
	}
	switch r.Status {
	case "completed":
		return "failed" // completed with no recognised success conclusion
	case "in_progress", "queued", "waiting", "pending", "requested":
		return "running"
	}
	return "pending"
}

// parseGitLabCIStatus decodes JSON from `glab ci status --output json`.
// Falls back to plain-text heuristics if JSON is unavailable (older glab versions).
func parseGitLabCIStatus(raw string) string {
	// Try JSON first (glab ≥ 1.36)
	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err == nil && result.Status != "" {
		switch result.Status {
		case "success", "passed":
			return "success"
		case "failed", "canceled", "skipped":
			return "failed"
		case "running", "pending", "created", "waiting_for_resource", "preparing":
			return "running"
		}
	}
	// Plaintext fallback
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "passed") || strings.Contains(lower, "success") {
		return "success"
	}
	if strings.Contains(lower, "failed") || strings.Contains(lower, "canceled") {
		return "failed"
	}
	return "running"
}

// gitCommitAndPush stages all changes, commits, and pushes to the target branch.
func gitCommitAndPush(baseDir, message, branch string) error {
	// Stage all changed files
	if _, err := runCommandInDir(baseDir, "git", "add", "-A"); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	// Check if there's anything to commit
	statusOut, _ := runCommandInDir(baseDir, "git", "status", "--porcelain")
	if strings.TrimSpace(statusOut) == "" {
		fmt.Println("  Nothing to commit.")
		return nil
	}

	if _, err := runCommandInDir(baseDir, "git", "commit", "-m", message); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	pushArgs := []string{"push"}
	if branch != "" {
		pushArgs = append(pushArgs, "origin", branch)
	}
	if _, err := runCommandInDir(baseDir, "git", pushArgs...); err != nil {
		return fmt.Errorf("git push: %w", err)
	}
	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// runShellCommand runs shellCmd in dir and returns combined output capped at 10 MB.
// The cap prevents OOM when test runners (e.g. Playwright, Cypress) produce
// large amounts of output that would otherwise fill an unbounded bytes.Buffer.
func runShellCommand(dir, shellCmd string) (string, error) {
	cmd := exec.Command("sh", "-c", shellCmd)
	cmd.Dir = dir
	lb := &limitedBuffer{max: 10 * 1024 * 1024}
	cmd.Stdout = lb
	cmd.Stderr = lb
	err := cmd.Run()
	return lb.String(), err
}

// limitedBuffer is an io.Writer that discards writes once the buffer exceeds max bytes.
type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len() < b.max {
		remaining := b.max - b.buf.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.buf.Write(p)
	}
	return len(p), nil // always report success so the process isn't disrupted
}

func (b *limitedBuffer) String() string { return b.buf.String() }

func runCommandInDir(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func lastNLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func shortSummary(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, "\n"); idx > 0 {
		s = s[:idx]
	}
	if len(s) > 72 {
		s = s[:72]
	}
	return s
}

func printIndented(s string) {
	for _, line := range strings.Split(s, "\n") {
		fmt.Printf("    %s\n", line)
	}
}

// runLoginFixAgent asks Claude to diagnose a failed login step and rewrite
// the validation.login section of .chief/config.yaml.
func runLoginFixAgent(cliPath, baseDir, apiBase string, lc config.LoginConfig, loginErr string, cfg *config.Config) error {
	cfgPath := ".chief/config.yaml"

	prompt := fmt.Sprintf(`The login step in chief validate failed. Diagnose the error and fix .chief/config.yaml.

## Login config that was used

Login URL: %s%s
Content-Type: %s
Body (after env var expansion): (env vars not shown — shape matters, not values)
Body template: %s
Token field: %s

## Error from the server

%s

## Current .chief/config.yaml

Read the file at %s to see the full config.

## Your task

1. Diagnose the root cause of the login failure (wrong content type, wrong field names, wrong endpoint, wrong token field, etc.).
2. Fix ONLY the validation.login (and optionally validation.credentialsFile) section in %s.
3. Do not change any other part of the config.
4. After fixing, briefly state: what was wrong and what you changed.

Common patterns to check:
- FastAPI OAuth2PasswordRequestForm: needs contentType: application/x-www-form-urlencoded, field named "username" (not "email"), returns {"access_token":"...","token_type":"bearer"} → tokenField: access_token
- JWT REST API: needs contentType: application/json, body {"email":"...","password":"..."}, tokenField varies (token, data.accessToken, auth.jwt, etc.)
- 422 with missing "username": almost certainly FastAPI OAuth2 — switch to form-encoded with username field
- 401 with "invalid credentials": config structure is right, actual creds wrong — do NOT change config
`, apiBase, lc.Path,
		func() string {
			if lc.ContentType != "" {
				return lc.ContentType
			}
			return "application/json (default)"
		}(),
		lc.Body, lc.TokenField, loginErr, cfgPath, cfgPath)

	_, err := runClaudeNonInteractive(cliPath, baseDir, prompt)
	return err
}

// loadCredsFile reads a .env-style file (KEY=VALUE lines) and sets each pair
// as an environment variable for the current process. Existing env vars are not
// overwritten (same semantics as dotenv's non-overriding mode).
func loadCredsFile(baseDir, path string) error {
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		if key == "" {
			continue
		}
		// Don't overwrite values already in the environment
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
	return nil
}

// performLogin POSTs to the login endpoint, extracts the token using tokenField
// (dot-path into the JSON response), and returns it.
func performLogin(baseURL string, lc config.LoginConfig) (string, error) {
	url := baseURL + lc.Path
	body := os.ExpandEnv(lc.Body)

	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build login request: %w", err)
	}
	contentType := lc.ContentType
	if contentType == "" {
		contentType = "application/json"
	}
	req.Header.Set("Content-Type", contentType)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64_000))
	if err != nil {
		return "", fmt.Errorf("read login response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("login returned %d: %s", resp.StatusCode, truncate(string(respBody), 300))
	}

	token, err := extractTokenField(respBody, lc.TokenField)
	if err != nil {
		return "", fmt.Errorf("extract token from login response: %w (body: %s)", err, truncate(string(respBody), 300))
	}
	return token, nil
}

// extractTokenField extracts a string value from a JSON byte slice using a
// dot-separated path (e.g. "data.accessToken", "token", "auth.jwt").
func extractTokenField(data []byte, field string) (string, error) {
	if field == "" {
		return "", fmt.Errorf("tokenField is not configured")
	}

	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return "", fmt.Errorf("parse JSON: %w", err)
	}

	parts := strings.Split(field, ".")
	var cur interface{} = obj
	for _, part := range parts {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("path %q: not an object at %q", field, part)
		}
		cur, ok = m[part]
		if !ok {
			return "", fmt.Errorf("path %q: key %q not found", field, part)
		}
	}

	switch v := cur.(type) {
	case string:
		if v == "" {
			return "", fmt.Errorf("path %q: token is empty string", field)
		}
		return v, nil
	default:
		return fmt.Sprintf("%v", v), nil
	}
}

// buildValidateConfig returns a sample .chief/config.yaml validation section
// with inline documentation. Used by: chief validate --init
func buildValidateConfig(stagingURL, authHeader, testCmd string) string {
	return fmt.Sprintf(`# .chief/config.yaml — validation section
# Run: chief validate --fix --push

project:
  testCommand: "%s"   # local test command (pytest, go test, npm test, etc.)
  stagingUrl: "%s"    # base URL for API use-case tests

  validation:
    stagingUrl: "%s"  # overrides project.stagingUrl for validation only (optional)

    # authHeader: HTTP header sent with every API test request.
    # Use env var expansion for secrets: ${STAGING_TOKEN}
    authHeader: "%s"

    # apiTests: list of use-case tests against the staging server.
    # Each test verifies ONE user-facing scenario end-to-end.
    apiTests:
      - name: "health check"          # human-readable test name
        method: GET                    # GET | POST | PUT | PATCH | DELETE
        path: /api/health              # URL path + query string
        expectStatus: 200             # expected HTTP status code

      - name: "login returns token"
        method: POST
        path: /api/auth/login
        body: '{"email":"test@example.com","password":"test123"}'
        expectStatus: 200
        expectBodyContains: '"token":'   # substring that must appear in response

      - name: "drug interaction search"
        method: GET
        path: /api/drugs/interactions?drug1=aspirin&drug2=warfarin
        expectStatus: 200
        expectBodyContains: '"interactions":'
        expectBodyNotContains: '"error"'   # substring that must NOT appear

      - name: "protected endpoint requires auth"
        method: GET
        path: /api/admin/users
        headers:
          Authorization: ""          # override auth header for this test only
        expectStatus: 401

    # cicd: how chief pushes and polls CI after a fix
    cicd:
      provider: github         # github | gitlab | none
      branch: dev              # branch to push fixes to
      workflow: ci.yml         # GitHub Actions workflow file (github only)
      pollIntervalSec: 30      # how often to check CI status
      maxWaitSec: 600          # give up after 10 minutes
`, testCmd, stagingURL, stagingURL, authHeader)
}

// WriteValidateConfig writes a starter config to .chief/config.yaml
// if no validation section exists yet.
func WriteValidateConfig(baseDir, stagingURL string) error {
	cfgPath := filepath.Join(baseDir, ".chief", "config.yaml")
	os.MkdirAll(filepath.Dir(cfgPath), 0755)

	// Don't overwrite existing config
	if _, err := os.Stat(cfgPath); err == nil {
		return fmt.Errorf("config already exists at %s — edit it directly", cfgPath)
	}

	content := buildValidateConfig(stagingURL, "Authorization: Bearer ${STAGING_TOKEN}", "pytest -x -q")
	return os.WriteFile(cfgPath, []byte(content), 0644)
}
