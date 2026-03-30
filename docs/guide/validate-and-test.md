---
description: Run API use-case tests against staging, auto-fix failures, push to CI, and use AI as a judge. Chief closes the loop between implementation and confidence.
---

# Validate & Test

After Chief implements your stories, `chief validate` closes the loop — running real tests against a real server, auto-fixing failures, and verifying CI passes.

## The Validate Loop

```
  Local test suite
        │ fail → fix agent → commit → push → CI
        ▼ pass
  API use-case tests (staging)
        │ fail → fix agent → commit → push → CI
        ▼ pass
  CI/CD pipeline
        │ fail → fix agent → next cycle
        ▼ pass
  ✓ Done
```

Each cycle: test → fix → push → re-test. Chief repeats until everything passes or max cycles is reached.

## Setup

Add to `.chief/config.yaml`:

```yaml
project:
  testCommand: "pytest -x -q 2>&1 | tail -30"  # or: go test ./..., npm test, etc.

  validation:
    stagingUrl: https://staging.yourapp.com
    authHeader: "Authorization: Bearer ${STAGING_TOKEN}"  # env var expanded at runtime

    apiTests:
      - name: "search returns results"
        method: GET
        path: /api/drugs/interactions?drug1=aspirin&drug2=warfarin
        expectStatus: 200
        expectBodyContains: '"interactions"'

      - name: "login with valid credentials"
        method: POST
        path: /api/auth/login
        body: '{"email":"test@example.com","password":"test123"}'
        expectStatus: 200
        expectBodyContains: '"token"'

      - name: "unauthenticated access is rejected"
        path: /api/admin/users
        expectStatus: 401

      - name: "no error field in healthy response"
        path: /api/health
        expectStatus: 200
        expectBodyNotContains: '"error"'

    cicd:
      provider: github          # github | gitlab | none
      branch: dev               # branch to push to
      workflow: ci.yml          # GitHub Actions workflow file
      pollIntervalSec: 30
      maxWaitSec: 600
```

Generate a starter config:
```bash
chief validate --init --staging-url https://staging.yourapp.com
```

## Running Validation

**Report only** (no fixes, no push):
```bash
chief validate
```

**Auto-fix failures:**
```bash
chief validate --fix
```

**Full loop — fix, push, wait for CI:**
```bash
chief validate --fix --push
```

**Override test command:**
```bash
chief validate --fix --test-cmd "go test ./... -race"
```

## What the Fix Agent Sees

When tests fail, Chief spawns a fix agent with precise context:

```
## Local test failures
FAILED tests/test_interaction.py::test_three_drugs - AssertionError
  Expected status 200, got 500
  ...last 30 lines of test output...

## API use-case test failures

### search returns results
Request:  GET /api/drugs/interactions?drug1=aspirin&drug2=warfarin
Status:   500 (expected 200)
Failure:  expected status 200, got 500
Response body: {"error": "list index out of range", "trace": "..."}

## Architecture rules (CLAUDE.md)
...your architecture rules...

## Relevant skills
...any skills Claude judged relevant to these failures...

## Fix rules
- Fix ONLY the failing tests/endpoints shown above
- Do NOT change API contracts, rename fields, or break other endpoints
- Write the failing test case FIRST if it doesn't exist, then fix the implementation
```

The fix agent has full file access. It reads source code, finds the root cause, writes the fix, and states: *root cause, files changed, why this won't break other flows*.

## Multiple Test Scenarios

Use `apiTests` to cover your entire use-case surface — not just happy paths.

**Pattern: cover all user-facing scenarios:**

```yaml
apiTests:
  # ── Happy paths ──────────────────────────────────
  - name: "search with two drugs returns interactions"
    path: /api/drugs/interactions?drug1=aspirin&drug2=warfarin
    expectStatus: 200
    expectBodyContains: '"interactions"'

  - name: "search with one drug returns empty interactions"
    path: /api/drugs/interactions?drug1=aspirin
    expectStatus: 200
    expectBodyContains: '"interactions":[]'

  # ── Auth boundaries ───────────────────────────────
  - name: "no token is rejected"
    path: /api/drugs/interactions
    # no auth header — uses per-test headers override
    headers:
      Authorization: ""
    expectStatus: 401

  - name: "expired token is rejected"
    path: /api/drugs/interactions
    headers:
      Authorization: "Bearer expired-token-here"
    expectStatus: 401

  # ── Input validation ──────────────────────────────
  - name: "unknown drug name returns 404"
    path: /api/drugs/interactions?drug1=notadrug&drug2=warfarin
    expectStatus: 404

  - name: "missing required param returns 400"
    path: /api/drugs/interactions
    expectStatus: 400

  # ── Error hygiene ─────────────────────────────────
  - name: "no stack traces in error responses"
    path: /api/drugs/interactions?drug1=bad&drug2=bad
    expectBodyNotContains: "Traceback"

  - name: "no stack traces in 500 responses"
    path: /api/admin/trigger-error  # a test endpoint that throws
    expectBodyNotContains: "at Object.<anonymous>"
```

