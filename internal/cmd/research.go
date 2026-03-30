package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ResearchOptions configures the pre-PRD auto-research pass.
type ResearchOptions struct {
	// Description is the raw work request
	Description string

	// BaseDir is the repo root
	BaseDir string

	// CLIPath is the Claude binary
	CLIPath string

	// OutputFile is where to write the research report (default: .chief/research-<slug>.md)
	OutputFile string
}

// ResearchResult contains the gathered context for PRD generation.
type ResearchResult struct {
	// RepoSummary is a brief description of what the repo does
	RepoSummary string

	// AffectedFiles lists files likely relevant to the request
	AffectedFiles []string

	// ArchRules is a summary of key architecture rules from CLAUDE.md
	ArchRules string

	// OngoingWork describes any in-progress work that could conflict
	OngoingWork string

	// RawReport is the full research report markdown
	RawReport string

	// ReportPath is where the report was saved
	ReportPath string
}

// researchCacheTTL is how long a research report is considered fresh.
const researchCacheTTL = 4 * time.Hour

// loadCachedResearch returns a previous research report if one exists for a
// related area (slug overlap) and is younger than ttl. Returns nil on miss.
func loadCachedResearch(baseDir, description string, ttl time.Duration) (*ResearchResult, error) {
	chiefDir := filepath.Join(baseDir, ".chief")
	entries, err := os.ReadDir(chiefDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no .chief dir yet — cache miss
		}
		return nil, fmt.Errorf("read .chief dir: %w", err)
	}

	descSlug := sanitizeSlug(description)
	// Extract the first meaningful word group from the slug (first 2 parts)
	descParts := strings.SplitN(descSlug, "-", 4)
	n := 2
	if len(descParts) < n {
		n = len(descParts)
	}
	areaKey := strings.Join(descParts[:n], "-")

	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "research-") || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		// Check if this report covers the same area
		reportSlug := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "research-"), ".md")
		if reportSlug == "" {
			continue
		}
		if !strings.HasPrefix(reportSlug, areaKey) && !strings.HasPrefix(areaKey, reportSlug) {
			continue
		}

		// Check freshness
		reportPath := filepath.Join(chiefDir, entry.Name())
		info, err := os.Stat(reportPath)
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) > ttl {
			continue // expired
		}

		// Cache hit — load and return
		data, err := os.ReadFile(reportPath)
		if err != nil {
			continue
		}
		return &ResearchResult{
			RawReport:  string(data),
			ReportPath: reportPath,
		}, nil
	}
	return nil, nil
}

// RunResearch runs a pre-PRD research pass: reads the codebase, identifies
// affected areas, and produces a context report that the PRD generator uses
// to write accurate, repo-aware stories.
func RunResearch(opts ResearchOptions) (*ResearchResult, error) {
	if opts.BaseDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get cwd: %w", err)
		}
		opts.BaseDir = cwd
	}
	if opts.CLIPath == "" {
		opts.CLIPath = "claude"
	}

	slug := sanitizeSlug(opts.Description)
	if len(slug) > 40 {
		slug = slug[:40]
	}

	if opts.OutputFile == "" {
		chiefDir := filepath.Join(opts.BaseDir, ".chief")
		os.MkdirAll(chiefDir, 0755)
		opts.OutputFile = filepath.Join(chiefDir, fmt.Sprintf("research-%s.md", slug))
	}

	// Check cache before calling Claude
	if cached, err := loadCachedResearch(opts.BaseDir, opts.Description, researchCacheTTL); err == nil && cached != nil {
		fmt.Printf("Using cached research report: %s (< %s old)\n\n", cached.ReportPath, researchCacheTTL)
		appendResearchLog(opts.BaseDir, opts.Description, "[cache hit]", true)
		return cached, nil
	}

	// Precondition checks (from autoresearch protocol)
	warnings := checkGitPreconditions(opts.BaseDir)
	for _, w := range warnings {
		fmt.Printf("WARN: %s\n", w)
	}

	// Read git memory — recent commits and status inform the research
	gitContext := readGitMemory(opts.BaseDir)

	fmt.Println("Running pre-PRD research pass...")
	fmt.Printf("Request: %q\n\n", opts.Description)

	prompt := buildResearchPrompt(opts.CLIPath, opts.Description, opts.BaseDir, gitContext)

	report, err := runClaudeNonInteractive(opts.CLIPath, opts.BaseDir, prompt)
	if err != nil {
		return nil, fmt.Errorf("research pass failed: %w", err)
	}

	// Save report
	if err := os.WriteFile(opts.OutputFile, []byte(report), 0644); err != nil {
		return nil, fmt.Errorf("write research report: %w", err)
	}

	fmt.Printf("Research complete → %s\n\n", opts.OutputFile)

	// Parse the structured "## Information Completeness" section Claude writes.
	// The research prompt explicitly instructs Claude to write SUFFICIENT or INSUFFICIENT
	// on the line immediately following that header — this is structured output, not brittle heuristics.
	infoSufficient := parseResearchSufficiency(report)

	// Log this research pass for future cache decisions and audit
	appendResearchLog(opts.BaseDir, opts.Description, opts.OutputFile, infoSufficient)

	return &ResearchResult{
		RawReport:  report,
		ReportPath: opts.OutputFile,
	}, nil
}

