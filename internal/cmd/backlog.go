package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minicodemonkey/chief/internal/config"
)

// BacklogOptions configures a chief backlog run.
type BacklogOptions struct {
	// IssueKey is the issue to process, e.g. "ZYGODEV-14".
	// Empty when Batch=true.
	IssueKey string

	// BaseDir is the repo root (default: cwd)
	BaseDir string

	// CLIPath is the Claude binary (default: "claude")
	CLIPath string

	// Batch fetches all open issues and generates one batch PRD.
	Batch bool

	// Force writes the PRD without asking for confirmation.
	Force bool
}

// BacklogResult is returned by RunBacklog.
type BacklogResult struct {
	// PRDPath is where the PRD was written.
	PRDPath string

	// Slug is the PRD slug (directory name under .chief/prds/).
	Slug string
}

// backlogIssue is a partial Backlog API issue response.
type backlogIssue struct {
	ID          int    `json:"id"`
	IssueKey    string `json:"issueKey"`
	Summary     string `json:"summary"`
	Description string `json:"description"`
	Status      struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"status"`
	IssueType struct {
		Name string `json:"name"`
	} `json:"issueType"`
	Assignee *struct {
		Name string `json:"name"`
		ID   int    `json:"id"`
	} `json:"assignee"`
	CreatedUser struct {
		Name string `json:"name"`
	} `json:"createdUser"`
	Created string `json:"created"`
	Updated string `json:"updated"`
}

// backlogComment is a partial Backlog API comment response.
type backlogComment struct {
	ID      int    `json:"id"`
	Content string `json:"content"`
	Created string `json:"created"`
	CreatedUser struct {
		Name string `json:"name"`
	} `json:"createdUser"`
}

// RunBacklog is the entry point for `chief backlog`.
//
// Flow for single issue:
//  1. Load project config (backlogBase, backlogProject, notifyUserId)
//  2. Fetch issue + all comments from Backlog API
//  3. Claude reads the repo + issue, decides case (NEED_INFO/REOPENED/NORMAL),
//     generates a repo-aware PRD with concrete acceptance criteria
//  4. Write PRD to .chief/prds/<slug>/prd.md
//
// Flow for --batch:
//  1. Fetch all open/in-progress issues
//  2. Claude categorises + groups related issues + generates one batch PRD
func RunBacklog(opts BacklogOptions) (*BacklogResult, error) {
	if opts.BaseDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get cwd: %w", err)
		}
		opts.BaseDir = cwd
	}
	if opts.CLIPath == "" {
		opts.CLIPath = "claude"
	}

	// API key from env
	apiKey := os.Getenv("BACKLOG_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("BACKLOG_API_KEY not set — run: export BACKLOG_API_KEY=<your-key>")
	}

	// Load project config for backlogBase / backlogProject / notifyUserId
	cfg, err := config.Load(opts.BaseDir)
	if err != nil {
		cfg = config.Default()
	}
	proj := cfg.Project

	backlogBase := proj.BacklogBase
	if backlogBase == "" {
		backlogBase = os.Getenv("BACKLOG_BASE")
	}
	if backlogBase == "" {
		return nil, fmt.Errorf("backlogBase not set — add to .chief/config.yaml under project.backlogBase or set BACKLOG_BASE env var")
	}

	backlogProject := proj.BacklogProject
	if backlogProject == "" {
		backlogProject = os.Getenv("BACKLOG_PROJECT")
	}

	notifyUserID := proj.NotifyUserID

	// ── Batch mode ────────────────────────────────────────────────
	if opts.Batch {
		return runBacklogBatch(opts, backlogBase, backlogProject, apiKey, notifyUserID)
	}

	// ── Single issue mode ─────────────────────────────────────────
	if opts.IssueKey == "" {
		return nil, fmt.Errorf("usage: chief backlog <ISSUE-KEY>  or  chief backlog --batch")
	}

	fmt.Printf("═══════════════════════════════════════════════════════\n")
	fmt.Printf("Chief Backlog — %s\n", opts.IssueKey)
	fmt.Printf("═══════════════════════════════════════════════════════\n\n")

	// Step 1: fetch issue
	fmt.Printf("Fetching %s...\n", opts.IssueKey)
	issue, err := fetchBacklogIssue(backlogBase, opts.IssueKey, apiKey)
	if err != nil {
		return nil, fmt.Errorf("fetch issue: %w", err)
	}
	fmt.Printf("  Summary: %s\n", issue.Summary)
	fmt.Printf("  Status:  %s (id=%d)\n", issue.Status.Name, issue.Status.ID)
	fmt.Printf("  Type:    %s\n\n", issue.IssueType.Name)

	// Step 2: fetch comments
	comments, err := fetchBacklogComments(backlogBase, opts.IssueKey, apiKey)
	if err != nil {
		// Non-fatal — proceed without comments
		fmt.Printf("  Warning: could not fetch comments: %v\n", err)
	}

	// Step 3: read optional BACKLOG.md for project context
	backlogMd := readBacklogMd(opts.BaseDir)

	// Step 4: Claude reads repo + issue, decides case, generates PRD
	fmt.Printf("Asking Claude to read the repo and generate a PRD...\n\n")
	prompt := buildBacklogPrompt(issue, comments, backlogBase, notifyUserID, backlogMd)

	raw, err := runClaudeNonInteractive(opts.CLIPath, opts.BaseDir, prompt)
	if err != nil {
		return nil, fmt.Errorf("claude failed: %w", err)
	}

	// Step 5: check if Claude decided the issue doesn't apply to this repo
	if skipReason := parseSkipSignal(raw); skipReason != "" {
		fmt.Printf("Skipped — issue does not apply to this repo:\n  %s\n", skipReason)
		fmt.Printf("\nIf this is wrong, run from the other repo.\n")
		return nil, nil
	}

	// Step 6: parse PRD output
	slug, prdContent, err := parseBacklogPRDOutput(raw)
	if err != nil {
		return nil, fmt.Errorf("parse PRD output: %w\nRaw:\n%s", err, truncate(raw, 800))
	}

	// Step 7: show and confirm
	fmt.Printf("── Proposed PRD: %s ─────────────────────────────\n", slug)
	fmt.Println(truncate(prdContent, 2000))
	if len(prdContent) > 2000 {
		fmt.Printf("\n... (%d chars total)\n", len(prdContent))
	}
	fmt.Println("────────────────────────────────────────────────────────")

	if !opts.Force {
		fmt.Print("\nWrite this PRD? [y/N]: ")
		var answer string
		fmt.Scanln(&answer)
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Println("Aborted.")
			return nil, nil
		}
	}

	// Step 7: write PRD
	prdDir := filepath.Join(opts.BaseDir, ".chief", "prds", slug)
	if err := os.MkdirAll(prdDir, 0o755); err != nil {
		return nil, fmt.Errorf("create PRD dir: %w", err)
	}
	prdPath := filepath.Join(prdDir, "prd.md")
	if err := os.WriteFile(prdPath, []byte(prdContent+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("write PRD: %w", err)
	}

	fmt.Printf("\nCreated %s\n", prdPath)
	fmt.Printf("Run: chief %s\n", slug)

	return &BacklogResult{PRDPath: prdPath, Slug: slug}, nil
}

// runBacklogBatch handles --batch: fetches all open issues, lets Claude group
// related ones and generate a single batch PRD.
func runBacklogBatch(opts BacklogOptions, backlogBase, backlogProject, apiKey, notifyUserID string) (*BacklogResult, error) {
	fmt.Printf("═══════════════════════════════════════════════════════\n")
	fmt.Printf("Chief Backlog — Batch mode\n")
	fmt.Printf("═══════════════════════════════════════════════════════\n\n")

	if backlogProject == "" {
		return nil, fmt.Errorf("project.backlogProject not set in .chief/config.yaml — needed for batch mode")
	}

	// Resolve project numeric ID
	projectID, err := fetchBacklogProjectID(backlogBase, backlogProject, apiKey)
	if err != nil {
		return nil, fmt.Errorf("fetch project ID for %s: %w", backlogProject, err)
	}

	fmt.Printf("Fetching open issues for %s (id=%d)...\n", backlogProject, projectID)
	issues, err := fetchAllOpenIssues(backlogBase, projectID, apiKey)
	if err != nil {
		return nil, fmt.Errorf("fetch open issues: %w", err)
	}
	fmt.Printf("  Found %d open/in-progress issues\n\n", len(issues))

	if len(issues) == 0 {
		fmt.Println("No open issues. Nothing to do.")
		return nil, nil
	}

	// Fetch comments for each issue (best-effort, truncated)
	issueComments := map[string][]backlogComment{}
	for _, iss := range issues {
		comments, _ := fetchBacklogComments(backlogBase, iss.IssueKey, apiKey)
		if len(comments) > 5 {
			comments = comments[len(comments)-5:] // keep last 5
		}
		issueComments[iss.IssueKey] = comments
	}

	backlogMd := readBacklogMd(opts.BaseDir)

	fmt.Printf("Asking Claude to categorise, group, and generate batch PRD...\n\n")
	prompt := buildBatchBacklogPrompt(issues, issueComments, backlogBase, backlogProject, notifyUserID, backlogMd)

	raw, err := runClaudeNonInteractive(opts.CLIPath, opts.BaseDir, prompt)
	if err != nil {
		return nil, fmt.Errorf("claude failed: %w", err)
	}

	slug, prdContent, err := parseBacklogPRDOutput(raw)
	if err != nil {
		return nil, fmt.Errorf("parse PRD output: %w\nRaw:\n%s", err, truncate(raw, 800))
	}

	fmt.Printf("── Proposed Batch PRD: %s ───────────────────────\n", slug)
	fmt.Println(truncate(prdContent, 2000))
	fmt.Println("────────────────────────────────────────────────────────")

	if !opts.Force {
		fmt.Print("\nWrite this PRD? [y/N]: ")
		var answer string
		fmt.Scanln(&answer)
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Println("Aborted.")
			return nil, nil
		}
	}

	slug = fmt.Sprintf("batch-%s-%s", strings.ToLower(backlogProject), time.Now().Format("20060102-1504"))
	prdDir := filepath.Join(opts.BaseDir, ".chief", "prds", slug)
	if err := os.MkdirAll(prdDir, 0o755); err != nil {
		return nil, fmt.Errorf("create PRD dir: %w", err)
	}
	prdPath := filepath.Join(prdDir, "prd.md")
	if err := os.WriteFile(prdPath, []byte(prdContent+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("write PRD: %w", err)
	}

	fmt.Printf("\nCreated %s\n", prdPath)
	fmt.Printf("Run: chief %s\n", slug)

	return &BacklogResult{PRDPath: prdPath, Slug: slug}, nil
}

// ── Prompt builders ───────────────────────────────────────────────────────────

func buildBacklogPrompt(issue *backlogIssue, comments []backlogComment, backlogBase, notifyUserID, backlogMd string) string {
	var sb strings.Builder

	sb.WriteString("You are a senior engineer processing a Backlog issue.\n")
	sb.WriteString("Your job: read the issue, read the codebase, then generate a Chief PRD.\n\n")

	// Issue details
	sb.WriteString(fmt.Sprintf("## Issue: %s\n", issue.IssueKey))
	sb.WriteString(fmt.Sprintf("**ID (for API calls):** %d\n", issue.ID))
	sb.WriteString(fmt.Sprintf("**Summary:** %s\n", issue.Summary))
	sb.WriteString(fmt.Sprintf("**Status:** %s (id=%d)\n", issue.Status.Name, issue.Status.ID))
	sb.WriteString(fmt.Sprintf("**Type:** %s\n\n", issue.IssueType.Name))
	sb.WriteString(fmt.Sprintf("**Description:**\n%s\n\n", issue.Description))

	if len(comments) > 0 {
		sb.WriteString("**Comments (newest last):**\n")
		for _, c := range comments {
			sb.WriteString(fmt.Sprintf("— %s (%s):\n%s\n\n", c.CreatedUser.Name, c.Created[:10], c.Content))
		}
	}

	if backlogMd != "" {
		sb.WriteString("## Project Backlog Context\n")
		sb.WriteString(backlogMd)
		sb.WriteString("\n\n")
	}

	sb.WriteString(fmt.Sprintf("## Backlog API\n"))
	sb.WriteString(fmt.Sprintf("Base URL: %s\n", backlogBase))
	sb.WriteString(fmt.Sprintf("API key env var: $BACKLOG_API_KEY\n"))
	if notifyUserID != "" {
		sb.WriteString(fmt.Sprintf("Notify user ID (for comments): %s\n", notifyUserID))
	}
	sb.WriteString(fmt.Sprintf("Status IDs: 1=Open, 2=In Progress, 3=Resolved, 4=Closed\n\n"))

	sb.WriteString(`## Your tasks

**STEP 0 — Decide if this issue belongs to this repo**
The same Backlog project tracks both backend and frontend issues. You may be in
either the backend repo or the frontend repo. Before doing anything else:

1. Look at the repo root to identify what this repo is:
   - Check for package.json with a "scripts" section → likely frontend (React/Next.js/Vue)
   - Check for go.mod, requirements.txt, pyproject.toml, or src/api/ → likely backend
   - Check CLAUDE.md for an explicit repo description
2. Read the issue summary and description to decide if the bug/feature lives here:
   - UI rendering, component state, form fields, CSS, browser behaviour → frontend
   - API endpoints, database, data processing, Python/Go logic → backend
   - Full-stack issues (both API and UI changes needed) → relevant to BOTH repos

**If the issue clearly belongs to the OTHER repo and has no work in this repo:**
Output exactly this (and nothing else):
SKIP: <one sentence explaining which repo owns this issue and why>

**If the issue is full-stack OR belongs here:** continue to STEP 1.

**STEP 1 — Read the codebase**
Use your file tools (Read, Glob, Grep) to find code relevant to this issue.
Read CLAUDE.md first. Then search for the affected area based on the issue summary.
Look at recent git commits if relevant.

**STEP 2 — Decide the case (you decide — do NOT use keyword matching)**
Based on what you read in the issue, comments, and code:

- **NEED_INFO**: The issue description is too vague to identify which code to change.
  You cannot determine the root cause, affected file, or correct fix.
  → Generate a 1-story PRD: post a targeted clarifying question to Backlog, stop.

- **REOPENED**: A previous fix was deployed but QA found it still broken.
  Signs: comments mention "still happening", "not fixed", status was Resolved then reopened.
  → Generate a re-investigation PRD: read prior fix commits, understand what was missed.

- **NORMAL**: Enough information to implement a correct fix.
  → Generate a full fix PRD with all stories.

**STEP 3 — Generate the PRD**

For NORMAL issues, generate these stories in order:

1. **Triage** — Read the issue, search code, confirm root cause.
   If anything is still unclear after reading the code: post a diagnosis comment to Backlog
   (plain text, user-facing language only) and stop (do NOT emit <chief-done/>).
   Only proceed if root cause is 100% confirmed.

2. **Fix** — Implement the fix. Generic, no hardcoding, backward-compatible.
   Exact files and function names based on what you found in STEP 1.

3. **Architecture Review** — Read CLAUDE.md rules. Check every changed file:
   layer separation, no hardcoding, API contract preserved, all callers updated.
   Fix all violations before proceeding.

4. **Tests** — Run the project's test suite. 0 failures required.
   Use the exact test command from the repo (package.json scripts, Makefile, pytest).

5. **Local smoke test** — If the local server is running, open it with playwright-cli.
   Navigate to the affected feature, screenshot it. Skip gracefully if server is down.

6. **Deploy + staging validation** — Commit, push, wait for the server to come up.
   Playwright on staging: login, navigate to the affected feature, screenshot.

7. **Backlog comment + Resolved** — Post a QA-friendly plain-text comment.
   Rules from BACKLOG.md:
   - Plain text ONLY — no Markdown, no bold, no file names, no code references
   - Describe what was wrong in user-facing language
   - Include numbered QA verification steps
   - Set status to Resolved (statusId=3)

**STEP 4 — Club related sub-issues if applicable**
If this issue has multiple sub-problems that touch the same code area,
handle them in a single PRD rather than generating separate ones.

## Output format

Output ONLY:

SLUG: <issue-key-lowercase-short-title>
TYPE: bug-fix
---BEGIN PRD---
<full PRD markdown>
---END PRD---

PRD rules:
- Title: "# PRD: <ISSUE-KEY> — <summary>"
- Embed the issue ID and Backlog base URL so acceptance criteria can use them directly
- Acceptance criteria use real bash commands based on what you found in the repo
- All Backlog API curl commands use the numeric issue ID ` + fmt.Sprintf("(%d)", issue.ID) + `, not the key string
- Comments use ` + fmt.Sprintf("notifiedUserId[]=%s", notifyUserID) + ` if set
- If NEED_INFO: generate only US-001 (post question, stop — no <chief-done/>)
- If NORMAL: all 7 stories with concrete file paths you found in the codebase
`)

	return sb.String()
}

func buildBatchBacklogPrompt(issues []backlogIssue, comments map[string][]backlogComment, backlogBase, backlogProject, notifyUserID, backlogMd string) string {
	var sb strings.Builder

	sb.WriteString("You are a senior engineer processing a batch of Backlog issues.\n")
	sb.WriteString("Your job: read all issues, read the codebase, group related issues, generate one batch PRD.\n\n")

	sb.WriteString(fmt.Sprintf("## Project: %s (%s)\n\n", backlogProject, backlogBase))

	if backlogMd != "" {
		sb.WriteString("## Project Backlog Context\n")
		sb.WriteString(backlogMd)
		sb.WriteString("\n\n")
	}

	sb.WriteString("## Open Issues\n\n")
	for _, iss := range issues {
		sb.WriteString(fmt.Sprintf("### %s — %s\n", iss.IssueKey, iss.Summary))
		sb.WriteString(fmt.Sprintf("**ID:** %d | **Status:** %s | **Type:** %s\n\n", iss.ID, iss.Status.Name, iss.IssueType.Name))
		if iss.Description != "" {
			sb.WriteString(fmt.Sprintf("%s\n\n", truncate(iss.Description, 400)))
		}
		if coms := comments[iss.IssueKey]; len(coms) > 0 {
			sb.WriteString("Recent comments:\n")
			for _, c := range coms {
				sb.WriteString(fmt.Sprintf("  — %s: %s\n", c.CreatedUser.Name, truncate(c.Content, 200)))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("---\n\n")
	}

	sb.WriteString(fmt.Sprintf(`## Backlog API
Base URL: %s
API key env var: $BACKLOG_API_KEY
Notify user ID: %s
Status IDs: 1=Open, 2=In Progress, 3=Resolved, 4=Closed

## Your tasks

**STEP 0 — Identify this repo and filter irrelevant issues**
The same Backlog project tracks both backend and frontend issues. First determine which
repo you are in:
- package.json with scripts / node_modules / React/Next.js → frontend repo
- requirements.txt / pyproject.toml / go.mod / src/api/ → backend repo
- Check CLAUDE.md for an explicit description

Then for each issue, decide: does this issue have work in THIS repo?
- Pure UI/component/CSS/browser issues → frontend only → SKIP in a backend repo
- Pure API/database/Python/Go logic issues → backend only → SKIP in a frontend repo
- Full-stack issues (both layers need changing) → relevant to BOTH repos

Remove SKIP issues from all subsequent steps. Note them in issues-plan.md under
a "WRONG_REPO" section with a one-line reason each.

**STEP 1 — Read the codebase**
Use Read, Glob, Grep to understand the code areas touched by these issues.
Read CLAUDE.md. Find the relevant files for each issue.

**STEP 2 — Categorise each issue**
For each issue decide:
- **NEED_INFO**: Cannot identify affected code from the description. Post clarifying question only.
- **SMALL_FIX**: 1-3 files, isolated, all info present. Fix immediately.
- **LARGE_FIX**: 4+ files, schema change, or touches shared utilities. Fix with extra care.
- **SKIP**: Already resolved, duplicate, or not actionable.

**STEP 3 — Group related issues**
If multiple issues touch the same file or code area, group them.
A grouped fix handles all related issues in one story rather than repeating the same context.
Example: two bugs in the same service function → one fix story, one test story, shared deploy.

**STEP 4 — Generate batch PRD**
Generate stories in this order:
1. Scan & Categorise — for each issue: confirm category, identify root cause, write issues-plan.md
2. Post NEED_INFO comments — plain text questions for each unclear issue (no code changes)
3. Fix all SMALL issues — one commit per issue, test after each
4. Fix all LARGE issues — one commit per issue, extra architecture checks
5. Architecture Review — check all changed files against CLAUDE.md rules (blocking gate)
6. Full test suite — 0 failures required before deploy
7. Single deploy — one push, wait for server to be live
8. Staging validation — Playwright per fixed issue, screenshot per issue
9. Batch Backlog comments + Resolved — plain text, QA verification steps, statusId=3

**Smart grouping rule for PRD:**
When 2+ issues touch the same code area, generate ONE combined fix story:
"Fix ISSUE-X and ISSUE-Y: <shared area>" — reduces context switching and test overhead.

## Output format

Output ONLY:

SLUG: batch-%s-<date>
TYPE: batch-fix
---BEGIN PRD---
<full PRD markdown>
---END PRD---

PRD rules:
- Embed actual issue IDs and keys in every Backlog curl command
- Comments use notifiedUserId[]=%s
- issues-plan.md is the shared state file — every story reads/writes it
- All acceptance criteria use real bash commands found in the repo
`, backlogBase, notifyUserID, strings.ToLower(backlogProject), notifyUserID))

	return sb.String()
}

// ── Output parser ─────────────────────────────────────────────────────────────

// parseSkipSignal returns the skip reason if Claude decided the issue doesn't
// belong to this repo. Returns empty string if the output should be parsed as a PRD.
func parseSkipSignal(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "SKIP:") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "SKIP:"))
		}
	}
	return ""
}

// parseBacklogPRDOutput extracts SLUG and PRD content from Claude's response.
func parseBacklogPRDOutput(raw string) (slug, prdContent string, err error) {
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "SLUG:") {
			slug = strings.TrimSpace(strings.TrimPrefix(line, "SLUG:"))
		}
	}

	start := strings.Index(raw, "---BEGIN PRD---")
	end := strings.Index(raw, "---END PRD---")
	if start < 0 || end < 0 || end <= start {
		// Fallback: try to extract a markdown block
		if md := extractYAMLBlock(raw); md != "" && strings.Contains(md, "PRD") {
			prdContent = md
		} else {
			// Last resort: if the response looks like a PRD itself
			if strings.Contains(raw, "## User Stories") || strings.Contains(raw, "## Introduction") {
				prdContent = strings.TrimSpace(raw)
			}
		}
		if prdContent == "" {
			return "", "", fmt.Errorf("no ---BEGIN PRD--- / ---END PRD--- delimiters in response")
		}
	} else {
		prdContent = strings.TrimSpace(raw[start+len("---BEGIN PRD---") : end])
	}

	if slug == "" {
		// Derive slug from PRD title
		for _, line := range strings.Split(prdContent, "\n") {
			if strings.HasPrefix(line, "# PRD:") {
				title := strings.TrimPrefix(line, "# PRD:")
				slug = sanitizeSlug(title)
				if len(slug) > 40 {
					slug = slug[:40]
				}
				break
			}
		}
	}

	if slug == "" {
		slug = fmt.Sprintf("backlog-%d", time.Now().Unix())
	}

	// Sanitize slug
	slug = sanitizeSlug(slug)
	if len(slug) > 50 {
		slug = slug[:50]
	}

	return slug, prdContent, nil
}

// ── Backlog API client (pure Go — no python3 needed) ─────────────────────────

func fetchBacklogIssue(base, key, apiKey string) (*backlogIssue, error) {
	url := fmt.Sprintf("%s/api/v2/issues/%s?apiKey=%s", base, key, apiKey)
	body, err := backlogGET(url)
	if err != nil {
		return nil, err
	}
	var issue backlogIssue
	if err := json.Unmarshal(body, &issue); err != nil {
		return nil, fmt.Errorf("parse issue JSON: %w — body: %s", err, truncate(string(body), 200))
	}
	return &issue, nil
}

func fetchBacklogComments(base, key, apiKey string) ([]backlogComment, error) {
	url := fmt.Sprintf("%s/api/v2/issues/%s/comments?apiKey=%s&count=20&order=asc", base, key, apiKey)
	body, err := backlogGET(url)
	if err != nil {
		return nil, err
	}
	var comments []backlogComment
	if err := json.Unmarshal(body, &comments); err != nil {
		return nil, fmt.Errorf("parse comments JSON: %w", err)
	}
	return comments, nil
}

func fetchBacklogProjectID(base, projectKey, apiKey string) (int, error) {
	url := fmt.Sprintf("%s/api/v2/projects/%s?apiKey=%s", base, projectKey, apiKey)
	body, err := backlogGET(url)
	if err != nil {
		return 0, err
	}
	var proj struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(body, &proj); err != nil {
		return 0, fmt.Errorf("parse project JSON: %w", err)
	}
	return proj.ID, nil
}

func fetchAllOpenIssues(base string, projectID int, apiKey string) ([]backlogIssue, error) {
	url := fmt.Sprintf("%s/api/v2/issues?apiKey=%s&projectId[]=%d&statusId[]=1&statusId[]=2&count=100&sort=updated&order=desc",
		base, apiKey, projectID)
	body, err := backlogGET(url)
	if err != nil {
		return nil, err
	}
	var issues []backlogIssue
	if err := json.Unmarshal(body, &issues); err != nil {
		return nil, fmt.Errorf("parse issues JSON: %w", err)
	}
	return issues, nil
}

func backlogGET(url string) ([]byte, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 200_000))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

// readBacklogMd reads docs/BACKLOG.md or .chief/BACKLOG.md for project context.
// Returns empty string if not found.
func readBacklogMd(baseDir string) string {
	for _, candidate := range []string{
		filepath.Join(baseDir, "docs", "BACKLOG.md"),
		filepath.Join(baseDir, ".chief", "BACKLOG.md"),
		filepath.Join(baseDir, "BACKLOG.md"),
	} {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		content := string(data)
		// Truncate to avoid burning tokens on a large file
		if len(content) > 4000 {
			content = content[:4000] + "\n\n[... BACKLOG.md truncated]"
		}
		return content
	}
	return ""
}