## AI as a Judge

Beyond pass/fail HTTP assertions, you can use Claude to judge response quality.

**Pattern: add a judgment story to your PRD:**

```markdown
## Story N: AI judge — verify search result quality
GIVEN the drug interaction search is running
WHEN we call /api/drugs/interactions?drug1=aspirin&drug2=warfarin
THEN Claude should judge the response as:
  - Complete: all known aspirin-warfarin interactions present
  - Accurate: severity ratings match clinical reference data
  - Well-formatted: interactions array has name, severity, description fields

Verify:
```bash
RESPONSE=$(curl -s -H "Authorization: Bearer $STAGING_TOKEN" \
  "$STAGING_URL/api/drugs/interactions?drug1=aspirin&drug2=warfarin")

echo "$RESPONSE" | claude -p "
You are a clinical pharmacology expert. Review this drug interaction API response.
Evaluate:
1. Are all major aspirin-warfarin interactions present? (bleeding risk, anticoagulant effect)
2. Are severity ratings accurate? (both should be HIGH)
3. Is the response format correct? (interactions array with name, severity, description)

Reply with: PASS or FAIL, then one sentence explaining why.

Response: $RESPONSE
"
```
```

The agent runs this as a bash command in the story, and the story only completes if Claude returns `PASS`.

## Reliability Testing with Multiple Scenarios

To test reliability under load or repeated calls:

```markdown
## Story N: verify idempotency of search results
GIVEN the search endpoint is running
WHEN we call it 10 times with the same inputs
THEN every response should be identical

Verify:
```bash
RESULTS=()
for i in $(seq 1 10); do
  RESULT=$(curl -s "$STAGING_URL/api/drugs/interactions?drug1=aspirin&drug2=warfarin" \
    -H "Authorization: Bearer $STAGING_TOKEN" | jq -c '.interactions | sort_by(.name)')
  RESULTS+=("$RESULT")
done

FIRST="${RESULTS[0]}"
for RESULT in "${RESULTS[@]}"; do
  if [ "$RESULT" != "$FIRST" ]; then
    echo "FAIL: non-idempotent response detected"
    exit 1
  fi
done
echo "PASS: all 10 responses identical"
```
```

## CI/CD Integration

When `--push` is set, Chief:

1. Stages all changed files (`git add -A`)
2. Commits with format: `fix(<area>): <one sentence>`
3. Pushes to configured branch
4. Polls CI status every 30 seconds (configurable)
5. On CI pass: waits 10 seconds for deploy to propagate, then re-runs API tests
6. On CI fail: spawns fix agent with CI failure context, starts next cycle

**GitHub Actions** — reads status via `gh run list --json status,conclusion`

**GitLab** — reads status via `glab ci status --output json`

**No CI** (`provider: none`) — pushes and immediately re-tests (assumes deploy is instant)

## Viewing Results

Each cycle prints:

```
── Cycle 1/3 ────────────────────────────────────────
Running local tests: pytest -x -q 2>&1 | tail -30
  ✗ Local tests failed
      FAILED tests/test_interaction.py::test_three_drugs

Running 4 API use-case test(s) against https://staging.yourapp.com
  ✓ [12ms] login with valid credentials
  ✗ [340ms] search returns results
      → expected status 200, got 500. Body: {"error":"list index out of range"...
  ✓ [8ms] unauthenticated access is rejected
  ✓ [15ms] no error field in healthy response

Spawning fix agent...
  Fix applied: fixed slice bounds in engine.py:142
  Committing: fix(interaction): fix list index out of range when >3 drugs passed
  Pushing to branch: dev
  Polling github CI/CD (max 10m0s)...
  CI status: running — waiting 30s...
  CI status: running — waiting 30s...
  ✓ CI/CD passed
  Waiting 10s for deploy to propagate...

── Cycle 2/3 ────────────────────────────────────────
Running local tests: pytest -x -q 2>&1 | tail -30
  ✓ Local tests passed

Running 4 API use-case test(s) against https://staging.yourapp.com
  ✓ [11ms] login with valid credentials
  ✓ [290ms] search returns results
  ✓ [9ms] unauthenticated access is rejected
  ✓ [14ms] no error field in healthy response

✓ All tests passed.
```

## See Also

- [Developer Workflows](./developer-workflows) — how to describe the work before validating it
- [Orchestration](./orchestration) — multi-service health monitoring
- [Skills](./skills) — encode test patterns and validation checklists into reusable skills
- [Configuration Reference](/reference/configuration) — full `.chief/config.yaml` options