// checkGitPreconditions verifies the repo is in a clean state for research.
// Returns a list of warning strings (empty = all good).
func checkGitPreconditions(baseDir string) []string {
	var warnings []string

	// Check it's a git repo
	cmd := newGitCmd(baseDir, "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		warnings = append(warnings, "not a git repository — research may miss git context")
		return warnings
	}

	// Check for dirty working tree
	statusCmd := newGitCmd(baseDir, "status", "--porcelain")
	out, _ := statusCmd.Output()
	if len(strings.TrimSpace(string(out))) > 0 {
		warnings = append(warnings, "working tree has uncommitted changes — research will see current (unsaved) state")
	}

	// Check for detached HEAD
	headCmd := newGitCmd(baseDir, "symbolic-ref", "HEAD")
	if err := headCmd.Run(); err != nil {
		warnings = append(warnings, "detached HEAD — git context may be incomplete")
	}

	return warnings
}

// readGitMemory reads recent git history and status to give research context.
func readGitMemory(baseDir string) string {
	var sb strings.Builder

	// Recent commits (what has been worked on)
	logOut, err := newGitCmd(baseDir, "log", "--oneline", "-20").Output()
	if err == nil && len(logOut) > 0 {
		sb.WriteString("## Recent Git History (last 20 commits)\n")
		sb.Write(logOut)
		sb.WriteString("\n")
	}

	// Current branch and status
	branchOut, _ := newGitCmd(baseDir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if len(branchOut) > 0 {
		sb.WriteString("## Current Branch\n")
		sb.Write(branchOut)
		sb.WriteString("\n")
	}

	// Files modified but not committed
	statusOut, _ := newGitCmd(baseDir, "status", "--short").Output()
	if len(strings.TrimSpace(string(statusOut))) > 0 {
		sb.WriteString("## Uncommitted Changes\n")
		sb.Write(statusOut)
		sb.WriteString("\n")
	}

	// What changed in the last commit (understand ongoing work)
	diffOut, err := newGitCmd(baseDir, "diff", "HEAD~1", "--stat").Output()
	if err == nil && len(diffOut) > 0 {
		sb.WriteString("## Last Commit Changes (diff stat)\n")
		sb.Write(diffOut)
		sb.WriteString("\n")
	}

	return sb.String()
}

// newGitCmd creates an exec.Cmd for a git command in the given directory.
func newGitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd
}

