---
description: A complete developer session with Chief — from speaking your intent to shipping validated code. Voice input, auto-research, confirmation, implementation, testing, and skill building.
---

# End-to-End Developer Session

Here's what a full session looks like, from first word to shipped code.

## Starting Cold: Just Speak

You don't need to know what command to run or how to structure a PRD. Open a terminal and:

```bash
chief prd --voice
```

Speak your intent. One to three sentences is enough:

> "The /api/reports/export endpoint is timing out for large date ranges. Users exporting more than 90 days of data get a 504. I think we need to either paginate the export or run it async with a job queue."

Chief records until 5 seconds of silence, then shows the transcript:

```
Transcript: "The /api/reports/export endpoint is timing out for large
date ranges. Users exporting more than 90 days of data get a 504..."

[K]eep  [R]edo  [D]one — add more? [y/N]
```

Press **D** (done). Research starts immediately.

## Chief Researches the Codebase

No prompt engineering. No "please look at these files first". Chief reads:

- Your git history — finds the last time this endpoint was changed, any related work in progress
- The export endpoint — traces the code path, finds the ORM query that's building the full result set in memory
- Your CLAUDE.md — reads your architecture rules (e.g. "async jobs use Celery, not threads")
- Your skills — if you have a `job-queue` skill, it reads it now
- Web search — if the timeout is from an upstream library it doesn't know, it searches for current docs

Then shows you the summary:

```
── Research findings ─────────────────────────────────────
## Work Type
robustness

## Affected Files
| File | Why | Risk |
|------|-----|------|
| api/reports/export.py | contains the export endpoint | MEDIUM |
| services/reports.py   | _build_export_query(): loads all rows into memory | MEDIUM |

## Recommended Approach
Two options (both valid per architecture rules):
1. Streaming: use cursor-based pagination to stream rows in 1000-row chunks
2. Async: offload to Celery job, return job_id, poll /api/jobs/<id>

CLAUDE.md says: "heavy operations should be async via Celery" — recommending option 2.

## Information Completeness
SUFFICIENT

## Suggested PRD Slug
fix-export-timeout-async-job
... (full report: .chief/research-fix-export-timeout-async-job.md)
```

## Add Corrections via Voice

The research saw your CLAUDE.md says Celery. But you know something it doesn't:

```
── Post-research review ──────────────────────────────────
Add feedback? [y/N]
```

Press **y**, speak:

> "We already have the job queue infrastructure in place — there's a job_queue.py in services/. The endpoint just needs to use it. The frontend already knows how to poll job status."

Chief appends this correction. Now the PRD will be precise about what already exists vs. what needs building.

## Review the PRD Before Anything Runs

Chief generates the PRD and asks:

```
PRD generated: fix-export-timeout-async-job (type: robustness)
Saved to: .chief/prds/fix-export-timeout-async-job/prd.md

Open in editor to review? [y/N]
```

Press **y**. The PRD opens. You see:

```markdown
## Story 1: Write a failing integration test for the timeout scenario
GIVEN a user requests an export for 90+ days of data
WHEN they call POST /api/reports/export
THEN the response should be 202 Accepted with a job_id (not 504 Timeout)

Verify: pytest tests/test_reports.py::test_large_export_returns_job_id -v

## Story 2: Refactor export endpoint to use existing job_queue.py
GIVEN the job queue infrastructure exists in services/job_queue.py
WHEN a large export is requested
THEN the endpoint should enqueue a job and return 202 {job_id: "..."}
  following the pattern in services/job_queue.py

Verify: pytest tests/test_reports.py -v

## Story 3: Verify job completes and CSV is downloadable
GIVEN the export job is enqueued
WHEN the job completes
THEN GET /api/jobs/<job_id>/result should return the CSV download URL

Verify: pytest tests/test_reports.py::test_export_job_lifecycle -v
```

Looks right. Close the editor. The agent loop starts.

## The Agent Loop Runs

Chief works through stories one at a time. Each story:
1. Fresh context window (no baggage from previous stories)
2. Reads `progress.md` to know what was built before
3. Codes, tests, fixes
4. Commits: `fix(reports): offload large export to async job via job_queue.py`

You can watch, pause, or walk away:

```
chief/fix-export-timeout-async-job
│
├── [✓] Story 1: Write failing test — committed 2m ago
├── [⟳] Story 2: Refactor endpoint — running...
└── [ ] Story 3: Verify job lifecycle — pending
```

