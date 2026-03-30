package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ─── sanitizeSlug ──────────────────────────────────────────────────────────

func TestSanitizeSlug(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"lowercase passthrough", "fix-auth-bug", "fix-auth-bug"},
		{"uppercase lowered", "Fix-Auth-Bug", "fix-auth-bug"},
		{"spaces to hyphens", "fix auth bug", "fix-auth-bug"},
		{"special chars removed", "fix@auth!bug", "fix-auth-bug"},
		{"multiple hyphens collapsed", "fix---auth---bug", "fix-auth-bug"},
		{"leading hyphens trimmed", "---fix-auth", "fix-auth"},
		{"trailing hyphens trimmed", "fix-auth---", "fix-auth"},
		{"dots removed", "fix.auth.bug", "fix-auth-bug"},
		{"slashes removed", "fix/auth/bug", "fix-auth-bug"},
		{"numbers kept", "fix-auth-v2", "fix-auth-v2"},
		{"all special chars", "!!!###", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeSlug(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeSlug(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ─── parseGenerateOutput ──────────────────────────────────────────────────

func TestParseGenerateOutput(t *testing.T) {
	validPRD := "# PRD: fix-auth\n\n## User Stories\n\n### US-001: Triage\n- [ ] Check logs\n"

	tests := []struct {
		name        string
		output      string
		wantSlug    string
		wantType    string
		wantContent string
		wantErr     bool
		errContains string
	}{
		{
			name: "valid output",
			output: "SLUG: fix-auth-bug\nTYPE: bug-fix\n---BEGIN PRD---\n" + validPRD + "\n---END PRD---\n",
			wantSlug:    "fix-auth-bug",
			wantType:    "bug-fix",
			wantContent: validPRD,
		},
		{
			name: "slug with uppercase gets lowered",
			output: "SLUG: Fix-Auth-Bug\nTYPE: bug-fix\n---BEGIN PRD---\n" + validPRD + "\n---END PRD---\n",
			wantSlug:    "fix-auth-bug",
			wantType:    "bug-fix",
			wantContent: validPRD,
		},
		{
			name: "extra text before slug is ignored",
			output: "Here is your PRD:\n\nSLUG: my-feature\nTYPE: feature\n---BEGIN PRD---\n" + validPRD + "\n---END PRD---\n",
			wantSlug:    "my-feature",
			wantType:    "feature",
			wantContent: validPRD,
		},
		{
			name:        "missing begin marker",
			output:      "SLUG: fix-auth\nTYPE: bug-fix\n" + validPRD + "\n---END PRD---\n",
			wantErr:     true,
			errContains: "---BEGIN PRD---",
		},
		{
			name:        "missing end marker",
			output:      "SLUG: fix-auth\nTYPE: bug-fix\n---BEGIN PRD---\n" + validPRD,
			wantErr:     true,
			errContains: "---END PRD---",
		},
		{
			name:        "missing slug line",
			output:      "TYPE: bug-fix\n---BEGIN PRD---\n" + validPRD + "\n---END PRD---\n",
			wantErr:     true,
			errContains: "SLUG",
		},
		{
			name:        "end marker before begin marker",
			output:      "SLUG: fix\nTYPE: bug-fix\n---END PRD---\n---BEGIN PRD---\n" + validPRD,
			wantErr:     true,
		},
		{
			name: "type field is optional (no error, empty type)",
			output: "SLUG: fix-auth\n---BEGIN PRD---\n" + validPRD + "\n---END PRD---\n",
			wantSlug:    "fix-auth",
			wantType:    "",
			wantContent: validPRD,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slug, content, prdType, err := parseGenerateOutput(tt.output)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil (slug=%q, type=%q)", slug, prdType)
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if slug != tt.wantSlug {
				t.Errorf("slug = %q, want %q", slug, tt.wantSlug)
			}
			if prdType != tt.wantType {
				t.Errorf("type = %q, want %q", prdType, tt.wantType)
			}
			if !strings.Contains(content, "US-001") {
				t.Errorf("content missing US-001 story, got: %q", content)
			}
		})
	}
}

// ─── readCLAUDEMd ─────────────────────────────────────────────────────────

func TestReadCLAUDEMd(t *testing.T) {
	t.Run("file exists returns content", func(t *testing.T) {
		dir := t.TempDir()
		content := "# Architecture Rules\n\nDo not break things.\n"
		if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		got := readCLAUDEMd(dir)
		if !strings.Contains(got, "Architecture Rules") {
			t.Errorf("expected content to contain 'Architecture Rules', got: %q", got)
		}
	})

	t.Run("file missing returns fallback message", func(t *testing.T) {
		dir := t.TempDir()
		got := readCLAUDEMd(dir)
		if !strings.Contains(got, "no CLAUDE.md") {
			t.Errorf("expected fallback message, got: %q", got)
		}
	})

	t.Run("file with more than 100 lines is truncated", func(t *testing.T) {
		dir := t.TempDir()
		var sb strings.Builder
		for i := 0; i < 200; i++ {
			sb.WriteString("line content here\n")
		}
		if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(sb.String()), 0644); err != nil {
			t.Fatal(err)
		}
		got := readCLAUDEMd(dir)
		lines := strings.Split(got, "\n")
		if len(lines) > 101 { // 100 lines + possible trailing newline
			t.Errorf("expected at most 100 lines, got %d", len(lines))
		}
	})
}

// ─── buildGeneratePrompt ──────────────────────────────────────────────────

func TestBuildGeneratePrompt(t *testing.T) {
	t.Run("contains description", func(t *testing.T) {
		prompt := buildGeneratePrompt("fix drug interaction", "", "https://staging.dev", "https://backlog.com", "865856", "pytest", "# Rules")
		if !strings.Contains(prompt, "fix drug interaction") {
			t.Error("prompt missing description")
		}
	})

	t.Run("contains staging URL", func(t *testing.T) {
		prompt := buildGeneratePrompt("fix x", "", "https://staging.example.com", "", "", "pytest", "")
		if !strings.Contains(prompt, "https://staging.example.com") {
			t.Error("prompt missing staging URL")
		}
	})

	t.Run("contains forced type when set", func(t *testing.T) {
		prompt := buildGeneratePrompt("fix x", "test-coverage", "https://s.dev", "", "", "pytest", "")
		if !strings.Contains(prompt, "test-coverage") {
			t.Error("prompt missing forced type")
		}
	})

	t.Run("no forced type section when empty", func(t *testing.T) {
		prompt := buildGeneratePrompt("fix x", "", "https://s.dev", "", "", "pytest", "")
		if strings.Contains(prompt, "Forced type:") {
			t.Error("prompt should not contain forced type section when empty")
		}
	})

	t.Run("contains architecture rules", func(t *testing.T) {
		prompt := buildGeneratePrompt("fix x", "", "", "", "", "pytest", "## My arch rule")
		if !strings.Contains(prompt, "My arch rule") {
			t.Error("prompt missing architecture rules")
		}
	})

	t.Run("contains all PRD type names", func(t *testing.T) {
		prompt := buildGeneratePrompt("fix x", "", "", "", "", "pytest", "")
		requiredTypes := []string{"bug-fix", "feature", "test-coverage", "robustness",
			"exploration", "server-debug", "refactor", "security", "perf", "migration", "review", "batch-fix"}
		for _, typ := range requiredTypes {
			if !strings.Contains(prompt, typ) {
				t.Errorf("prompt missing type %q", typ)
			}
		}
	})

	t.Run("contains architecture review requirements", func(t *testing.T) {
		prompt := buildGeneratePrompt("fix x", "", "", "", "", "pytest", "")
		archChecks := []string{
			"CLAUDE.md", "Layer separation", "API contract", "Caller scan", "chief-done",
			"Blast radius", "Atomicity test",
		}
		for _, check := range archChecks {
			if !strings.Contains(prompt, check) {
				t.Errorf("prompt missing architecture check: %q", check)
			}
		}
	})

	t.Run("contains openspec GWT acceptance criteria format", func(t *testing.T) {
		prompt := buildGeneratePrompt("fix x", "", "", "", "", "pytest", "")
		gwtChecks := []string{"GIVEN", "WHEN", "THEN", "Verify:", "The system SHALL"}
		for _, check := range gwtChecks {
			if !strings.Contains(prompt, check) {
				t.Errorf("prompt missing GWT/openspec element: %q", check)
			}
		}
	})

	t.Run("contains DRY TDD and architecture compliance rules", func(t *testing.T) {
		prompt := buildGeneratePrompt("fix x", "", "", "", "", "pytest", "")
		rules := []string{
			"DRY", "TDD",
			"write the failing test first",
			"Architecture compliance",
			"Design compliance",
			"no new abstractions",
			"reuse it; never duplicate",
		}
		for _, rule := range rules {
			if !strings.Contains(prompt, rule) {
				t.Errorf("prompt missing rule: %q", rule)
			}
		}
	})
}

// ─── extractTextFromStreamJSON ────────────────────────────────────────────

func TestExtractTextFromStreamJSON(t *testing.T) {
	t.Run("extracts from result message", func(t *testing.T) {
		raw := `{"type":"system","subtype":"init"}` + "\n" +
			`{"type":"assistant","message":{"content":[{"type":"text","text":"partial"}]}}` + "\n" +
			`{"type":"result","result":"SLUG: my-slug\nTYPE: bug-fix\n---BEGIN PRD---\n# PRD\n---END PRD---"}` + "\n"
		got := extractTextFromStreamJSON(raw)
		if !strings.Contains(got, "SLUG: my-slug") {
			t.Errorf("expected result message content, got: %q", got)
		}
	})

	t.Run("falls back to assistant content blocks when no result", func(t *testing.T) {
		raw := `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello "},{"type":"text","text":"world"}]}}` + "\n"
		got := extractTextFromStreamJSON(raw)
		if got != "Hello world" {
			t.Errorf("expected concatenated assistant text 'Hello world', got: %q", got)
		}
	})

	t.Run("ignores non-text content blocks", func(t *testing.T) {
		raw := `{"type":"assistant","message":{"content":[{"type":"tool_use","text":""},{"type":"text","text":"answer"}]}}` + "\n"
		got := extractTextFromStreamJSON(raw)
		if got != "answer" {
			t.Errorf("expected only text blocks, got: %q", got)
		}
	})

	t.Run("empty input returns empty string", func(t *testing.T) {
		got := extractTextFromStreamJSON("")
		if got != "" {
			t.Errorf("expected empty string, got: %q", got)
		}
	})

	t.Run("non-json lines are ignored", func(t *testing.T) {
		raw := "not json\n{\"type\":\"result\",\"result\":\"found it\"}\n"
		got := extractTextFromStreamJSON(raw)
		if got != "found it" {
			t.Errorf("expected 'found it', got: %q", got)
		}
	})

	t.Run("result message takes precedence over assistant blocks", func(t *testing.T) {
		raw := `{"type":"assistant","message":{"content":[{"type":"text","text":"partial answer"}]}}` + "\n" +
			`{"type":"result","result":"final full answer"}` + "\n"
		got := extractTextFromStreamJSON(raw)
		if got != "final full answer" {
			t.Errorf("expected result to take precedence, got: %q", got)
		}
	})
}

// ─── GenerateOptions.SkipResearch ────────────────────────────────────────

func TestGenerateOptionsSkipResearch(t *testing.T) {
	// Verify SkipResearch field exists and defaults to false (research always on)
	opts := GenerateOptions{}
	if opts.SkipResearch {
		t.Error("SkipResearch should default to false (auto-research is on by default)")
	}

	opts2 := GenerateOptions{SkipResearch: true}
	if !opts2.SkipResearch {
		t.Error("SkipResearch should be settable to true")
	}
}

// ─── RunGenerate (integration — requires claude CLI) ──────────────────────

func TestRunGenerateIntegration(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not found — skipping integration test")
	}

	dir := t.TempDir()

	// Write a minimal CLAUDE.md so the prompt has context
	claudeMd := "# Architecture\n\n- No breaking changes\n- Two-layer model\n"
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(claudeMd), 0644); err != nil {
		t.Fatal(err)
	}

	opts := GenerateOptions{
		Description: "write a hello world function and add a unit test for it",
		ForcedType:  "feature",
		BaseDir:     dir,
	}

	if err := RunGenerate(opts); err != nil {
		t.Fatalf("RunGenerate failed: %v", err)
	}

	// Verify .chief/prds/<slug>/prd.md was created
	prdsDir := filepath.Join(dir, ".chief", "prds")
	entries, err := os.ReadDir(prdsDir)
	if err != nil {
		t.Fatalf("prds directory not created: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no PRD directories created")
	}

	// Read the generated PRD
	prdPath := filepath.Join(prdsDir, entries[0].Name(), "prd.md")
	data, err := os.ReadFile(prdPath)
	if err != nil {
		t.Fatalf("prd.md not found: %v", err)
	}
	content := string(data)

	// Verify required structure
	requiredSections := []string{"# PRD:", "## User Stories", "US-001", "Acceptance Criteria"}
	for _, section := range requiredSections {
		if !strings.Contains(content, section) {
			t.Errorf("prd.md missing required section %q\ncontent:\n%s", section, content)
		}
	}

	// Verify architecture review story is present
	if !strings.Contains(content, "Architecture") {
		t.Errorf("prd.md should contain an architecture review story")
	}
}

