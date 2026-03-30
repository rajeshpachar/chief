package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/minicodemonkey/chief/internal/config"
)

// jsonUnmarshal is a thin alias so tests can stub it if needed.
var jsonUnmarshal = json.Unmarshal

// Package-level compiled regexes for sanitizeSlug (avoids re-compilation on every call).
var (
	slugNonAlphanumericRE = regexp.MustCompile(`[^a-z0-9-]`)
	slugMultiHyphenRE     = regexp.MustCompile(`-+`)
)

// validPRDTypes is the canonical set of types chief understands.
var validPRDTypes = []string{
	"bug-fix", "feature", "test-coverage", "robustness",
	"exploration", "server-debug", "refactor", "security",
	"perf", "migration", "review", "batch-fix",
}

// GenerateOptions configures the PRD generator.
type GenerateOptions struct {
	// Description is the free-form work request. If empty, prompts interactively.
	Description string

	// ForcedType overrides auto-detection. One of: bug-fix, feature, test-coverage,
	// robustness, exploration, server-debug, refactor, security, perf, migration, review, batch-fix
	ForcedType string

	// Voice enables multi-round voice input loop: speak → confirm → combine → PRD.
	Voice bool

	// VoiceDuration is the max recording duration per round (seconds). Default 30.
	VoiceDuration int

	// VoiceBackend forces a specific transcription backend (openai, gemini, mlx).
	VoiceBackend string

	// SkipResearch disables the automatic pre-PRD research pass.
	// By default, research always runs to ground the PRD in real codebase context.
	SkipResearch bool

	// BaseDir is the repo root (default: cwd)
	BaseDir string

	// CLIPath is the Claude binary (default: "claude")
	CLIPath string
}