// appendResearchLog appends a row to .chief/research-log.tsv (TSV format,
// inspired by autoresearch results logging protocol).
func appendResearchLog(baseDir, description, reportPath string, infoSufficient bool) {
	logPath := filepath.Join(baseDir, ".chief", "research-log.tsv")

	// Create with header if it doesn't exist
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		os.MkdirAll(filepath.Dir(logPath), 0755)
		header := "timestamp\tdescription\treport_path\tinfo_sufficient\n"
		os.WriteFile(logPath, []byte(header), 0644)
	}

	sufficient := "no"
	if infoSufficient {
		sufficient = "yes"
	}

	row := fmt.Sprintf("%s\t%s\t%s\t%s\n",
		time.Now().Format(time.RFC3339),
		strings.ReplaceAll(description, "\t", " "),
		reportPath,
		sufficient,
	)

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(row)
}

// buildResearchPrompt constructs the research prompt for Claude.
// Skills are listed as an index — the research agent reads the relevant ones itself using its file tools.
func buildResearchPrompt(cliPath, description, baseDir, gitContext string) string {
	claudeMd := readCLAUDEMd(baseDir)
	// Pass skill index only — the research agent has file-reading tools and will read
	// the skill files it judges relevant. No separate claude -p call needed.
	skillsCtx := readSkillsAndDocs(baseDir)

	return fmt.Sprintf(`You are a senior engineer doing a pre-PRD research pass for a work request.
Your job is to read this codebase and produce a concise context report that will
be used to write an accurate, repo-aware PRD. Do NOT write code or make changes.

## Work request
%q

## Architecture context (CLAUDE.md)
%s

## Git memory (recent history — read this before anything else)
%s

## Existing skills and docs in this repo
%s

---

## Research tasks

**STEP 0 — Scan for prior research in .chief/**
Before doing anything else, check whether prior research exists that you can build on:
1. Glob ".chief/research-*.md" to list existing research reports in this repo
2. Read the ones that look relevant — same feature area, same files, same tech stack, or a prior phase of the same work
3. From each relevant report, extract: affected files already confirmed, architecture constraints already found, recommended approaches already validated, web search results already gathered
4. Use these as your starting context — skip re-investigating what is already confirmed and still true
5. Note what has changed since those reports: new commits, new files, modified dependencies

This avoids duplicate work across phases. For multi-phase features (Phase A then B then C), each phase inherits what all prior phases discovered.

**STEP 1 — Read git memory**
The git history above shows what has been worked on recently. Before researching:
- Are there commits that overlap with this request? (avoid duplicate work)
- Are there in-progress changes that could conflict?
- Does the last commit diff reveal anything about the current state?

**STEP 2 — Understand the request**
- What is the user asking for, in concrete terms?
- What type of work is this? (bug-fix / feature / test-coverage / robustness / exploration / server-debug / refactor / security / perf / migration / review)
- Check the skills and docs section above — does an existing skill or pattern already handle part of this?

**STEP 3 — Find affected code**
- Search for files, functions, classes relevant to the request
- Use Read, Grep, Glob to explore — do NOT guess file names
- Skip files already confirmed in prior research reports (STEP 0) unless they may have changed
- For bugs: trace the full code path that produces the wrong output
- For features: find where similar features are implemented (follow existing patterns)
- For tests: find existing test files, check what is and isn't covered
- For exploration: map the full data/control flow end-to-end
- Check if any .claude/skills/ or docs/ files describe the affected area

**STEP 4 — Assess blast radius**
For each affected file, evaluate:
- How many other files import it? (grep for imports)
- Is it in a critical path? (auth, payments, database, API gateway)
- How many tests cover it?
- Risk level: HIGH (>10 dependents or critical path) / MEDIUM (3-10) / LOW (<3)

**STEP 5 — Fix priority order** (if this is a bug-fix or robustness work)
Fixes should be prioritized in this order:
1. Build failures first (nothing works if it doesn't compile)
2. Type errors (prevent cascading bugs)
3. Test failures (verify correctness)
4. Logic bugs and edge cases
5. Warnings and polish

**STEP 6 — Identify risks**
- Shared utilities or models touched by this change?
- Callers of affected functions (grep for all call sites)
- API endpoints the frontend calls — would the response shape change?
- Edge cases the work request doesn't mention
- Ongoing work that could conflict (from git memory)

**STEP 7 — Web search (if needed)**
If the request involves an external library, API, framework version, or technology you are
uncertain about, use WebSearch to look up the latest docs or known issues before answering.
Examples of when to search: new library version, cloud API change, security advisory,
framework migration guide, performance best practices for a specific tool.
Skip searching for things already covered in prior research (STEP 0) unless the version has changed.
Cite any search results used.

**STEP 8 — Assess information completeness**
Is there enough information to implement a correct fix/feature?
Sufficient means ALL of the following are known:
- Which file(s) and code path are responsible
- What the correct behavior should be
- Whether the fix is isolated or risks breaking other flows
- Enough to write the story acceptance criteria with real bash commands

---

## Output format

Write a structured markdown report:

# Research Report: <request summary>

## Work Type
<type>

## Affected Files
| File | Why relevant | Risk level |
|------|-------------|------------|
| path/to/file.py | contains the X function that... | LOW/MEDIUM/HIGH |

## Current Behavior
<trace of what the code does today, in plain language>

## Recommended Approach
<concrete approach: what to change, where, how — or "BLOCKED: need more info">
<one-sentence description of the change — if it needs "and", split into two stories>

## Fix Priority Order (if applicable)
<list fixes in the order they should be implemented: build → types → tests → logic → warnings>

## Architecture Constraints
<relevant rules from CLAUDE.md that apply to this work>

## Blast Radius
- Files directly changed: N
- Callers of changed functions: <list>
- Risk level: LOW / MEDIUM / HIGH

## Prior Research Used
<list the .chief/research-*.md files you read in STEP 0 and what you carried forward from each — or "None found">

## Git Conflicts
<any in-progress work from git memory that could conflict — or "None detected">

## Existing Skills / Patterns Used
<list any .claude/skills/ or docs/ files that are relevant — or "None found">

## Web Search Results (if used)
<citations and key findings from any web searches performed — or "No search needed">

## Information Completeness
SUFFICIENT / INSUFFICIENT

### If insufficient — questions needed before work starts:
1. <specific question with lettered options: a) b) c)>

## Test File
<path to the relevant test file(s)>

## Suggested PRD Slug
<3-5 word slug>
`, description, claudeMd, gitContext, skillsCtx)
}

