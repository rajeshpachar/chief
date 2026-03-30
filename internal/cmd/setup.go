package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SetupOptions configures the chief setup run.
type SetupOptions struct {
	// BaseDir is the repo root (default: cwd)
	BaseDir string

	// CLIPath is the Claude binary (default: "claude")
	CLIPath string

	// Force writes the config without asking for confirmation
	Force bool

	// DocsURLs are extra documentation URLs the user wants Claude to read
	DocsURLs []string

	// UseSubscription strips ANTHROPIC_API_KEY from the Claude child process env
	// so that Claude uses claude.ai subscription billing instead of API key billing.
	// Should mirror agent.useSubscription from .chief/config.yaml (default: true).
	UseSubscription bool
}

// SetupResult contains the outcome of a setup run.
type SetupResult struct {
	// ConfigPath is where the config was written
	ConfigPath string

	// Updated is true if an existing config was updated (false = created fresh)
	Updated bool

	// ProposedYAML is the config YAML that was written
	ProposedYAML string
}

// setupQuestion is one gap Claude could not fill from the repo alone.
type setupQuestion struct {
	// Key is a short identifier for this question (used in the follow-up prompt)
	Key string `json:"key"`

	// Question is the human-readable prompt to show the user
	Question string `json:"question"`

	// Hint explains why Claude couldn't determine this (e.g. "no .env.example found")
	Hint string `json:"hint"`

	// Default is a suggested value (shown in the prompt, used when user presses Enter)
	Default string `json:"default"`
}

// firstPassResult is the structured JSON Claude returns from the first pass.
type firstPassResult struct {
	// Findings is a brief summary of what Claude discovered in the repo
	Findings string `json:"findings"`

	// YAMLDraft is the best-guess config with empty strings for unknowns
	YAMLDraft string `json:"yaml_draft"`

	// Questions are the specific gaps the user must fill in
	Questions []setupQuestion `json:"questions"`
}