// RunGenerate creates a PRD from a free-form description using Claude as the classifier.
func RunGenerate(opts GenerateOptions) error {
	if opts.BaseDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get cwd: %w", err)
		}
		opts.BaseDir = cwd
	}
	if opts.CLIPath == "" {
		opts.CLIPath = "claude"
	}

	// ─── Step 1: Get description ──────────────────────────────
	description := strings.TrimSpace(opts.Description)

	if opts.Voice {
		// Multi-round voice loop: speak → confirm each segment → combine all
		combined, err := CollectVoiceInput(VoiceOptions{
			DurationSec: opts.VoiceDuration,
			Backend:     opts.VoiceBackend,
		})
		if err != nil {
			return fmt.Errorf("voice input: %w", err)
		}
		if description != "" {
			description = description + "\n\n" + combined
		} else {
			description = combined
		}
	}

	if description == "" {
		fmt.Println("What do you want Chief to do?")
		fmt.Println("(describe in plain language — bug fix, test gap, new feature, server issue, etc.)")
		fmt.Println()
		fmt.Print("> ")
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read input: %w", err)
		}
		description = strings.TrimSpace(line)
	}

	if description == "" {
		return fmt.Errorf("no description provided")
	}

	// Auto-research: always run a research pass unless explicitly disabled.
	// Uses Claude to read the codebase, git history, CLAUDE.md, existing skills/docs,
	// and optionally web-search for relevant context — then injects findings into the PRD.
	if !opts.SkipResearch {
		fmt.Println("Running auto-research to ground PRD in codebase...")
		research, err := RunResearch(ResearchOptions{
			Description: description,
			BaseDir:     opts.BaseDir,
			CLIPath:     opts.CLIPath,
		})
		if err != nil {
			fmt.Printf("Warning: research pass failed (%v) — generating PRD without extra context\n", err)
		} else if research != nil {
			// Show research summary to user
			fmt.Println()
			fmt.Println("── Research findings ────────────────────────────────────────")
			// Print a short preview (first 20 lines) of the report
			lines := strings.Split(research.RawReport, "\n")
			preview := lines
			if len(preview) > 20 {
				preview = lines[:20]
				fmt.Println(strings.Join(preview, "\n"))
				fmt.Printf("  ... (%d more lines — full report: %s)\n", len(lines)-20, research.ReportPath)
			} else {
				fmt.Println(strings.Join(preview, "\n"))
			}
			fmt.Println()

			// Post-research review loop: user can add voice feedback or corrections
			// before the final PRD is generated.
			if opts.Voice {
				fmt.Println("── Post-research review ─────────────────────────────────────")
				fmt.Println("Review the findings above. Add corrections or extra context via voice,")
				fmt.Println("or press Enter to proceed directly to PRD generation.")
				fmt.Println()
				fmt.Print("Add feedback? [y/N] ")
				reader := bufio.NewReader(os.Stdin)
				ans, _ := reader.ReadString('\n')
				if strings.ToLower(strings.TrimSpace(ans)) == "y" {
					feedback, ferr := CollectVoiceInput(VoiceOptions{
						DurationSec: opts.VoiceDuration,
						Backend:     opts.VoiceBackend,
					})
					if ferr == nil && feedback != "" {
						research.RawReport = research.RawReport + "\n\n## User Corrections & Feedback\n\n" + feedback
					}
				}
			}

			description = fmt.Sprintf("%s\n\n---\n\n## Pre-PRD Research Findings\n\n%s",
				description, summariseResearch(research.RawReport))
		}
	}

	// Validate ForcedType if set
	if opts.ForcedType != "" {
		valid := false
		for _, t := range validPRDTypes {
			if t == opts.ForcedType {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("unknown type %q — valid types: %s", opts.ForcedType, strings.Join(validPRDTypes, ", "))
		}
	}

	// ─── Step 2: Load project config ──────────────────────────
	cfg, _ := config.Load(opts.BaseDir)
	stagingURL := cfg.Project.StagingURL
	if stagingURL == "" {
		stagingURL = "<staging-url — set project.stagingUrl in .chief/config.yaml>"
	}
	backlogBase := cfg.Project.BacklogBase
	if backlogBase == "" {
		backlogBase = "<backlog-base — set project.backlogBase in .chief/config.yaml>"
	}
	notifyUserID := cfg.Project.NotifyUserID
	if notifyUserID == "" {
		notifyUserID = "<notify-user-id>"
	}
	testCmd := cfg.Project.TestCommand
	if testCmd == "" {
		testCmd = "pytest ../tests/ -x -q 2>&1 | tail -10"
	}

	// Read CLAUDE.md for architecture context (first 100 lines)
	claudeCtx := readCLAUDEMd(opts.BaseDir)

	fmt.Printf("Generating PRD for: %q\n", description)
	fmt.Println()

	// ─── Step 3: Call claude -p to classify + generate ────────
	prompt := buildGeneratePrompt(description, opts.ForcedType, stagingURL, backlogBase, notifyUserID, testCmd, claudeCtx)

	out, err := runClaudeNonInteractive(opts.CLIPath, opts.BaseDir, prompt)
	if err != nil {
		return fmt.Errorf("claude failed: %w", err)
	}

	// ─── Step 4: Parse slug + PRD content ─────────────────────
	slug, prdContent, prdType, err := parseGenerateOutput(out)
	if err != nil {
		// Print raw output to help debug
		fmt.Println("Raw output from Claude:")
		fmt.Println(out)
		return fmt.Errorf("parse output: %w", err)
	}

	// ─── Step 5: Write PRD ────────────────────────────────────
	prdDir := filepath.Join(opts.BaseDir, ".chief", "prds", slug)
	if err := os.MkdirAll(prdDir, 0755); err != nil {
		return fmt.Errorf("create PRD directory: %w", err)
	}
	prdPath := filepath.Join(prdDir, "prd.md")
	if err := os.WriteFile(prdPath, []byte(prdContent), 0644); err != nil {
		return fmt.Errorf("write prd.md: %w", err)
	}

	fmt.Printf("PRD created\n\n")
	fmt.Printf("  Type:  %s\n", prdType)
	fmt.Printf("  Slug:  %s\n", slug)
	fmt.Printf("  File:  %s\n\n", prdPath)
	fmt.Printf("Review:  cat %s\n", prdPath)
	fmt.Printf("Start:   chief %s\n", slug)
	return nil
}

