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
Audit the full implementation of this PRD. Work through every step in order — do not skip any.

STEP 1 — Understand the project:
- Read README.md (repo root) for project overview and domain context.
- Read CLAUDE.md (repo root, then ~/.claude/CLAUDE.md if it exists) for architecture rules,
  naming conventions, forbidden patterns, and module-specific constraints.
- Run: git log --oneline -20 to understand recent development rhythm and commit conventions.

STEP 2 — Determine exactly what changed:
Do NOT assume the diff base — determine it:
  git branch --show-current
  git merge-base HEAD origin/main 2>/dev/null \
    || git merge-base HEAD origin/dev 2>/dev/null \
    || git merge-base HEAD origin/master 2>/dev/null \
    || git rev-list --max-parents=0 HEAD
  git diff <fork-point>...HEAD --stat    # which files changed
  git diff <fork-point>...HEAD           # full diff
  git log <fork-point>..HEAD --oneline   # commit list for this PRD

If on primary branch with no fork point: git diff HEAD~<N>..HEAD where N = commits in PRD.

STEP 3 — Audit each story (%s) against the diff:
For EVERY story and EVERY acceptance criterion — check all of these:

a) Real implementation vs stub: Is the criterion in actual running code, or is it a stub?
   Search new code for: "pass", "TODO", "FIXME", "raise NotImplementedError",
   "return None", "return []", "return {}", "not implemented", placeholder strings.
   A criterion is NOT met if its implementation is a stub or a comment.

b) Copy-paste context leak: Does new code reference variable names, table names, IDs,
   tenant values, or file paths that belong to a different module and were blindly copied?
   Read new code in context — a value that makes no sense here came from somewhere else.

c) Edge cases and input validation: Are nulls, empty inputs, missing config keys, out-of-range
   values, and auth failures handled? For API endpoints: inputs must be validated before use,
   not used optimistically and hoped-for.

d) Error handling completeness: All errors wrapped with context and propagated.
   No silent swallows: "except Exception: pass", "if err != nil { return }", bare "_ = err".
   Errors must be logged or returned — not discarded.

e) Async correctness (if project uses async): All awaitable calls are awaited.
   No sync blocking calls (DB queries, file I/O, HTTP) in async context without executor.
   No asyncio.run() / loop.run_until_complete() inside a running event loop.

f) Partial failure rollback: Multi-step operations writing to DB, files, or external services —
   does a failure at step 2 leave step 1's writes in a dirty state?
   There must be a transaction, rollback path, or compensating cleanup.

g) Test validity — read the tests, not just their names:
   Flag as invalid if any of these are true:
   - The test mocks the function it is supposed to test.
   - The assertion is trivially true (assert True, assert result == mock.return_value).
   - Deleting the implementation would still leave the test green.
   - The test mutates global state (DB rows, class vars, files) with no teardown,
     so it would corrupt the next test in the suite.

STEP 4 — Related files: compatibility and completeness:
For each changed public symbol (function, class, constant, route, schema field):
  grep -r "<symbol>" --include="*.py" --include="*.go" --include="*.ts" \
    --include="*.tsx" --include="*.js" --include="*.rb" --include="*.rs" . \
    | grep -v "test_\|_test\."
Read the top callers and check:
- Signature changes: are ALL call sites updated to match new parameters?
- Renames: is the old name fully gone, or do some callers still use it?
- Schema / model changes: is there a DB migration? Are serializers updated?
- New required config keys: do they have safe defaults so existing deployments don't break?
Flag incomplete refactors (definition updated, callers not) explicitly.

STEP 5 — Code quality (read new code directly, not just the diff):
a) Hardcoding and config drift: grep for hardcoded IDs, tenant names, ports, URLs, API keys,
   magic numbers. Also check: does an existing constant already define this value?
   The same literal in 2+ places is config drift — one must reference the other.

b) Duplication: search for functions with similar names or logic before concluding the new
   code is the only implementation. AI agents frequently write a new helper that duplicates
   one already in a sibling file.

c) Wrong layer: business logic in a route handler, SQL in a model, HTTP calls in a data layer?
   Check against layer boundaries in CLAUDE.md.

d) Architecture violations: check each new file and function against the specific rules in
   CLAUDE.md (data separation, sharding, transaction versioning, DRY, etc.).

