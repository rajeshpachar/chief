package prd

import (
	"fmt"
	"os"
	"strings"
)

const reviewStoryID = "RV-000"
const preDevReviewStoryID = "PR-000"

// InjectReviewStory appends a post-implementation review story to the PRD file.
// The story tells Claude to audit every acceptance criterion, check the git diff,
// and either finish clean or add RV-001, RV-002… fix stories directly to prd.md.
// voiceNote is optional additional context from the user (voice input); pass "" to omit.
//
// Returns false (no-op) if a review story already exists.
func InjectReviewStory(prdPath, voiceNote string) (bool, error) {
	p, err := LoadPRD(prdPath)
	if err != nil {
		return false, fmt.Errorf("load PRD: %w", err)
	}

	if hasReviewStory(p) {
		return false, nil
	}

	// Determine next priority: just after the last story.
	nextPri := len(p.UserStories) + 1

	story := buildReviewStoryMarkdown(p, nextPri, voiceNote)

	f, err := os.OpenFile(prdPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return false, fmt.Errorf("open PRD: %w", err)
	}
	_, writeErr := f.WriteString(story)
	closeErr := f.Close()
	if writeErr != nil {
		return false, fmt.Errorf("write review story: %w", writeErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close PRD: %w", closeErr)
	}
	return true, nil
}

// InjectPreDevReviewStory inserts a pre-development architecture review story (PR-000)
// into the PRD with priority 0.5 so it runs before any dev story.
// The story tells Claude to review the PRD against the project architecture and either
// approve it or revise it in-place before development begins.
// voiceNote is optional additional context from the user (voice input); pass "" to omit.
//
// Returns false (no-op) if a pre-dev review story already exists, or if any story
// has already been started (InProgress or Passes).
func InjectPreDevReviewStory(prdPath, voiceNote string) (bool, error) {
	p, err := LoadPRD(prdPath)
	if err != nil {
		return false, fmt.Errorf("load PRD: %w", err)
	}

	if hasPreDevReviewStory(p) {
		return false, nil
	}

	// Don't inject if dev has already started.
	for _, s := range p.UserStories {
		if s.InProgress || s.Passes {
			return false, nil
		}
	}

	story := buildPreDevReviewStoryMarkdown(p, voiceNote)

	f, err := os.OpenFile(prdPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return false, fmt.Errorf("open PRD: %w", err)
	}
	_, writeErr := f.WriteString(story)
	closeErr := f.Close()
	if writeErr != nil {
		return false, fmt.Errorf("write pre-dev review story: %w", writeErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close PRD: %w", closeErr)
	}
	return true, nil
}

// hasReviewStory returns true if the PRD already has any RV- story.
func hasReviewStory(p *PRD) bool {
	for _, s := range p.UserStories {
		if strings.HasPrefix(s.ID, "RV-") {
			return true
		}
	}
	return false
}

// hasPreDevReviewStory returns true if the PRD already has a PR- story.
func hasPreDevReviewStory(p *PRD) bool {
	for _, s := range p.UserStories {
		if strings.HasPrefix(s.ID, "PR-") {
			return true
		}
	}
	return false
}

// buildReviewStoryMarkdown returns the markdown block for the RV-000 story.
func buildReviewStoryMarkdown(p *PRD, priority int, voiceNote string) string {
	// Build a compact list of story IDs for reference.
	var ids []string
	for _, s := range p.UserStories {
		ids = append(ids, s.ID)
	}
	storyList := strings.Join(ids, ", ")

	voiceSection := ""
	if strings.TrimSpace(voiceNote) != "" {
		voiceSection = fmt.Sprintf(`
**Additional focus from reviewer:**
%s

`, strings.TrimSpace(voiceNote))
	}

	return fmt.Sprintf(`
### %s: Post-Implementation Review
**Priority:** %d
%s
Audit the implementation of this entire PRD and verify that every acceptance
criterion has been met. Work through the following steps:

STEP 1 — Collect context:
- Run: git diff main...HEAD (or git diff master...HEAD if main doesn't exist)
  to see all changes made during this PRD.
- Re-read this prd.md file to review every story (%s) and its acceptance criteria.

STEP 2 — Audit each story:
For every story, verify in the git diff that:
- The acceptance criterion is actually implemented (not just mentioned in a comment).
- Edge cases are handled (nulls, empty inputs, auth failures, race conditions).
- No obvious regressions: existing behaviour that the story didn't intend to change
  should still work.
- Existing functionality unrelated to this PRD is intact (no accidental breakage).

STEP 3 — Decide:
If every acceptance criterion is fully satisfied:
- Commit a review report: git commit --allow-empty -m "review: all criteria verified for %s"
- Output <chief-done/>

If gaps or issues are found:
- Edit THIS prd.md file (not a new file) and append fix stories immediately
  after this block. Use IDs RV-001, RV-002, etc. Each story needs:
    ### RV-001: <short title>
    **Priority:** 1
    <one paragraph describing exactly what is missing and how to fix it>
    - [ ] <specific, testable acceptance criterion>
  Keep it tight — max 5 fix stories, group related issues.
- Commit the updated prd.md: git commit -m "review: found N gaps, added fix stories"
- Output <chief-done/>

- [ ] Git diff reviewed against every acceptance criterion
- [ ] Existing functionality verified intact
- [ ] Either all criteria verified, or fix stories added to prd.md
`, reviewStoryID, priority, voiceSection, storyList, p.Project)
}

// buildPreDevReviewStoryMarkdown returns the markdown block for the PR-000 story.
// Priority 0.5 ensures it runs before any story with priority >= 1.
func buildPreDevReviewStoryMarkdown(p *PRD, voiceNote string) string {
	var ids []string
	for _, s := range p.UserStories {
		ids = append(ids, s.ID)
	}
	storyList := strings.Join(ids, ", ")

	voiceSection := ""
	if strings.TrimSpace(voiceNote) != "" {
		voiceSection = fmt.Sprintf(`
**Additional focus from reviewer:**
%s

`, strings.TrimSpace(voiceNote))
	}

	return fmt.Sprintf(`
### %s: Pre-Development Architecture Review
**Priority:** 0.5
%s
Before any implementation begins, review this PRD against the project's existing
architecture, patterns, and conventions. Stories to review: %s

STEP 1 — Understand the project context:
- Read CLAUDE.md (in this directory and ~/.claude/) for architecture guidelines,
  patterns, naming conventions, and constraints.
- Run: git log --oneline -20 to understand the development rhythm and recent changes.
- Skim key source files/directories to identify existing patterns (error handling,
  naming, package structure, API conventions).

STEP 2 — Review each planned story against:
- Breaking changes: does it modify public interfaces, APIs, or shared behaviour that
  other code depends on? If so, the story must explicitly handle backwards compatibility.
- Architecture alignment: does the approach fit existing patterns, or does it introduce
  inconsistency that will create tech debt?
- Acceptance criteria quality: are criteria specific and testable? Rewrite any vague
  criteria ("should work", "handle errors") to be unambiguous and verifiable.
- Story dependencies: are there ordering constraints between stories that the current
  priority order doesn't respect? Fix Priority values if so.
- Missing edge cases: are null inputs, auth failures, concurrency, and error paths covered
  in the acceptance criteria?

STEP 3 — Decide:
If the PRD is well-formed and implementation-ready:
- Commit: git commit --allow-empty -m "review: PRD approved for %s"
- Output <chief-done/>

If issues are found:
- Edit THIS prd.md file directly (not a new file):
  - Rewrite vague acceptance criteria in place to be specific and testable.
  - Adjust Priority values if story ordering needs to change.
  - Add missing edge-case acceptance criteria to existing stories.
  - Only add new stories (PR-001, PR-002...) if a critical requirement is entirely
    absent from the PRD and cannot fit in an existing story.
- Commit: git commit -m "review: PRD revised before dev — N issues corrected"
- Output <chief-done/>

- [ ] Project architecture and patterns reviewed
- [ ] Each story checked for breaking changes and alignment
- [ ] All acceptance criteria are specific and testable
- [ ] PRD approved or revised
`, preDevReviewStoryID, voiceSection, storyList, p.Project)
}
