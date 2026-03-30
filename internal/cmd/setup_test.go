package cmd

import (
	"strings"
	"testing"
)

// ─── extractYAMLBlock ─────────────────────────────────────────────────────────

func TestExtractYAMLBlock(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "yaml fence",
			input: "Here is the config:\n```yaml\nproject:\n  testCommand: pytest\n```\nDone.",
			want:  "project:\n  testCommand: pytest",
		},
		{
			name: "yml fence",
			input: "```yml\nproject:\n  testCommand: go test\n```",
			want:  "project:\n  testCommand: go test",
		},
		{
			name:  "no fence",
			input: "No code block here",
			want:  "",
		},
		{
			name: "plain fence with project prefix",
			input: "```\nproject:\n  testCommand: npm test\n```",
			want:  "project:\n  testCommand: npm test",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractYAMLBlock(tc.input)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// ─── parseFirstPassResult ─────────────────────────────────────────────────────

func TestParseFirstPassResult(t *testing.T) {
	raw := `{
  "findings": "Node.js repo, test command: npm test, GitHub Actions CI on main branch.",
  "yaml_draft": "project:\n  testCommand: npm test\n",
  "questions": [
    {
      "key": "staging_url",
      "question": "What is your staging API URL?",
      "hint": "No URL found in .env.example",
      "default": ""
    }
  ]
}`
	result, err := parseFirstPassResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Findings == "" {
		t.Error("findings should not be empty")
	}
	if result.YAMLDraft == "" {
		t.Error("yaml_draft should not be empty")
	}
	if len(result.Questions) != 1 {
		t.Errorf("expected 1 question, got %d", len(result.Questions))
	}
	if result.Questions[0].Key != "staging_url" {
		t.Errorf("expected key 'staging_url', got %q", result.Questions[0].Key)
	}
}

func TestParseFirstPassResultInMarkdownFence(t *testing.T) {
	// Claude sometimes wraps JSON in a markdown fence
	raw := "Here is my analysis:\n```json\n{\"findings\":\"Go repo.\",\"yaml_draft\":\"project:\\n  testCommand: go test ./...\\n\",\"questions\":[]}\n```"
	result, err := parseFirstPassResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.YAMLDraft == "" {
		t.Error("yaml_draft should not be empty")
	}
}

func TestParseFirstPassResultFallbackToYAML(t *testing.T) {
	// If Claude returns YAML instead of JSON, gracefully fall back
	raw := "Here is the config:\n```yaml\nproject:\n  testCommand: pytest\n```"
	result, err := parseFirstPassResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !setupContains(result.YAMLDraft, "testCommand: pytest") {
		t.Errorf("yaml_draft should contain testCommand, got: %q", result.YAMLDraft)
	}
}

func TestParseFirstPassResultNoJSON(t *testing.T) {
	_, err := parseFirstPassResult("Nothing useful here.")
	if err == nil {
		t.Error("expected error when no JSON or YAML found")
	}
}

func TestParseFirstPassResultEmptyYAMLDraft(t *testing.T) {
	raw := `{"findings":"found stuff","yaml_draft":"","questions":[]}`
	_, err := parseFirstPassResult(raw)
	if err == nil {
		t.Error("expected error when yaml_draft is empty")
	}
}

// ─── buildFirstPassPrompt ─────────────────────────────────────────────────────

func TestBuildFirstPassPromptInstructsFileReads(t *testing.T) {
	prompt := buildFirstPassPrompt("", "")
	// Must tell Claude to read key files
	for _, file := range []string{"package.json", "go.mod", ".env.example", ".github/workflows"} {
		if !setupContains(prompt, file) {
			t.Errorf("prompt should mention %q", file)
		}
	}
	// Must ask for structured JSON output
	if !setupContains(prompt, "yaml_draft") {
		t.Error("prompt should ask for yaml_draft field")
	}
	if !setupContains(prompt, "questions") {
		t.Error("prompt should ask for questions field")
	}
}

func TestBuildFirstPassPromptIncludesExistingConfig(t *testing.T) {
	existing := "project:\n  testCommand: go test ./...\n"
	prompt := buildFirstPassPrompt(existing, "")
	if !setupContains(prompt, existing) {
		t.Error("prompt should include existing config YAML")
	}
	if !setupContains(prompt, "preserve") {
		t.Error("prompt should tell Claude to preserve what works")
	}
}

func TestBuildFirstPassPromptIncludesDocContent(t *testing.T) {
	docs := "\n### Docs: https://example.com/api\nGET /api/health → 200 OK\n"
	prompt := buildFirstPassPrompt("", docs)
	if !setupContains(prompt, "https://example.com/api") {
		t.Error("prompt should include docs content")
	}
}

// ─── buildFinalPassPrompt ─────────────────────────────────────────────────────

func TestBuildFinalPassPromptIncludesAnswers(t *testing.T) {
	draft := "project:\n  testCommand: npm test\n  validation:\n    stagingUrl: \"\"\n"
	questions := []setupQuestion{
		{Key: "staging_url", Question: "What is your staging URL?"},
	}
	answers := map[string]string{
		"staging_url": "https://metacrm.k8s-dev.hlthclub.in",
	}
	prompt := buildFinalPassPrompt(draft, questions, answers)

	if !setupContains(prompt, draft) {
		t.Error("prompt should include draft config")
	}
	if !setupContains(prompt, "https://metacrm.k8s-dev.hlthclub.in") {
		t.Error("prompt should include the user's answer")
	}
	if !setupContains(prompt, "staging_url") {
		t.Error("prompt should include the question key")
	}
}

func TestBuildFinalPassPromptSkipsEmptyAnswers(t *testing.T) {
	draft := "project:\n  testCommand: pytest\n"
	questions := []setupQuestion{
		{Key: "staging_url", Question: "Staging URL?"},
		{Key: "token_field", Question: "Token field?"},
	}
	// Only one answer provided
	answers := map[string]string{"staging_url": "https://api.example.com"}
	prompt := buildFinalPassPrompt(draft, questions, answers)

	if !setupContains(prompt, "https://api.example.com") {
		t.Error("prompt should include provided answer")
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func setupContains(s, sub string) bool {
	return sub != "" && strings.Contains(s, sub)
}