e) Dead code: commented-out blocks, unused imports, functions added but never called,
   config keys written but never read.

f) Fabricated symbols: for every import and external function call in new code, verify the
   symbol actually exists at that path in the current codebase. AI agents import functions
   that don't exist yet, were renamed, or live in a different module.

g) End-to-end wiring: new code must be reachable from an entry point — it is not enough
   that the function exists. Verify:
   - New API routes are registered in the router/app factory (not just defined in isolation).
   - New pipeline steps/executors are registered in the step-type dispatch map.
   - New UI pages/components are imported and mounted in the layout or routing config.
   - New background tasks/workers are started in the service entry point.
   - New config keys are actually read by runtime code (not just written).
   Trace the call chain from entry point → new code. If any link is missing, the feature
   is dead on arrival even though the implementation looks complete.

STEP 6 — Frontend/backend API contract (only if the PRD touches both layers):
For every new or changed API endpoint:
a) Request shape: grep the frontend call site and the backend route handler side by side.
   - HTTP method matches (GET vs POST vs PATCH).
   - URL path and path parameters match exactly (including trailing slashes).
   - Query parameter names match (frontend: ?foo=bar, backend: foo: str = Query(...)).
   - Request body field names match — including casing (camelCase in frontend JSON vs
     snake_case in backend Pydantic model). Check if the backend has a model_config
     alias_generator; if not, field names must be identical.

b) Response shape: check the backend response model against what the frontend actually reads.
   - Field names the frontend accesses (response.data.someField) must exist in the backend
     response model with the same name and type.
   - Nested structures: if backend returns {"patient": {"id": ...}} but frontend reads
     response.patientId, the contract is broken.
   - Optional vs required: if frontend renders a field unconditionally, the backend must
     always return it (not Optional with None default).
   - Pagination: if backend paginates (returns {items, total, page}), frontend must handle
     the wrapper — not assume a flat array.

c) Error shape: does the frontend handle the backend's error format?
   Check that error responses (4xx/5xx) use the same envelope the frontend expects
   (e.g. {detail: "..."} vs {error: "...", message: "..."}).

d) Auth headers: frontend must send the correct auth header (Bearer token, API key, cookie).
   A new endpoint that requires auth but receives no credentials will silently fail in
   the browser with a 401/403 the user may not see.

STEP 7 — Security and commit hygiene:
a) Auth on new routes: every new API endpoint or RPC must enforce authentication and
   authorisation. A route without an auth check is a security gap even if the PRD didn't
   mention it.

b) Injection: user input must never be concatenated into SQL, shell commands, file paths,
   or template strings. It must be parameterized or sanitized.

c) Sensitive data in logs: passwords, tokens, PII, and internal IDs must not appear in
   log output at INFO or above. They must be masked or omitted.

d) Commit scope: run git diff --stat <fork-point>...HEAD and check for changed files
   unrelated to this PRD. Agent sessions routinely edit adjacent files "while there".
   Flag every unrelated change — it belongs in a separate commit.

e) Commit message accuracy: read git log <fork-point>..HEAD. Does each message accurately
   describe what that commit actually changed? "Fix typo" that includes logic changes
   obscures history and must be corrected.

STEP 8 — Decide:
If every criterion is met and no issues from steps 3-7:
- Commit: git commit --allow-empty -m "review: all criteria verified for %s"
- Output <chief-done/>

If any gap is found:
- Edit THIS prd.md (not a new file) and append fix stories after this block.
  Use IDs RV-001, RV-002, etc. Each needs:
    ### RV-001: <short title — file, symbol, issue type>
    **Priority:** 1
    <one paragraph: exact location, what is wrong, how to fix it>
    - [ ] <specific, testable acceptance criterion>
  Group minor issues. Max 5 stories.
- Commit: git commit -m "review: found N gaps, added fix stories"
- Output <chief-done/>

- [ ] README.md and CLAUDE.md read
- [ ] Diff base correctly determined (not assumed)
- [ ] Every criterion checked — stubs, copy-paste leaks, and context errors caught
- [ ] Async correctness and partial-failure rollback verified
- [ ] Tests read and confirmed to actually test the implementation
- [ ] All callers of changed symbols checked — incomplete refactors caught
- [ ] New code checked: duplication, wrong layer, fabricated imports, dead code
- [ ] End-to-end wiring verified: routes registered, dispatchers updated, entry points connected
- [ ] Frontend/backend contract checked: URL, method, field names, response shape, error shape
- [ ] Security: auth on new routes, no injection, no sensitive data in logs
- [ ] Commit scope clean, commit messages accurate
- [ ] Either all criteria clean, or specific RV fix stories added to prd.md
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