// ─── RunGenerateWithResearch (integration — requires claude CLI) ───────────

func TestRunGenerateWithResearchIntegration(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not found — skipping integration test")
	}

	dir := t.TempDir()

	// Write some source files for the research pass to find
	srcDir := filepath.Join(dir, "src")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "auth.py"), []byte("def login(user, password):\n    pass\n"), 0644)
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# Rules\n- No breaking changes\n"), 0644)

	opts := GenerateOptions{
		Description: "the login function returns None instead of raising an exception on bad password",
		ForcedType:  "bug-fix",
		BaseDir:     dir,
	}

	if err := RunGenerateWithResearch(opts); err != nil {
		t.Fatalf("RunGenerateWithResearch failed: %v", err)
	}

	// Research report should be written
	chiefDir := filepath.Join(dir, ".chief")
	entries, err := os.ReadDir(chiefDir)
	if err != nil {
		t.Fatalf(".chief dir not created: %v", err)
	}
	hasResearchReport := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "research-") {
			hasResearchReport = true
			break
		}
	}
	if !hasResearchReport {
		t.Error("expected research-*.md report in .chief/")
	}

	// PRD should be in .chief/prds/
	prdsDir := filepath.Join(chiefDir, "prds")
	prdsEntries, err := os.ReadDir(prdsDir)
	if err != nil || len(prdsEntries) == 0 {
		t.Fatal("no PRD created after RunGenerateWithResearch")
	}
}
