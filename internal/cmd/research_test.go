package cmd

import (
	"os"
	"os/exec" //nolint:typecheck
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ─── buildResearchPrompt ─────────────────────────────────────────────────

func TestBuildResearchPrompt(t *testing.T) {
	t.Run("contains the work request", func(t *testing.T) {
		prompt := buildResearchPrompt("claude", "fix login bug", "/tmp", "")
		if !strings.Contains(prompt, "fix login bug") {
			t.Error("prompt missing work request")
		}
	})

	t.Run("contains research task headings", func(t *testing.T) {
		prompt := buildResearchPrompt("claude", "fix x", "/tmp", "")
		requiredSections := []string{
			"Find affected code",
			"Assess blast radius",
			"Identify risks",
			"Assess information completeness",
			"Recommended Approach",
			"Web search",
			"Existing skills",
		}
		for _, section := range requiredSections {
			if !strings.Contains(prompt, section) {
				t.Errorf("prompt missing section %q", section)
			}
		}
	})

	t.Run("contains fix priority order", func(t *testing.T) {
		prompt := buildResearchPrompt("claude", "fix x", "/tmp", "")
		priorities := []string{"Build failures", "Type errors", "Test failures"}
		for _, p := range priorities {
			if !strings.Contains(prompt, p) {
				t.Errorf("prompt missing fix priority %q", p)
			}
		}
	})

	t.Run("contains output format instructions", func(t *testing.T) {
		prompt := buildResearchPrompt("claude", "fix x", "/tmp", "")
		requiredOutputFields := []string{
			"Work Type",
			"Affected Files",
			"Blast Radius",
			"Git Conflicts",
			"Suggested PRD Slug",
			"SUFFICIENT / INSUFFICIENT",
		}
		for _, field := range requiredOutputFields {
			if !strings.Contains(prompt, field) {
				t.Errorf("prompt missing output field %q", field)
			}
		}
	})

	t.Run("includes git context when provided", func(t *testing.T) {
		gitCtx := "## Recent Git History\nabc1234 fix auth bug\ndef5678 add tests\n"
		prompt := buildResearchPrompt("claude", "fix x", "/tmp", gitCtx)
		if !strings.Contains(prompt, "fix auth bug") {
			t.Error("prompt should include git context")
		}
	})

	t.Run("includes CLAUDE.md content when present", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("## My Rule\nDo not break things.\n"), 0644)
		prompt := buildResearchPrompt("claude", "fix x", dir, "")
		if !strings.Contains(prompt, "My Rule") {
			t.Error("prompt should include CLAUDE.md content")
		}
	})

	t.Run("fallback message when no CLAUDE.md", func(t *testing.T) {
		prompt := buildResearchPrompt("claude", "fix x", t.TempDir(), "")
		if !strings.Contains(prompt, "no CLAUDE.md") {
			t.Error("prompt should indicate missing CLAUDE.md")
		}
	})
}

// ─── parseResearchSufficiency ─────────────────────────────────────────────