// buildGeneratePrompt constructs the meta-prompt for PRD classification and generation.
func buildGeneratePrompt(description, forcedType, stagingURL, backlogBase, notifyUserID, testCmd, claudeCtx string) string {
	typeSection := ""
	if forcedType != "" {
		typeSection = fmt.Sprintf("\nForced type: %s — use this type, do not auto-detect.\n", forcedType)
	}

	return fmt.Sprintf(`You are a senior engineer. Classify this work request and generate a Chief PRD.

## Project architecture context (from CLAUDE.md)
%s

## Project settings
Staging URL: %s
Backlog base: %s
Notify user ID: %s
Test command: %s

## Work request
%q
%s
---

## Output format (STRICT — output ONLY this, nothing else before or after)

SLUG: <lowercase-hyphenated-slug>
TYPE: <type>
---BEGIN PRD---
<full PRD markdown>
---END PRD---

## Step 1 — Classify

Every kind of developer work maps to one of these types:

| Type           | Use when the request is about...                                          |
|----------------|---------------------------------------------------------------------------|
| bug-fix        | Something broken, wrong output, regression, crash, error on page          |
| feature        | New functionality, new screen, new API endpoint, new integration           |
| test-coverage  | Missing tests, improving coverage, writing tests for existing code         |
| robustness     | Adding retries, validation, error handling, edge cases, input sanitization |
| exploration    | Understanding how something works, mapping a flow, spike, research         |
| server-debug   | Production/staging issue, 500 error, slow response, k8s pod crash          |
| refactor       | Code cleanup, rename, restructure — no behavior change                    |
| security       | Vulnerability, auth bypass, injection risk, audit, secrets exposure        |
| perf           | Slow query, high memory, optimization, profiling, load testing             |
| migration      | Upgrading library, replacing deprecated API, moving to new pattern         |
| review         | Post-PR audit, architecture alignment check, design review                 |
| batch-fix      | Multiple backlog issues fixed in one cycle                                 |

Pick the ONE type that best fits. If the request spans multiple concerns, pick the dominant one.

## Step 2 — Pick slug (3-5 words, lowercase, hyphens, describes the work — no dates)

## Step 3 — Generate PRD

### PRD structure rules
- Title: # PRD: <slug> — <short description of the outcome>
- Introduction (2-3 sentences): what will be done, why it matters, which part of the codebase is affected
  - State each requirement as: "The system SHALL <action>"
  - Reference specific CLAUDE.md rules that apply
- User Stories: ### US-00N: <Title> | Priority <1-5> | <work-type label>
- Each story has:
  - **Description**: one sentence — what the agent does and why
  - **Acceptance Criteria** (GWT + bash verify):
    - GIVEN <precondition — system state before the action>
    - WHEN <action the agent takes>
    - THEN <observable outcome>
    - Verify: <bash command that exits 0 on success — e.g. pytest, curl, mypy, playwright>
- Success Metrics section at the end (2-4 measurable outcomes)

### Story templates by type

BUG-FIX (7 stories):
- US-001 Triage: Reproduce the bug locally; trace the code path; write the root cause to progress.md.
  If root cause is unclear, post diagnosis to Backlog (plain text, notifyUserId=%s) and stop — do NOT emit <chief-done/>.
- US-002 Fix: Implement the root cause fix; no hardcoding; backward-compatible; syntax-check all changed files.
  Write test FIRST (TDD) that fails before the fix and passes after.
- US-003 Architecture & DRY Review: (see ALWAYS checklist below)
- US-004 Unit + Type Tests: Run "%s" — 0 failures; run mypy/tsc on changed files — 0 new errors.
- US-005 Local Smoke: If localhost is running, open browser → login → navigate to affected page → screenshot.
  Skip gracefully with a note if servers are not running.
- US-006 Deploy + Staging Verify: git commit + push dev branch; poll %s/api/health (15× every 30s);
  kubectl rollout status; playwright-cli staging screenshot confirming fix.
- US-007 Backlog Resolved: Upload screenshot; post plain-text comment (no Markdown, user-facing language,
  numbered verification steps, notify %s); set statusId=3.

FEATURE (6 stories):
- US-001 Spec & Pattern Study: Read all existing similar features; define exact API shape / UI contract;
  write spec to progress.md; confirm no existing code already does this.
- US-002 Implement: Build following existing patterns; no new abstractions unless spec requires;
  write tests FIRST (TDD), then implement.
- US-003 Architecture & DRY Review: (see ALWAYS checklist below)
- US-004 Tests: Unit tests for all new logic; integration test for any new endpoint;
  follow existing test file naming and structure.
- US-005 Deploy + Demo: git commit + push; poll health; playwright-cli end-to-end walkthrough + screenshot.
- US-006 Backlog Update: Comment with demo screenshot; describe what was built in plain language; notify user.

TEST-COVERAGE (4 stories):
- US-001 Coverage Audit: Run coverage report (pytest --cov or equivalent); list all uncovered lines/paths;
  rank gaps by risk (auth > payments > data > UI).
- US-002 Write Tests: Cover every identified gap; follow existing test patterns exactly;
  no new test infrastructure or helpers.
- US-003 Verify Coverage: Show before/after coverage numbers; all tests pass; no flaky tests;
  no suppression comments.
- US-004 Document Gaps: Write coverage-report.md — any deliberately untested paths with clear justification.

ROBUSTNESS (5 stories):
- US-001 Audit Weak Points: Map all unhandled errors, missing retries, missing input validation
  in the affected area; write findings to progress.md.
- US-002 Harden: Add error handling / retries / validation; happy-path behavior MUST be unchanged;
  no new endpoints or schema changes.
- US-003 Architecture & DRY Review: verify changes are purely additive; no behavior change on happy path.
- US-004 Error-Path Tests: Test every new error path with pytest parametrize; cover boundary inputs.
- US-005 Deploy + Verify: Confirm happy path works on staging after deploy; run smoke test.

EXPLORATION (3 stories):
- US-001 Read and Map: Trace the full code path end-to-end; identify every file involved;
  draw data-flow diagram in progress.md; read CLAUDE.md for context.
- US-002 Find Gaps: Document what is missing, ambiguous, could break, or is undocumented;
  NO code changes in this story.
- US-003 Report: Write exploration-report.md with: findings, risk assessment, recommended next steps,
  and one suggested follow-up PRD slug.

SERVER-DEBUG (5 stories):
- US-001 Reproduce: curl the failing endpoint on staging; capture exact HTTP status, error body, stack trace;
  check kubectl logs and recent deployments.
- US-002 Diagnose: Read logs + relevant code; identify root cause; write diagnosis to progress.md
  with: symptom, cause, affected path, impact scope.
- US-003 Fix + Architecture Review: Fix root cause; run full architecture review checklist;
  fix all violations found.
- US-004 Deploy + Verify: Push fix; poll health; confirm original error is gone; save before/after screenshots.
- US-005 Post-mortem: Write postmortem.md — timeline, root cause, fix, prevention steps, monitoring gaps.

REFACTOR (5 stories):
- US-001 Scope: Define exactly what will change and what will NOT change; write scope to progress.md;
  confirm external API / test contracts are unchanged.
- US-002 Refactor: Make structural changes; no behavior changes; run tests after each logical chunk.
- US-003 Architecture & DRY Review: verify no unintended behavior changes; DRY violations resolved.
- US-004 Verify Behavior Unchanged: Run full test suite before and after; diff must show 0 test failures added;
  grep for any test that was removed or weakened.
- US-005 Document: Update any comments/docs that reference old structure.

SECURITY (5 stories):
- US-001 Threat Model: List all entry points, trust boundaries, sensitive data flows in affected area;
  write threat model to progress.md.
- US-002 Find Vulnerabilities: Test for OWASP Top 10 in scope; check for injection, broken auth,
  hardcoded secrets, insecure defaults.
- US-003 Fix Critical Issues: Fix all HIGH/CRITICAL findings first; add input validation and output encoding;
  no security-suppression comments.
- US-004 Architecture Review + Re-scan: Re-run checks after fix; verify no new vectors introduced.
- US-005 Report: Write security-report.md with: findings, severity, fix applied, remaining known risks.

PERF (5 stories):
- US-001 Baseline: Measure current performance with a repeatable benchmark command; write baseline to progress.md.
- US-002 Profile: Find the bottleneck (slow query, N+1, memory leak, CPU hotspot); instrument with tools.
- US-003 Optimize: Apply targeted fix to the bottleneck ONLY; no speculative optimization.
- US-004 Measure Improvement: Re-run baseline benchmark; show before/after numbers; improvement must be ≥20%%.
- US-005 Deploy + Monitor: Push; watch metrics for 10 minutes post-deploy; confirm no regression.

MIGRATION (5 stories):
- US-001 Inventory: List every file, function, and call-site that uses the old pattern/library.
- US-002 Migrate: Replace old with new in all identified locations; keep old as fallback if needed.
- US-003 Architecture & DRY Review: verify no mixed old/new patterns remain; no duplication.
- US-004 Tests: Run full suite; 0 new failures; confirm new pattern is tested in at least 2 test cases.
- US-005 Cleanup: Remove old pattern, deprecated imports, feature flags, migration shims.

REVIEW (5 stories):
- US-001 Read All Changes: git diff main; read every changed file in full; list files changed.
- US-002 Architecture Alignment: Compare each change against CLAUDE.md; document ALL deviations.
- US-003 Test Coverage Check: Run full suite; identify uncovered new code paths; list them.
- US-004 Fix All Deviations: Fix every deviation found in US-002 and US-003; re-verify after each fix.
- US-005 Sign-off: Write review-complete.md — all findings, fixes applied, open known issues, sign-off.

BATCH-FIX (9 stories):
- US-001 Scan: Run full test suite + linter; list all failures and warnings; rank HIGH/MEDIUM/LOW.
- US-002 Triage Comments: For each HIGH issue, post triage comment to Backlog (plain text, notify %s).
- US-003 Fix Small Issues: Fix all LOW/MEDIUM issues (< 30 min each); commit each fix separately.
- US-004 Fix Large Issues: Fix all HIGH issues; write test FIRST for each; commit each fix separately.
- US-005 Architecture & DRY Review: (see ALWAYS checklist below)
- US-006 Full Test Suite: Run "%s"; 0 failures; mypy/tsc — 0 new errors.
- US-007 Deploy: git push dev; poll %s/api/health; kubectl rollout status.
- US-008 Staging Playwright: playwright-cli run against staging; screenshot each fixed area.
- US-009 Batch Backlog Update: For each resolved issue, post plain-text comment + notify %s; set statusId=3.

---

ALWAYS include in Architecture & DRY Review stories:
1. Read CLAUDE.md before reviewing anything
2. Layer separation: no cross-layer imports, no business logic in API handlers, no DB calls in UI code
3. API contract: every existing response field still present with same type — grep the field name across tests
4. Caller scan: grep -rn for EVERY modified function name; check all callers still work
5. DRY check: no block of logic copy-pasted; any logic repeated 3+ times must be extracted
6. TDD check: every new function has at least one test written before or alongside it
7. Hardcoding check: zero domain-specific literals (IDs, names, codes, magic numbers) in source
8. Architecture compliance: new code extends existing patterns from CLAUDE.md; no invented patterns
9. Design compliance: no new abstractions unless the spec explicitly requires them
10. Blast radius: count files that import changed modules; list them
11. Atomicity test: each story can be described in ONE sentence without "and" — if not, split it
12. Fix priority for violations: build → type errors → test failures → logic bugs → warnings
13. Do NOT emit <chief-done/> until ALL violations are fixed and verified

ALWAYS include in fix/implement stories:
- TDD: write the failing test first, then write the implementation to make it pass
- Commit BEFORE running tests (enables clean git revert HEAD if tests fail)
- Commit format: "fix(<area>): <one sentence>" or "feat(<area>): <one sentence>"
- On test failure: git revert HEAD --no-edit → diagnose → fix differently → re-commit
- Follow existing patterns — no new patterns without explicit justification in progress.md
- DRY: if similar logic exists elsewhere, reuse it; never duplicate

ALWAYS include in test stories:
- Tests run AFTER the fix is committed, not before
- Never modify a test to make it pass — fix the implementation
- Never suppress: @ts-ignore, eslint-disable, # type: ignore, noqa
- Test name format: test_<what>_<condition>_<expected> e.g. test_login_empty_password_raises
`, claudeCtx, stagingURL, backlogBase, notifyUserID, testCmd, description, typeSection,
		notifyUserID, testCmd, stagingURL, notifyUserID,
		notifyUserID, testCmd, stagingURL, notifyUserID)
}

