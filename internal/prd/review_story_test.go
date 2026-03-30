package prd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalPRD writes a prd.md with two pending stories to a temp dir and returns the path.
func minimalPRD(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prdDir := filepath.Join(dir, ".chief", "prds", "test")
	if err := os.MkdirAll(prdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(prdDir, "prd.md")
	content := `# PRD: TestProject

## User Stories

### US-001: First story
**Priority:** 1

- [ ] criterion one

### US-002: Second story
**Priority:** 2

- [ ] criterion two
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// startedPRD returns a prd.md where US-001 is already passing.
func startedPRD(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prdDir := filepath.Join(dir, ".chief", "prds", "test")
	if err := os.MkdirAll(prdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(prdDir, "prd.md")
	content := `# PRD: TestProject

## User Stories

### US-001: First story
**Priority:** 1
**Status:** done

- [x] criterion one

### US-002: Second story
**Priority:** 2

- [ ] criterion two
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- InjectReviewStory ---

func TestInjectReviewStory_InjectsOnce(t *testing.T) {
	path := minimalPRD(t)

	injected, err := InjectReviewStory(path, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !injected {
		t.Fatal("expected injected=true on first call")
	}

	// Verify RV-000 is present.
	p, err := LoadPRD(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range p.UserStories {
		if s.ID == reviewStoryID {
			found = true
		}
	}
	if !found {
		t.Errorf("RV-000 not found in PRD after injection")
	}
}

func TestInjectReviewStory_Idempotent(t *testing.T) {
	path := minimalPRD(t)

	if _, err := InjectReviewStory(path, ""); err != nil {
		t.Fatal(err)
	}
	injected, err := InjectReviewStory(path, "")
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if injected {
		t.Error("expected injected=false on second call (idempotent)")
	}
}

func TestInjectReviewStory_VoiceNoteIncluded(t *testing.T) {
	path := minimalPRD(t)

	_, err := InjectReviewStory(path, "focus on the auth flow")
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "focus on the auth flow") {
		t.Error("voice note not found in injected story")
	}
}

func TestInjectReviewStory_NoVoiceNote_NoSection(t *testing.T) {
	path := minimalPRD(t)

	_, err := InjectReviewStory(path, "")
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Additional focus from reviewer") {
		t.Error("voice section should be absent when no note provided")
	}
}

// --- InjectPreDevReviewStory ---

func TestInjectPreDevReviewStory_InjectsOnce(t *testing.T) {
	path := minimalPRD(t)

	injected, err := InjectPreDevReviewStory(path, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !injected {
		t.Fatal("expected injected=true on first call")
	}

	p, err := LoadPRD(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range p.UserStories {
		if s.ID == preDevReviewStoryID {
			found = true
			// PR-000 must have priority < 1 so it runs before dev stories.
			if s.Priority >= 1 {
				t.Errorf("PR-000 priority = %g, want < 1", s.Priority)
			}
		}
	}
	if !found {
		t.Error("PR-000 not found in PRD after injection")
	}
}

func TestInjectPreDevReviewStory_Idempotent(t *testing.T) {
	path := minimalPRD(t)

	if _, err := InjectPreDevReviewStory(path, ""); err != nil {
		t.Fatal(err)
	}
	injected, err := InjectPreDevReviewStory(path, "")
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if injected {
		t.Error("expected injected=false on second call (idempotent)")
	}
}

func TestInjectPreDevReviewStory_BlockedWhenDevStarted(t *testing.T) {
	path := startedPRD(t)

	injected, err := InjectPreDevReviewStory(path, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if injected {
		t.Error("expected injected=false when a story has already passed")
	}
}

func TestInjectPreDevReviewStory_VoiceNoteIncluded(t *testing.T) {
	path := minimalPRD(t)

	_, err := InjectPreDevReviewStory(path, "check for breaking changes in the API")
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "check for breaking changes in the API") {
		t.Error("voice note not found in injected pre-dev review story")
	}
}