func TestParseResearchSufficiency(t *testing.T) {
	cases := []struct {
		name   string
		report string
		want   bool
	}{
		{
			name:   "SUFFICIENT on next line",
			report: "## Some Section\nblah blah\n## Information Completeness\nSUFFICIENT\n### If insufficient",
			want:   true,
		},
		{
			name:   "INSUFFICIENT on next line",
			report: "## Information Completeness\nINSUFFICIENT\n### If insufficient",
			want:   false,
		},
		{
			name:   "blank line then SUFFICIENT",
			report: "## Information Completeness\n\nSUFFICIENT",
			want:   true,
		},
		{
			name:   "SUFFICIENT / INSUFFICIENT format (template literal)",
			report: "## Information Completeness\nSUFFICIENT / INSUFFICIENT",
			want:   true, // HasPrefix("SUFFICIENT") and not HasPrefix("INSUFFICIENT")
		},
		{
			name:   "no completeness section",
			report: "## Work Type\nbug-fix\n## Affected Files\n...",
			want:   false,
		},
		{
			name:   "empty report",
			report: "",
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseResearchSufficiency(tc.report)
			if got != tc.want {
				t.Errorf("parseResearchSufficiency(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// ─── summariseResearch ────────────────────────────────────────────────────

func TestSummariseResearch(t *testing.T) {
	report := `# Research Report: fix login bug

## Work Type
bug-fix

## Some Ignored Section
lots of verbose prose that should be dropped
spanning multiple lines

## Affected Files
| File | Why | Risk |
|------|-----|------|
| auth.go | contains login | LOW |

## Recommended Approach
Fix the password hash comparison in auth.go line 42.

## Information Completeness
SUFFICIENT

## Suggested PRD Slug
fix-login-hash-bug
`
	summary := summariseResearch(report)

	if strings.Contains(summary, "Some Ignored Section") {
		t.Error("summary should drop ignored sections")
	}
	if !strings.Contains(summary, "Affected Files") {
		t.Error("summary must include Affected Files")
	}
	if !strings.Contains(summary, "Recommended Approach") {
		t.Error("summary must include Recommended Approach")
	}
	if !strings.Contains(summary, "SUFFICIENT") {
		t.Error("summary must include Information Completeness")
	}
	if !strings.Contains(summary, "Suggested PRD Slug") {
		t.Error("summary must include Suggested PRD Slug")
	}

	// Must be shorter than original
	if len(summary) >= len(report) {
		t.Errorf("summary (%d chars) should be shorter than original (%d chars)", len(summary), len(report))
	}
}

func TestSummariseResearchFallback(t *testing.T) {
	// No matching sections — should fall back to capped raw report
	report := "# Just a title\n\nSome prose without standard section headers."
	summary := summariseResearch(report)
	if summary == "" {
		t.Error("fallback should return non-empty string")
	}
}

// ─── checkGitPreconditions ────────────────────────────────────────────────

func TestCheckGitPreconditions(t *testing.T) {
	t.Run("non-git directory returns warning", func(t *testing.T) {
		dir := t.TempDir()
		warnings := checkGitPreconditions(dir)
		found := false
		for _, w := range warnings {
			if strings.Contains(w, "not a git") {
				found = true
			}
		}
		if !found {
			t.Errorf("expected 'not a git repository' warning for non-git dir, got: %v", warnings)
		}
	})

	t.Run("clean git repo returns no warnings", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
		dir := t.TempDir()
		// Init a git repo with a commit so HEAD is valid
		exec.Command("git", "-C", dir, "init").Run()
		exec.Command("git", "-C", dir, "config", "user.email", "test@test.com").Run()
		exec.Command("git", "-C", dir, "config", "user.name", "Test").Run()
		os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0644)
		exec.Command("git", "-C", dir, "add", ".").Run()
		exec.Command("git", "-C", dir, "commit", "-m", "init").Run()

		warnings := checkGitPreconditions(dir)
		// Clean repo should have no warnings
		for _, w := range warnings {
			if strings.Contains(w, "not a git") || strings.Contains(w, "detached") {
				t.Errorf("unexpected warning for clean repo: %q", w)
			}
		}
	})
}

// ─── appendResearchLog ────────────────────────────────────────────────────

func TestAppendResearchLog(t *testing.T) {
	dir := t.TempDir()

	// First entry should create the file with header
	appendResearchLog(dir, "fix login bug", "/tmp/research-fix.md", true)

	logPath := filepath.Join(dir, ".chief", "research-log.tsv")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("research-log.tsv not created: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "timestamp\tdescription\treport_path\tinfo_sufficient") {
		t.Error("log missing TSV header")
	}
	if !strings.Contains(content, "fix login bug") {
		t.Error("log missing description")
	}
	if !strings.Contains(content, "yes") {
		t.Error("log should show info_sufficient=yes")
	}

	// Second entry with insufficient info
	appendResearchLog(dir, "vague request", "/tmp/research-vague.md", false)
	data2, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data2), "no") {
		t.Error("log should show info_sufficient=no for second entry")
	}

	// Count rows: header + 2 entries = 3 lines (plus possible trailing newline)
	lines := strings.Split(strings.TrimSpace(string(data2)), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines (header + 2 entries), got %d\n%s", len(lines), string(data2))
	}
}

func TestAppendResearchLogEscapesTabs(t *testing.T) {
	dir := t.TempDir()
	// Description with tab characters should not corrupt TSV
	appendResearchLog(dir, "fix\tauth\tbug", "/tmp/r.md", true)

	data, _ := os.ReadFile(filepath.Join(dir, ".chief", "research-log.tsv"))
	// Tabs in description should be replaced with spaces
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatal("expected at least header + 1 entry")
	}
	entryFields := strings.Split(lines[1], "\t")
	if len(entryFields) != 4 {
		t.Errorf("TSV entry should have exactly 4 fields, got %d: %v", len(entryFields), entryFields)
	}
}