## Validate the Result

Stories done. Now close the loop:

```bash
chief validate --fix --push
```

Chief runs your test suite, then hits staging with the API tests from your config:

```
── Cycle 1/3 ────────────────────────────────────────────
Running local tests: pytest -x -q
  ✓ Local tests passed (47 passed in 12.3s)

Running 6 API use-case test(s) against https://staging.yourapp.com
  ✓ [45ms]  login with valid credentials
  ✓ [890ms] large export returns 202 with job_id
  ✓ [12ms]  small export returns result directly
  ✓ [8ms]   unauthenticated access rejected
  ✗ [2100ms] job result is downloadable
      → expected status 200, got 404. Body: {"error":"job not found"}

Spawning fix agent...
  Root cause: job_id format mismatch — endpoint returns UUID, poller expects integer
  Fix: normalised to UUID throughout
  Committing: fix(reports): normalise job_id to UUID in export and job status endpoints
  Pushing to branch: dev
  ✓ CI/CD passed (2m 14s)

── Cycle 2/3 ────────────────────────────────────────────
  ✓ Local tests passed
  ✓ All 6 API tests passed

✓ All tests passed.
```

## Save What You Learned as a Skill

You just solved async export offloading. Next time you'll face it in another service. Save the pattern:

```bash
mkdir -p .claude/commands/async-job-offload
```

Write `.claude/commands/async-job-offload/SKILL.md`:

```markdown
---
name: async-job-offload
description: Offload slow endpoints to async jobs using job_queue.py; return 202 with job_id
---

# Async Job Offload Pattern

## When to use
Endpoint takes > 5s, times out under load, or processes large datasets.

## Implementation pattern (this repo)

1. Check `services/job_queue.py` — use `enqueue_job(task_fn, **kwargs)`
2. Endpoint returns `202 {"job_id": "<uuid>"}`
3. Job result stored in Redis with key `job:<uuid>:result`
4. Poll via `GET /api/jobs/<uuid>/result` — returns 200 with result or 202 if still running

## Test pattern

```python
def test_slow_endpoint_returns_job_id():
    resp = client.post("/api/export", json={"days": 90})
    assert resp.status_code == 202
    assert "job_id" in resp.json()

def test_job_result_is_reachable(job_id):
    # Poll until done (max 30s in test)
    for _ in range(30):
        resp = client.get(f"/api/jobs/{job_id}/result")
        if resp.status_code == 200:
            return resp.json()
        time.sleep(1)
    pytest.fail("job did not complete in 30s")
```

## Common mistakes
- Returning the job result synchronously "just to be safe" — defeats the purpose
- Integer job IDs — use UUID to avoid enumeration attacks
- Not setting a TTL on the job result — Redis memory leak
```

Next time you say *"this endpoint is timing out"*, Chief reads this skill and starts from your exact pattern — not from first principles.

## The Complete Flow in One View

```
You speak intent
      │
      ▼
Chief transcribes (mlx/OpenAI/Gemini)
      │
      ▼
Auto-research (codebase + git + skills + web)
      │
      ▼
You review findings, optionally correct
      │
      ▼
PRD generated (right type, right stories, GWT criteria)
      │
      ▼
You confirm (edit if needed)
      │
      ▼
Agent loop (story by story, fresh context each time)
      │
      ▼
Validate (local tests → API tests → fix → CI → redeploy → re-test)
      │
      ▼
✓ Shipped
      │
      ▼
Save skill (optional — encode the pattern for next time)
```

## When to Use Which Command

| You want to... | Command |
|---|---|
| Describe new work via voice | `chief prd --voice` |
| Describe work via text | `chief prd "description"` |
| Override the PRD type | `chief prd --type bug-fix --voice` |
| Skip research (you know the solution) | `chief prd --no-research "..."` |
| Run the TUI dashboard | `chief` |
| Validate staging after implementation | `chief validate` |
| Validate + auto-fix failures | `chief validate --fix` |
| Validate + fix + push + CI | `chief validate --fix --push` |
| Fix multiple broken services | `chief orchestrate` |
| Preview orchestration without changes | `chief orchestrate --dry-run` |
| Generate starter config | `chief validate --init` |

## See Also

- [Voice-Driven Development](./voice-driven-development)
- [Developer Workflows](./developer-workflows)
- [Skills](./skills)
- [Validate & Test](./validate-and-test)
- [Orchestration](./orchestration)
