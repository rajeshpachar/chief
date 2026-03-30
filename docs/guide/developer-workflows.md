---
description: How to use Chief for every type of developer work — building features, fixing bugs, improving performance, adding test coverage, and more. Chief classifies your intent and generates the right stories.
---

# Developer Workflows

Chief isn't just for building new features. Every type of work a developer does can be driven through Chief.

## Work Types

Chief automatically classifies your request into one of these types and generates appropriate stories:

| Type | When to use | What you get |
|------|------------|--------------|
| `feature` | New capability, endpoint, or UI | Feature stories with GWT acceptance criteria |
| `bug-fix` | Something is broken | Reproduce → fix → regression test stories |
| `test-coverage` | Tests missing or inadequate | Test-first stories (write the failing test, then pass it) |
| `robustness` | Edge cases, error handling, resilience | Defensive stories, error scenario tests |
| `perf` | Something is too slow | Profile → benchmark → optimise → verify stories |
| `refactor` | Clean up without changing behaviour | Safe-refactor stories with before/after test coverage |
| `security` | Auth gaps, injection risks, secrets | Security audit + hardening stories |
| `migration` | DB schema, API version, dependency upgrade | Versioned migration with rollback stories |
| `exploration` | "How does X work?" / investigate first | Spike + findings document story |
| `server-debug` | Service is down or misbehaving | Log-trace + root-cause stories |
| `review` | Before PR merge | Review checklist story |
| `batch-fix` | Many small issues | Triage + fix-in-order stories |

## Feature Building

```bash
chief prd --voice
```

Describe what you want to build:

> "Add an endpoint that returns the top 5 most frequently searched drug combinations this week. It should hit the analytics table and cache the result for 10 minutes."

Chief researches how similar endpoints are built in your codebase, checks if an analytics service already exists, reads the database schema from your models, then generates stories:

1. Write the test first — define expected response shape
2. Add the query to the analytics service
3. Wire the endpoint
4. Add the cache layer (following your existing cache patterns)
5. Verify with a live API test

**Force the type if needed:**
```bash
chief prd --type feature --voice
```

## Bug Fixing

```bash
chief prd --voice
```

> "Users are getting a 422 when they submit the login form after the password reset flow. Started after last Wednesday's deploy."

Chief checks git history around Wednesday's deploy, traces the login endpoint's validation path, and generates:

1. Write a failing test that reproduces the 422
2. Find the root cause in the validation logic
3. Fix with minimal change
4. Confirm the test now passes
5. Check no other auth flows regressed

::: tip Fix priority order
Chief always orders bug-fix stories correctly: build failures first, type errors second, test failures third, logic bugs fourth. You never fix a symptom before the root cause.
:::

## Performance Improvement

```bash
chief prd --type perf "the /api/search endpoint takes 4s on large result sets"
```

Chief researches the endpoint's query plan, checks existing benchmark tooling, then generates:

1. Add a benchmark test that captures current baseline (4s)
2. Profile with `py-spy` / `pprof` / `clinic` (whatever your stack uses)
3. Implement the optimisation (index, query rewrite, N+1 elimination)
4. Verify the benchmark is now under target (e.g. < 500ms)
5. Add the benchmark to CI so regression is caught

## Adding Test Coverage

```bash
chief prd --type test-coverage "the payment processing module has no unit tests"
```

Chief reads the payment module, identifies every untested function and branch, then generates one story per coverage gap. Each story follows TDD order:

1. Write the failing test
2. Confirm it fails for the right reason
3. The implementation already exists — just make the test pass
4. Repeat per function

## Reliability & Robustness

```bash
chief prd --type robustness "the file upload endpoint has no size or type validation"
```

Chief maps the upload path end-to-end, checks how other endpoints handle validation in your codebase, then generates:

1. Test: upload a 0-byte file → expect 400
2. Test: upload a 2GB file → expect 413
3. Test: upload an exe disguised as jpg → expect 422
4. Implement the validation (following your existing patterns)
5. Test: valid upload still works end-to-end

## Refactoring

```bash
chief prd --type refactor "the auth middleware is duplicated in three places"
```

Chief finds all three copies, checks for behavioural differences (critical — they may have diverged), then generates:

1. Write characterisation tests for all three call sites (lock in current behaviour)
2. Extract the shared middleware
3. Replace each copy
4. Confirm all characterisation tests still pass
5. Delete the dead code

::: warning Safe by default
Chief will not suggest a refactor without test coverage first. If tests don't exist, the first story always creates them.
:::

## Security Review

```bash
chief prd --type security "review the admin API for auth gaps before the pen test"
```

Chief reads every admin endpoint, checks auth decorators, traces the permission model, and generates a review checklist story followed by hardening stories for any gaps found.

## Database Migration

```bash
chief prd --type migration "add a composite index on (user_id, created_at) to the events table"
```

Chief checks your migration framework (Alembic, Flyway, Django migrations), reads existing migration history, and generates:

1. Create the migration file (up + down)
2. Test on a copy of production row count to verify it completes in < 60s
3. Verify the index is used by the query planner for the target query
4. Rollback test — down migration works cleanly

## Exploration / Investigation

When you don't know what the fix is yet:

```bash
chief prd --type exploration "understand why memory usage grows continuously on the worker service"
```

Chief generates a spike story: trace memory allocation, read relevant code, produce a findings document with root cause hypothesis. The output becomes the input for a follow-up bug-fix PRD.

## Multi-Service Debugging

When multiple services need to be healthy together:

```bash
chief orchestrate
```

Chief polls each service's logs and health checks concurrently, identifies which services have errors, and spawns targeted fix agents — one per unhealthy service. See [Orchestration](./orchestration).

## Running Multiple Scenarios

To validate a scenario end-to-end after implementation:

```bash
chief validate --fix --push
```

Chief runs your local test suite, then runs every API use-case test against staging, spawns a fix agent for failures, commits, pushes to CI, waits for the pipeline, and re-tests. The loop repeats until everything passes or max cycles is reached. See [Validate & Test](./validate-and-test).

## Choosing the Right Starting Point

```
Is it broken right now?
├── Yes → bug-fix or server-debug
└── No, but...
    ├── It's slow → perf
    ├── It's missing tests → test-coverage
    ├── It's messy code → refactor
    ├── It's a new thing → feature
    ├── It's risky (security/compliance) → security
    └── I'm not sure what's wrong → exploration
```

## See Also

- [Voice-Driven Development](./voice-driven-development) — how to describe work via voice
- [Skills](./skills) — save repeatable patterns so Chief can reuse them
- [Validate & Test](./validate-and-test) — close the loop with real API tests and AI judges