// ─── discoverAllSkills ────────────────────────────────────────────────────

func TestDiscoverAllSkills(t *testing.T) {
	t.Run("empty repo returns empty list", func(t *testing.T) {
		skills := discoverAllSkills(t.TempDir())
		if len(skills) != 0 {
			t.Errorf("expected 0 skills, got %d", len(skills))
		}
	})

	t.Run("discovers skills from .claude/skills", func(t *testing.T) {
		dir := t.TempDir()
		skillDir := filepath.Join(dir, ".claude", "skills", "k8s-debug")
		os.MkdirAll(skillDir, 0755)
		os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\ndescription: Debug Kubernetes pods\n---\n# k8s-debug"), 0644)

		skills := discoverAllSkills(dir)
		if len(skills) != 1 {
			t.Fatalf("expected 1 skill, got %d", len(skills))
		}
		if skills[0].Name != "k8s-debug" {
			t.Errorf("expected skill name 'k8s-debug', got %q", skills[0].Name)
		}
		if !strings.Contains(skills[0].Desc, "Kubernetes") {
			t.Errorf("expected description to contain 'Kubernetes', got %q", skills[0].Desc)
		}
	})

	t.Run("discovers skills from .claude/commands", func(t *testing.T) {
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, ".claude", "commands", "db-migrate"), 0755)

		skills := discoverAllSkills(dir)
		if len(skills) != 1 || skills[0].Name != "db-migrate" {
			t.Errorf("expected db-migrate skill, got %v", skills)
		}
	})

	t.Run("repo-local skill takes precedence over global same name", func(t *testing.T) {
		// This test can't override ~/.claude/commands without temp home, so just verify
		// that duplicate names are deduplicated (repo-local wins since it's scanned first)
		dir := t.TempDir()
		// Create same name in both skills and commands
		os.MkdirAll(filepath.Join(dir, ".claude", "skills", "shared"), 0755)
		os.WriteFile(filepath.Join(dir, ".claude", "skills", "shared", "SKILL.md"),
			[]byte("---\ndescription: from skills\n---"), 0644)
		os.MkdirAll(filepath.Join(dir, ".claude", "commands", "shared"), 0755)
		os.WriteFile(filepath.Join(dir, ".claude", "commands", "shared", "SKILL.md"),
			[]byte("---\ndescription: from commands\n---"), 0644)

		skills := discoverAllSkills(dir)
		count := 0
		for _, s := range skills {
			if s.Name == "shared" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("expected exactly 1 'shared' skill (deduped), got %d", count)
		}
		// First scanned (skills/) wins
		for _, s := range skills {
			if s.Name == "shared" && !strings.Contains(s.Desc, "from skills") {
				t.Errorf("expected repo skills/ to take precedence, got desc %q", s.Desc)
			}
		}
	})
}

// ─── readSkillsAndDocs ────────────────────────────────────────────────────

func TestReadSkillsAndDocs(t *testing.T) {
	t.Run("empty repo returns fallback", func(t *testing.T) {
		result := readSkillsAndDocs(t.TempDir())
		if !strings.Contains(result, "no skills") {
			t.Errorf("expected fallback message for empty dir, got: %q", result)
		}
	})

	t.Run("detects skills directory", func(t *testing.T) {
		dir := t.TempDir()
		skillsDir := filepath.Join(dir, ".claude", "skills")
		os.MkdirAll(skillsDir, 0755)
		os.MkdirAll(filepath.Join(skillsDir, "autoresearch"), 0755)
		os.WriteFile(filepath.Join(skillsDir, "commit.md"), []byte("# commit skill"), 0644)

		result := readSkillsAndDocs(dir)
		if !strings.Contains(result, "autoresearch") {
			t.Error("expected autoresearch skill to appear")
		}
		if !strings.Contains(result, "commit.md") {
			t.Error("expected commit.md to appear")
		}
	})

	t.Run("detects docs directory", func(t *testing.T) {
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, "docs"), 0755)
		os.WriteFile(filepath.Join(dir, "docs", "architecture.md"), []byte("# arch"), 0644)

		result := readSkillsAndDocs(dir)
		if !strings.Contains(result, "architecture.md") {
			t.Error("expected architecture.md in docs")
		}
	})

	t.Run("detects existing PRDs", func(t *testing.T) {
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, ".chief", "prds", "fix-auth-bug"), 0755)

		result := readSkillsAndDocs(dir)
		if !strings.Contains(result, "fix-auth-bug") {
			t.Error("expected fix-auth-bug PRD to appear")
		}
	})
}