// RunSetup uses Claude to study the repo and produce a complete .chief/config.yaml.
//
// Flow:
//  1. Load existing config (if any)
//  2. Fetch any --docs URLs
//  3. First Claude call — Claude reads the repo using its file tools, produces a
//     draft config + a list of questions it genuinely couldn't answer from the code
//  4. Show what Claude found; ask only the questions Claude flagged
//  5. If there were gaps, a second Claude call finalises the config with the answers
//  6. Show the proposed config; confirm; write
func RunSetup(opts SetupOptions) (*SetupResult, error) {
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

	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println("Chief Setup — studying your repo to configure .chief/config.yaml")
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println()

	// ── Step 1: Load existing config ──────────────────────────────────────
	configPath := filepath.Join(opts.BaseDir, ".chief", "config.yaml")
	existingYAML := ""
	configExists := false
	if raw, err := os.ReadFile(configPath); err == nil {
		existingYAML = string(raw)
		configExists = true
		fmt.Printf("Found existing config — will update/correct it.\n\n")
	}

	// ── Step 2: Fetch --docs URLs ──────────────────────────────────────────
	docContent := ""
	if len(opts.DocsURLs) > 0 {
		fmt.Printf("Fetching %d docs URL(s)...\n", len(opts.DocsURLs))
		var sb strings.Builder
		for _, u := range opts.DocsURLs {
			body, err := fetchURL(u)
			if err != nil {
				fmt.Printf("  Warning: could not fetch %s: %v\n", u, err)
				continue
			}
			fmt.Printf("  ✓ %s (%d chars)\n", u, len(body))
			sb.WriteString("\n### Docs: ")
			sb.WriteString(u)
			sb.WriteString("\n")
			sb.WriteString(truncate(body, 3000))
			sb.WriteString("\n")
		}
		docContent = sb.String()
	}

	// ── Step 2b: Build env (strip API key when using claude.ai subscription) ─
	var claudeEnv []string
	if opts.UseSubscription {
		all := os.Environ()
		claudeEnv = make([]string, 0, len(all))
		for _, e := range all {
			if !strings.HasPrefix(e, "ANTHROPIC_API_KEY=") {
				claudeEnv = append(claudeEnv, e)
			}
		}
	}

	// ── Step 3: First Claude pass — read repo, produce draft + questions ──
	fmt.Println("Asking Claude to read your repo and draft the config...")
	firstPrompt := buildFirstPassPrompt(existingYAML, docContent)

	raw, err := runClaudeNonInteractive(opts.CLIPath, opts.BaseDir, firstPrompt, claudeEnv)
	if err != nil {
		return nil, fmt.Errorf("claude first pass: %w", err)
	}

	result, err := parseFirstPassResult(raw)
	if err != nil {
		return nil, fmt.Errorf("parse claude response: %w\nRaw output:\n%s", err, truncate(raw, 800))
	}

	// ── Step 4: Show findings, ask only Claude's flagged questions ─────────
	fmt.Println()
	fmt.Println("── What Claude found ─────────────────────────────────────")
	fmt.Println(result.Findings)
	fmt.Println()

	answers := map[string]string{}
	if len(result.Questions) > 0 {
		fmt.Printf("── %d thing(s) Claude couldn't determine ─────────────────\n", len(result.Questions))
		reader := bufio.NewReader(os.Stdin)
		for _, q := range result.Questions {
			prompt := q.Question
			if q.Hint != "" {
				fmt.Printf("  (%s)\n", q.Hint)
			}
			if q.Default != "" {
				prompt = fmt.Sprintf("%s [%s]: ", q.Question, q.Default)
			} else {
				prompt = fmt.Sprintf("%s: ", q.Question)
			}
			fmt.Print(prompt)
			line, _ := reader.ReadString('\n')
			line = strings.TrimSpace(line)
			if line == "" {
				line = q.Default
			}
			if line != "" {
				answers[q.Key] = line
			}
		}
		fmt.Println()
	}

	// ── Step 5: Finalise the YAML ──────────────────────────────────────────
	proposedYAML := ""
	if len(answers) > 0 {
		// Second pass: incorporate user answers
		fmt.Println("Finalising config with your answers...")
		finalPrompt := buildFinalPassPrompt(result.YAMLDraft, result.Questions, answers)
		finalRaw, err := runClaudeNonInteractive(opts.CLIPath, opts.BaseDir, finalPrompt, claudeEnv)
		if err != nil {
			return nil, fmt.Errorf("claude final pass: %w", err)
		}
		proposedYAML = extractYAMLBlock(finalRaw)
		if proposedYAML == "" {
			// Fallback: use the draft if finalisation failed to return a block
			proposedYAML = result.YAMLDraft
		}
	} else {
		// No gaps — use the draft directly
		proposedYAML = result.YAMLDraft
	}

	if proposedYAML == "" {
		return nil, fmt.Errorf("no YAML config produced — try running again or use 'chief validate --init' for a template")
	}

	// ── Step 6: Show, confirm, write ──────────────────────────────────────
	fmt.Println("── Proposed .chief/config.yaml ──────────────────────────")
	fmt.Println(proposedYAML)
	fmt.Println("─────────────────────────────────────────────────────────")
	if configExists {
		fmt.Println("(This will replace the existing config.)")
	}

	if !opts.Force {
		fmt.Print("\nWrite this config? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("Aborted — no changes made.")
			return &SetupResult{Updated: configExists, ProposedYAML: proposedYAML}, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return nil, fmt.Errorf("create .chief dir: %w", err)
	}
	if err := os.WriteFile(configPath, []byte(proposedYAML+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("write config: %w", err)
	}

	action := "Created"
	if configExists {
		action = "Updated"
	}
	fmt.Printf("\n%s %s\n", action, configPath)
	fmt.Println("Run 'chief validate' to test it.")

	return &SetupResult{
		ConfigPath:   configPath,
		Updated:      configExists,
		ProposedYAML: proposedYAML,
	}, nil
}

// ── First-pass prompt ─────────────────────────────────────────────────────────

// buildFirstPassPrompt instructs Claude to read the repo itself and produce a
// structured JSON response with a draft config and any unanswered questions.
func buildFirstPassPrompt(existingYAML, docContent string) string {
	var sb strings.Builder

	sb.WriteString(`You are configuring .chief/config.yaml for this repository.

Chief is a developer agent that needs to know:
- How to run local tests (project.testCommand)
- Where staging is deployed (project.validation.stagingUrl / apiUrl)
- How to authenticate API tests (validation.authHeader or validation.login)
- Which API use-case tests to run (validation.apiTests)
- CI/CD pipeline details (validation.cicd)
- Whether there are multiple services to orchestrate (project.services)

## Your task

Read the repository files using your file tools, then produce a JSON response.

**Files to read (read each one that exists):**
- package.json — look at "scripts" for test command; "dependencies" for framework clues
- go.mod — module name, Go version, key dependencies
- requirements.txt, pyproject.toml, setup.py — Python project detection
- Makefile — look for test/lint/run targets
- .env.example, .env.sample, .env.local, .env.test — staging URLs, auth env var names, service ports
- .github/workflows/*.yml OR .gitlab-ci.yml — CI provider, push branch, workflow file
- CLAUDE.md — architecture rules and constraints
- Any service subdirectories: services/, apps/, packages/ (list what you find)
- A few API route files to understand the endpoint structure (src/app/api/, routes/, api/, server/)
- Any existing test files to understand test patterns

`)

	if existingYAML != "" {
		sb.WriteString("**Existing config (preserve what works, only correct what's wrong):**\n```yaml\n")
		sb.WriteString(existingYAML)
		sb.WriteString("\n```\n\n")
	}

	if docContent != "" {
		sb.WriteString("**Additional documentation provided by the user:**\n")
		sb.WriteString(docContent)
		sb.WriteString("\n\n")
	}

	sb.WriteString(`## Output format

Respond with a single JSON object — no prose before or after:

{
  "findings": "2-4 sentences: what you found (language, test command, CI, auth pattern, services)",
  "yaml_draft": "complete YAML string for .chief/config.yaml — leave fields as empty string \"\" if unknown",
  "questions": [
    {
      "key": "short_identifier",
      "question": "Human-readable question to ask the repo owner",
      "hint": "why you couldn't determine this from the code",
      "default": "suggested value or empty string"
    }
  ]
}

## Rules for yaml_draft

- project.testCommand: use the exact command from package.json scripts, go.mod, or Makefile. Never guess.
- project.validation.stagingUrl: use any URL found in .env.example. If not found, set to "" and add to questions.
- project.validation.apiUrl: set this when the repo is a frontend and the API is at a different URL. Omit if same server.
- project.validation.login: use when you see a login endpoint in the code AND no static token pattern. Fields: path, body (a JSON *string* with ${VAR} placeholders — MUST be a quoted YAML string, NOT a nested map, e.g. body: '{"username":"${USERNAME}","password":"${PASSWORD}"}'), tokenField (dot-path in response JSON, e.g. "token" or "data.accessToken"), headerPrefix ("Bearer ").
- project.validation.credentialsFile: set to ".chief/staging.creds" whenever login is used.
- project.validation.authHeader: use when you see a static API key or token env var pattern. Format: "Authorization: Bearer ${VAR_NAME}".
- project.validation.apiTests: write 4-8 tests based on actual routes you found. Include: health check, auth boundary (401), one happy-path test per major feature area. Use "expectStatus" (not "expectedStatus"). The "body" field for POST tests MUST be a JSON string (e.g. body: '{"key":"value"}'), NOT a nested YAML map.
- project.validation.cicd: fill from CI config files. provider: "github"|"gitlab"|"none", branch: push branch, workflow: filename (github only), pollIntervalSec: 30, maxWaitSec: 600.
- project.services: only include when you find clearly separate service directories with their own start commands. Omit for monoliths.

## Rules for questions

Only add a question if you genuinely cannot determine the value from the repo. Do NOT ask about:
- Things you found in the files (test command, CI provider, branch, etc.)
- Things with sensible defaults (pollIntervalSec, maxWaitSec, credentialsFile path)
- Things that can be left blank (optional config fields)

Common questions you might need to ask:
- staging URL (if no .env.example or URL not found there)
- backend API URL (if frontend repo and API URL not in code)
- login token field (if login endpoint found but response schema unclear)
- auth method choice (if unclear whether static token or login-based)
`)

	return sb.String()
}

// ── Parse first-pass result ───────────────────────────────────────────────────

// parseFirstPassResult extracts the JSON from Claude's first-pass response.
// Falls back to treating a YAML block as a no-questions draft if JSON is not found.
func parseFirstPassResult(raw string) (*firstPassResult, error) {
	jsonStr := extractJSON(raw)

	var result firstPassResult
	if jsonStr != "" {
		if err := json.Unmarshal([]byte(jsonStr), &result); err == nil && result.YAMLDraft != "" {
			return &result, nil
		}
	}

	// Fallback: if Claude returned a YAML block instead of JSON, treat it as a
	// no-questions draft config (some models omit the JSON wrapper).
	yamlDraft := extractYAMLBlock(raw)
	if yamlDraft != "" {
		return &firstPassResult{
			Findings:  "Config generated from repo analysis.",
			YAMLDraft: yamlDraft,
		}, nil
	}

	return nil, fmt.Errorf("no valid JSON or YAML block found in Claude's response")
}

// ── Final-pass prompt ─────────────────────────────────────────────────────────

// buildFinalPassPrompt asks Claude to produce the final YAML by incorporating
// the user's answers into the draft config.
func buildFinalPassPrompt(yamlDraft string, questions []setupQuestion, answers map[string]string) string {
	var sb strings.Builder

	sb.WriteString("Produce the final .chief/config.yaml by incorporating the user's answers into this draft.\n\n")

	sb.WriteString("## Draft config\n```yaml\n")
	sb.WriteString(yamlDraft)
	sb.WriteString("\n```\n\n")

	sb.WriteString("## User answers\n\n")
	for _, q := range questions {
		if val, ok := answers[q.Key]; ok && val != "" {
			sb.WriteString(fmt.Sprintf("- **%s** (%s): `%s`\n", q.Key, q.Question, val))
		}
	}

	sb.WriteString(`
## Instructions

1. Apply the user's answers to fill in the corresponding fields in the draft.
2. Keep everything else from the draft unchanged.
3. Preserve all comments from the draft.
4. Do not add new questions or omit existing fields.

Output ONLY the final YAML code block:

` + "```yaml\n```")

	return sb.String()
}

// ── YAML extraction ───────────────────────────────────────────────────────────

// extractYAMLBlock finds the first ```yaml ... ``` block in Claude's output.
func extractYAMLBlock(raw string) string {
	for _, fence := range []string{"```yaml", "```yml"} {
		start := strings.Index(raw, fence)
		if start < 0 {
			continue
		}
		inner := raw[start+len(fence):]
		end := strings.Index(inner, "```")
		if end < 0 {
			continue
		}
		return strings.TrimSpace(inner[:end])
	}
	// Fallback: plain ``` block that looks like YAML
	start := strings.Index(raw, "```")
	if start >= 0 {
		inner := raw[start+3:]
		end := strings.Index(inner, "```")
		if end >= 0 {
			candidate := strings.TrimSpace(inner[:end])
			if strings.HasPrefix(candidate, "project:") || strings.HasPrefix(candidate, "#") ||
				strings.Contains(candidate, "testCommand") {
				return candidate
			}
		}
	}
	return ""
}

// ── URL fetching ──────────────────────────────────────────────────────────────

func fetchURL(u string) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 50_000))
	if err != nil {
		return "", err
	}
	return string(body), nil
}