// skillInfo holds metadata about one discovered skill/command.
type skillInfo struct {
	Name string // directory name, e.g. "k8s-debug"
	Path string // absolute path to the skill directory
	Desc string // one-line description from frontmatter
}

// discoverAllSkills finds skills from repo-local dirs AND the global ~/.claude/commands/.
// Repo-local skills take precedence over global ones with the same name.
func discoverAllSkills(baseDir string) []skillInfo {
	var skills []skillInfo
	seen := map[string]bool{}

	dirs := []string{
		filepath.Join(baseDir, ".claude", "skills"),
		filepath.Join(baseDir, ".claude", "commands"),
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".claude", "commands"))
	}

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || seen[e.Name()] {
				continue
			}
			seen[e.Name()] = true
			fullPath := filepath.Join(dir, e.Name())
			skills = append(skills, skillInfo{
				Name: e.Name(),
				Path: fullPath,
				Desc: readSkillDescription(fullPath),
			})
		}
	}
	return skills
}

// selectRelevantSkills uses claude -p to intelligently pick which skills are
// relevant to the current task, then returns the full content of those skills.
// Falls back gracefully to empty string if Claude is unavailable or nothing matches.
func selectRelevantSkills(cliPath, taskDesc string, skills []skillInfo) string {
	if len(skills) == 0 {
		return ""
	}

	var listSB strings.Builder
	for _, s := range skills {
		if s.Desc != "" {
			listSB.WriteString(fmt.Sprintf("- %s: %s\n", s.Name, s.Desc))
		} else {
			listSB.WriteString(fmt.Sprintf("- %s\n", s.Name))
		}
	}

	prompt := fmt.Sprintf(`Task: %s

Available skills:
%s

Which of these skills are directly relevant to this task?
Reply with ONLY a JSON array of skill names, like: ["k8s-debug"]
If none apply, reply: []
Do not explain.`, taskDesc, listSB.String())

	out, err := exec.Command(cliPath, "-p", prompt, "--output-format", "text").Output()
	if err != nil {
		return "" // graceful degradation — no skills injected
	}

	raw := extractJSON(strings.TrimSpace(string(out)))
	raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(raw), "["), "]"))
	if raw == "" {
		return ""
	}

	selected := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.Trim(strings.TrimSpace(part), `"'`)
		if name != "" {
			selected[name] = true
		}
	}

	var contentSB strings.Builder
	for _, s := range skills {
		if !selected[s.Name] {
			continue
		}
		contentSB.WriteString(fmt.Sprintf("### Skill: %s\n", s.Name))
		for _, fname := range []string{"SKILL.md", "skill.md", "README.md"} {
			data, err := os.ReadFile(filepath.Join(s.Path, fname))
			if err != nil {
				continue
			}
			contentSB.Write(data)
			contentSB.WriteString("\n\n")
			break
		}
	}
	return contentSB.String()
}