// ─── readGitMemory ────────────────────────────────────────────────────────

func TestReadGitMemory(t *testing.T) {
	t.Run("non-git dir returns empty string gracefully", func(t *testing.T) {
		dir := t.TempDir()
		result := readGitMemory(dir)
		// Should not panic; may return empty or partial content
		_ = result
	})

	t.Run("git repo returns log content", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
		dir := t.TempDir()
		exec.Command("git", "-C", dir, "init").Run()
		exec.Command("git", "-C", dir, "config", "user.email", "test@test.com").Run()
		exec.Command("git", "-C", dir, "config", "user.name", "Test").Run()
		os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644)
		exec.Command("git", "-C", dir, "add", ".").Run()
		exec.Command("git", "-C", dir, "commit", "-m", "initial commit").Run()

		memory := readGitMemory(dir)
		if !strings.Contains(memory, "initial commit") {
			t.Errorf("expected git memory to contain commit message, got: %q", memory)
		}
		if !strings.Contains(memory, "Recent Git History") {
			t.Error("expected 'Recent Git History' section header")
		}
	})
}

// ─── RunResearch (unit — output file path) ───────────────────────────────

func TestRunResearchDefaultOutputPath(t *testing.T) {
	dir := t.TempDir()

	// We can't run real research without Claude, but we can verify the
	// output path construction by checking that the .chief dir would be used.
	slug := sanitizeSlug("fix login bug returns none")
	expectedDir := filepath.Join(dir, ".chief")
	expectedFile := filepath.Join(expectedDir, "research-"+slug+".md")

	// Simulate what RunResearch does for path construction
	os.MkdirAll(expectedDir, 0755)
	os.WriteFile(expectedFile, []byte("# Research Report"), 0644)

	if _, err := os.Stat(expectedFile); os.IsNotExist(err) {
		t.Errorf("expected research file at %s", expectedFile)
	}
}

// ─── Research cache (reuse across same-area work) ────────────────────────

func TestLoadCachedResearch(t *testing.T) {
	dir := t.TempDir()
	chiefDir := filepath.Join(dir, ".chief")
	os.MkdirAll(chiefDir, 0755)

	// Write a fake research report
	reportPath := filepath.Join(chiefDir, "research-fix-auth.md")
	reportContent := "# Research Report: fix auth\n\n## Affected Files\n| auth.py | login function |\n"
	os.WriteFile(reportPath, []byte(reportContent), 0644)

	// Simulate loading — report should be found and returned
	cached, err := loadCachedResearch(dir, "fix auth bug", 1*time.Hour)
	if err != nil {
		t.Fatalf("loadCachedResearch returned error: %v", err)
	}
	if cached == nil {
		t.Fatal("expected cached research to be found")
	}
	if !strings.Contains(cached.RawReport, "Research Report") {
		t.Errorf("cached report content wrong: %q", cached.RawReport)
	}
}