// parseGenerateOutput extracts slug, type, and PRD content from Claude's output.
func parseGenerateOutput(output string) (slug, prdContent, prdType string, err error) {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "SLUG:") {
			slug = strings.TrimSpace(strings.TrimPrefix(line, "SLUG:"))
		}
		if strings.HasPrefix(line, "TYPE:") {
			prdType = strings.TrimSpace(strings.TrimPrefix(line, "TYPE:"))
		}
	}

	beginIdx := strings.Index(output, "---BEGIN PRD---")
	endIdx := strings.Index(output, "---END PRD---")

	if beginIdx == -1 || endIdx == -1 || endIdx <= beginIdx {
		return "", "", "", fmt.Errorf("could not find ---BEGIN PRD--- / ---END PRD--- markers in output")
	}

	prdContent = strings.TrimSpace(output[beginIdx+len("---BEGIN PRD---") : endIdx])

	if slug == "" {
		return "", "", "", fmt.Errorf("SLUG: line missing from output")
	}
	if prdContent == "" {
		return "", "", "", fmt.Errorf("PRD content is empty")
	}

	// Sanitize slug and validate non-empty
	slug = sanitizeSlug(slug)
	if slug == "" {
		return "", "", "", fmt.Errorf("SLUG sanitized to empty string — Claude output may be malformed")
	}
	return slug, prdContent, prdType, nil
}