// parseResearchSufficiency reads the structured "## Information Completeness" section
// that the research prompt instructs Claude to write. Looks for SUFFICIENT/INSUFFICIENT
// on the line immediately following the section header.
func parseResearchSufficiency(report string) bool {
	lines := strings.Split(report, "\n")
	for i, line := range lines {
		if strings.Contains(line, "Information Completeness") {
			// Check the next non-empty line for SUFFICIENT/INSUFFICIENT
			for _, next := range lines[i+1:] {
				trimmed := strings.TrimSpace(next)
				if trimmed == "" {
					continue
				}
				upper := strings.ToUpper(trimmed)
				return strings.HasPrefix(upper, "SUFFICIENT") && !strings.HasPrefix(upper, "INSUFFICIENT")
			}
		}
	}
	return false
}

// checkResearchSufficiency asks Claude whether the research report has enough
// information to write concrete PRD acceptance criteria.
// Use this for re-evaluation only — hot path uses parseResearchSufficiency instead.
func checkResearchSufficiency(cliPath, report string) bool {
	// Focus on the summary sections at the end — first 6000 chars of the end
	excerpt := report
	if len(excerpt) > 6000 {
		excerpt = excerpt[len(excerpt)-6000:]
	}

	prompt := fmt.Sprintf(`Read this research report and reply with exactly one word — SUFFICIENT or INSUFFICIENT — to indicate whether there is enough information to write concrete, accurate PRD acceptance criteria.

%s`, excerpt)

	out, err := exec.Command(cliPath, "-p", prompt, "--output-format", "text").Output()
	if err != nil {
		return false
	}
	upper := strings.ToUpper(strings.TrimSpace(string(out)))
	return strings.Contains(upper, "SUFFICIENT") && !strings.Contains(upper, "INSUFFICIENT")
}