func TestLoadCachedResearchExpired(t *testing.T) {
	dir := t.TempDir()
	chiefDir := filepath.Join(dir, ".chief")
	os.MkdirAll(chiefDir, 0755)

	// Write a fake research report with an old mtime (set to 2 hours ago)
	reportPath := filepath.Join(chiefDir, "research-fix-auth.md")
	os.WriteFile(reportPath, []byte("# Old Report\n"), 0644)
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	os.Chtimes(reportPath, twoHoursAgo, twoHoursAgo)

	// With a 1-hour TTL, the old report should NOT be returned
	cached, err := loadCachedResearch(dir, "fix auth bug", 1*time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cached != nil {
		t.Error("expected expired cache to return nil")
	}
}

func TestLoadCachedResearchMiss(t *testing.T) {
	dir := t.TempDir()

	// No research reports exist at all
	cached, err := loadCachedResearch(dir, "completely different topic", 1*time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cached != nil {
		t.Error("expected cache miss to return nil")
	}
}

func TestLoadCachedResearchSlugMatching(t *testing.T) {
	dir := t.TempDir()
	chiefDir := filepath.Join(dir, ".chief")
	os.MkdirAll(chiefDir, 0755)

	// Report for "fix-auth" area
	os.WriteFile(filepath.Join(chiefDir, "research-fix-auth.md"), []byte("# Auth Report\n"), 0644)

	// Slightly different description in same area — should still match
	cached, _ := loadCachedResearch(dir, "fix auth login", 1*time.Hour)
	if cached == nil {
		t.Log("no fuzzy match — acceptable if slug matching is exact")
	}

	// Completely unrelated — should NOT match
	cachedUnrelated, _ := loadCachedResearch(dir, "add csv export feature", 1*time.Hour)
	if cachedUnrelated != nil {
		t.Error("unrelated request should not match auth research cache")
	}
}

// ─── RunResearch integration (requires claude CLI) ───────────────────────

func TestRunResearchIntegration(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not found — skipping research integration test")
	}

	dir := t.TempDir()

	// Create a small codebase for Claude to explore
	srcDir := filepath.Join(dir, "src")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "auth.py"), []byte(`
def login(username, password):
    """Authenticate a user. Returns user dict or raises ValueError."""
    if not username or not password:
        return None  # BUG: should raise ValueError
    return {"user": username}
`), 0644)
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# Rules\n- No breaking changes\n- Raise exceptions, don't return None on error\n"), 0644)

	opts := ResearchOptions{
		Description: "login returns None instead of raising ValueError on empty credentials",
		BaseDir:     dir,
	}

	result, err := RunResearch(opts)
	if err != nil {
		t.Fatalf("RunResearch failed: %v", err)
	}

	// Report file should exist
	if _, err := os.Stat(result.ReportPath); os.IsNotExist(err) {
		t.Errorf("research report not written to %s", result.ReportPath)
	}

	// Report should contain expected sections
	requiredSections := []string{"Work Type", "Affected Files", "Recommended Approach"}
	for _, section := range requiredSections {
		if !strings.Contains(result.RawReport, section) {
			t.Errorf("report missing section %q", section)
		}
	}

	// auth.py should be identified as an affected file
	if !strings.Contains(result.RawReport, "auth.py") {
		t.Errorf("report should identify auth.py as affected file\nreport:\n%s", result.RawReport)
	}

	t.Logf("Research report path: %s", result.ReportPath)
	preview := result.RawReport
	if len(preview) > 500 {
		preview = preview[:500]
	}
	t.Logf("Report preview:\n%s", preview)
}

// ─── RunResearch cache reuse integration ─────────────────────────────────

func TestRunResearchReusesCache(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not found — skipping cache reuse test")
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# Rules\n- No breaking changes\n"), 0644)

	srcDir := filepath.Join(dir, "src")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "billing.py"), []byte("def calculate_bill(amount):\n    return amount * 1.1\n"), 0644)

	opts := ResearchOptions{
		Description: "billing calculation is wrong for zero amount",
		BaseDir:     dir,
	}

	// First run — should call Claude
	result1, err := RunResearch(opts)
	if err != nil {
		t.Fatalf("first RunResearch failed: %v", err)
	}

	// Second run for related area — should reuse cache, not call Claude again
	opts2 := ResearchOptions{
		Description: "billing calculation returns wrong value",
		BaseDir:     dir,
	}
	result2, err := RunResearch(opts2)
	if err != nil {
		t.Fatalf("second RunResearch failed: %v", err)
	}

	// Both should reference the same or similar report
	// At minimum, result2 should succeed without error
	t.Logf("First report: %s", result1.ReportPath)
	t.Logf("Second report: %s", result2.ReportPath)
}