// sanitizeSlug lowercases and removes any character that's not a-z, 0-9, or hyphen.
func sanitizeSlug(s string) string {
	s = strings.ToLower(s)
	s = slugNonAlphanumericRE.ReplaceAllString(s, "-")
	s = slugMultiHyphenRE.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// runClaudeNonInteractive runs claude -p with stream-json output and returns
// the assistant text extracted from the result message.
// runClaudeNonInteractive runs claude -p in non-interactive mode and returns the
// assistant text. Pass a custom env slice (e.g. with ANTHROPIC_API_KEY stripped)
// as the optional fourth argument; omit to inherit the current process environment.
func runClaudeNonInteractive(cliPath, workDir, prompt string, env ...[]string) (string, error) {
	cmd := exec.Command(cliPath,
		"--dangerously-skip-permissions",
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
	)
	cmd.Dir = workDir
	if len(env) > 0 && env[0] != nil {
		cmd.Env = env[0]
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// stdout carries the stream-json even on failure (e.g. billing error).
		// Try to extract a human-readable message from it before falling back to stderr.
		if msg := extractTextFromStreamJSON(stdout.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return "", fmt.Errorf("%w\nstderr: %s", err, s)
		}
		return "", err
	}

	// Extract text from stream-json: look for the "result" message which contains
	// the full assistant response in the "result" field.
	return extractTextFromStreamJSON(stdout.String()), nil
}

// extractTextFromStreamJSON parses stream-json output from claude CLI and
// returns the assistant text from the final "result" message.
// Falls back to concatenating all "assistant" content blocks if no result found.
func extractTextFromStreamJSON(raw string) string {
	type streamMsg struct {
		Type   string `json:"type"`
		Result string `json:"result"`
	}
	type contentBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type assistantMsg struct {
		Type    string `json:"type"`
		Message struct {
			Content []contentBlock `json:"content"`
		} `json:"message"`
	}

	var resultText string
	var assistantText strings.Builder

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Try "result" message first (contains the full final response)
		var msg streamMsg
		if err := jsonUnmarshal([]byte(line), &msg); err == nil && msg.Type == "result" && msg.Result != "" {
			resultText = msg.Result
			continue
		}

		// Accumulate assistant content blocks as fallback
		var am assistantMsg
		if err := jsonUnmarshal([]byte(line), &am); err == nil && am.Type == "assistant" {
			for _, block := range am.Message.Content {
				if block.Type == "text" {
					assistantText.WriteString(block.Text)
				}
			}
		}
	}

	if resultText != "" {
		return resultText
	}
	return assistantText.String()
}

// readCLAUDEMd reads the first 100 lines of CLAUDE.md from baseDir if it exists.
func readCLAUDEMd(baseDir string) string {
	path := filepath.Join(baseDir, "CLAUDE.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "(no CLAUDE.md found — generate will use generic architecture rules)"
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 100 {
		lines = lines[:100]
	}
	return strings.Join(lines, "\n")
}