// readSkillsAndDocs returns a plain-text listing of available skills and docs.
// Used for display and tests. For intelligent injection, use selectRelevantSkills.
func readSkillsAndDocs(baseDir string) string {
	var sb strings.Builder

	// Skills and commands (repo-local)
	for _, subdir := range []string{"skills", "commands"} {
		dir := filepath.Join(baseDir, ".claude", subdir)
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("### Agent %s (.claude/%s/)\n", strings.Title(subdir), subdir))
		for _, e := range entries {
			if e.IsDir() {
				desc := readSkillDescription(filepath.Join(dir, e.Name()))
				if desc != "" {
					sb.WriteString(fmt.Sprintf("- %s/ — %s\n", e.Name(), desc))
				} else {
					sb.WriteString(fmt.Sprintf("- %s/\n", e.Name()))
				}
			} else if strings.HasSuffix(e.Name(), ".md") || strings.HasSuffix(e.Name(), ".yaml") {
				sb.WriteString("- " + e.Name() + "\n")
			}
		}
		sb.WriteString("\n")
	}

	// docs/ — project documentation
	docsDir := filepath.Join(baseDir, "docs")
	if entries, err := os.ReadDir(docsDir); err == nil && len(entries) > 0 {
		sb.WriteString("### Project Docs (docs/)\n")
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".md") || strings.HasSuffix(e.Name(), ".rst") {
				sb.WriteString("- " + e.Name() + "\n")
			}
		}
		sb.WriteString("\n")
	}

	// .chief/prds/ — existing PRDs (ongoing work context)
	prdsDir := filepath.Join(baseDir, ".chief", "prds")
	if entries, err := os.ReadDir(prdsDir); err == nil && len(entries) > 0 {
		sb.WriteString("### Existing PRDs (.chief/prds/)\n")
		for _, e := range entries {
			if e.IsDir() {
				sb.WriteString("- " + e.Name() + "\n")
			}
		}
		sb.WriteString("\n")
	}

	if sb.Len() == 0 {
		return "(no skills, commands, docs, or PRDs found)"
	}
	return sb.String()
}

// readSkillDescription reads the first line of description from a SKILL.md or README.md
// inside a skill/command directory. Returns empty string if not found.
func readSkillDescription(dir string) string {
	for _, fname := range []string{"SKILL.md", "skill.md", "README.md", "readme.md"} {
		data, err := os.ReadFile(filepath.Join(dir, fname))
		if err != nil {
			continue
		}
		// Look for YAML frontmatter description field
		content := string(data)
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "description:") {
				desc := strings.TrimSpace(strings.TrimPrefix(line, "description:"))
				// Trim surrounding quotes
				desc = strings.Trim(desc, `"'`)
				if len(desc) > 80 {
					desc = desc[:80] + "..."
				}
				return desc
			}
		}
		return "" // file exists but no description field
	}
	return ""
}

// summariseResearch extracts the high-signal sections from a research report
// for injection into the PRD generation prompt. Avoids sending the full 3000-word
// report (which wastes ~4000 tokens). Keeps: Work Type, Affected Files,
// Recommended Approach, Architecture Constraints, Blast Radius, Existing Skills.
// Falls back to a character-capped slice if section extraction yields nothing.
func summariseResearch(report string) string {
	keepSections := []string{
		"## Work Type",
		"## Affected Files",
		"## Current Behavior",
		"## Recommended Approach",
		"## Fix Priority Order",
		"## Architecture Constraints",
		"## Blast Radius",
		"## Prior Research Used",
		"## Existing Skills",
		"## Web Search Results",
		"## Information Completeness",
		"## Test File",
		"## Suggested PRD Slug",
	}

	lines := strings.Split(report, "\n")
	var out strings.Builder
	inKeep := false

	for _, line := range lines {
		// Check if this line starts a section we want to keep
		for _, sec := range keepSections {
			if strings.HasPrefix(strings.TrimSpace(line), sec) {
				inKeep = true
				break
			}
		}
		// Check if this line starts a section we want to skip
		if strings.HasPrefix(strings.TrimSpace(line), "## ") {
			skip := true
			for _, sec := range keepSections {
				if strings.HasPrefix(strings.TrimSpace(line), sec) {
					skip = false
					break
				}
			}
			if skip {
				inKeep = false
			}
		}
		if inKeep {
			out.WriteString(line)
			out.WriteString("\n")
		}
	}

	summary := strings.TrimSpace(out.String())
	if len(summary) < 100 {
		// Section extraction failed (unusual report format) — cap raw report instead
		if len(report) > 4000 {
			return report[:4000] + "\n\n[... report truncated for token efficiency ...]"
		}
		return report
	}
	return summary
}

// RunGenerateWithResearch is kept for backwards compatibility.
// Research now runs automatically inside RunGenerate unless SkipResearch is set.
func RunGenerateWithResearch(opts GenerateOptions) error {
	return RunGenerate(opts)
}