STEP 1 — Understand the project:
- Read README.md (repo root) for project overview and domain context.
- Read CLAUDE.md (repo root and ~/.claude/CLAUDE.md if present) for architecture rules,
  naming conventions, forbidden patterns, and module-specific constraints.
- Run: git log --oneline -20 to understand the development rhythm and recent changes.
- Skim 3-5 key source files to identify existing patterns: error handling style,
  naming conventions, package/module structure, API conventions, test patterns.

STEP 2 — Review each planned story for implementation readiness:
For EVERY story, check all of the following:

a) Breaking changes: does it modify public interfaces, APIs, types, or shared behaviour
   that other code depends on? If so, the story must explicitly state how backwards
   compatibility is handled or that it is intentionally broken.

b) Architecture alignment: does the approach fit existing patterns? Flag any story that
   would introduce a new pattern where an established one already exists (e.g. a new
   error-handling style, a new way to do auth, a different data access pattern).

c) Acceptance criteria quality: every criterion must be specific, testable, and
   unambiguous. Rewrite any vague criteria ("should work", "handle errors", "look good")
   to be concrete and verifiable with a command or assertion.

d) Story ordering and dependencies: are there ordering constraints the current Priority
   values don't respect? (e.g. a story that depends on a shared component that is built
   in a later story) Adjust Priority values if so.

e) Missing edge cases: are the following explicitly covered in acceptance criteria where
   relevant — null/empty inputs, missing keys, auth failures, concurrent access, large
   payloads, pagination, error propagation?

f) Scope creep risk: does any story's description imply changes far beyond what the
   acceptance criteria test? If so, narrow the description or add criteria to bound it.

g) Cross-layer completeness: if a story adds a feature spanning backend and frontend,
   are there explicit acceptance criteria covering BOTH sides?
   Common omissions: UI story exists, no API story; API story exists, no UI wiring.
   Each changed layer must have at least one testable criterion.

h) Wiring and registration requirements explicit: AI agents implement core logic but
   skip registration steps unless criteria demand it. For each story, verify:
   - New API route: is there a criterion that it is reachable (not just defined)?
   - New pipeline step/executor: is registration in the dispatch map required?
   - New UI page/component: is there a criterion that it appears in navigation/routing?
   - New DB table: is a migration required? Is rollback covered?
   If any registration step is load-bearing but absent from criteria, add it now.

i) Test requirements explicit in criteria: if a criterion does not mention a test,
   an AI agent will skip writing one. For every story check:
   - At least one criterion requires a unit or integration test.
   - Edge-case criteria are paired with a test requirement.
   - New API endpoints have a criterion requiring an integration test.
   Add "covered by a unit test" or "verified by integration test" where missing.

j) Implementation hints sufficient: each story must give enough context to implement
   without guessing. Flag stories missing:
   - A reference to the existing pattern to follow (e.g., "follow pattern in X.py").
   - Key files or classes to modify.
   - Non-obvious constraints (ordering, idempotency, backwards-compat requirement).
   Vague stories produce random AI implementations — tighten them before dev starts.

STEP 3 — Decide:
If the PRD is well-formed and every story is implementation-ready:
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

- [ ] README.md and CLAUDE.md read — project context understood
- [ ] Each story checked for breaking changes and architecture alignment
- [ ] All acceptance criteria are specific, testable, and unambiguous
- [ ] Story ordering and priorities respect dependencies
- [ ] Edge cases (nulls, auth failures, pagination, concurrency) in criteria
- [ ] Cross-layer features have criteria covering both frontend and backend
- [ ] Wiring/registration steps (router, dispatch map, migrations) explicit in criteria
- [ ] Every story has at least one test requirement in its criteria
- [ ] Each story has sufficient implementation hints (pattern reference, file, constraint)
- [ ] PRD approved or revised with tracked changes
`, preDevReviewStoryID, voiceSection, storyList, p.Project)
}
