---
description: Build reusable skills that Chief auto-applies to the right tasks. Skills encode how to approach a problem domain — database migrations, Kubernetes debugging, API design review — so you never explain the same thing twice.
---

# Skills

A skill is a markdown file that tells Chief's agents how to approach a specific domain. Once written, it's automatically applied whenever the task is relevant.

You never need to say "remember to check the migration rollback" again. The migration skill says it for you.

## How Skills Work

When Chief runs auto-research or spawns a fix agent, it:

1. Discovers all available skills (repo-local + global)
2. Asks Claude which skills are relevant to the current task
3. Injects the full content of relevant skills into the agent's context

The agent reads the skill before acting — it knows your conventions, your pitfalls, your checklists.

## Where Skills Live

**Repo-local skills** — apply only to this project:
```
your-repo/
└── .claude/
    └── commands/
        ├── k8s-debug/
        │   └── SKILL.md
        └── db-migrate/
            └── SKILL.md
```

**Global skills** — apply to every project on your machine:
```
~/.claude/
└── commands/
    ├── code-review/
    │   └── SKILL.md
    └── security-audit/
        └── SKILL.md
```

Repo-local skills take precedence if a global skill has the same name.

## Skill File Format

A skill is a markdown file with YAML frontmatter:

```markdown
---
name: db-migrate
description: Guide safe database migrations with rollback verification and zero-downtime patterns
---

# Database Migration Skill

## Before writing a migration

- Check the current schema in `alembic/versions/` — read the latest migration file first
- Confirm the target table's row count: `SELECT COUNT(*) FROM <table>` — if > 1M rows, the migration must be online (no full table lock)
- Check if this column/index already exists before adding it

## Migration file requirements

Every migration must have:
1. An `upgrade()` function
2. A `downgrade()` function that exactly reverses `upgrade()`
3. A docstring explaining why this migration exists

## Zero-downtime rules

- Never drop a column in the same migration that removes its code references — deploy the code first, migrate second
- Never rename a column — add the new column, backfill, update code, then drop the old column in a separate migration
- Adding a NOT NULL column requires a default value or a backfill migration first

## Testing the migration

Before committing:
1. Run `alembic upgrade head` against a copy of the production database dump
2. Confirm it completes in < 30 seconds (or document why it's acceptable if longer)
3. Run `alembic downgrade -1` and verify the schema matches the pre-migration state

## Commit format

```
db: add index on (user_id, created_at) to events table

Migration: a3f8c2d1e4b5
Estimated time on prod: < 5s (events table: ~50K rows)
Rollback: alembic downgrade a3f8c2d1e4b5
```
```

The `description` field is what Chief uses to decide if this skill is relevant. Keep it specific and accurate.

## Building a Skill After Solving a Problem

The best time to write a skill is right after you've solved a problem you'll face again.

**Pattern:**
1. You hit a problem (Kubernetes pod crashlooping, flaky migration, tricky auth pattern)
2. Chief helps you solve it
3. You extract the approach into a skill so next time Chief starts from that knowledge

**Example: After debugging a Kubernetes memory issue:**

```bash
mkdir -p .claude/commands/k8s-memory-debug
```

Create `.claude/commands/k8s-memory-debug/SKILL.md`:

```markdown
---
name: k8s-memory-debug
description: Debug Kubernetes pod OOMKilled and memory growth issues using kubectl and py-spy
---

# Kubernetes Memory Debug Skill

## First: establish what's happening

```bash
# Check recent OOM events
kubectl get events --field-selector reason=OOMKilling -n <namespace>

# Check current memory usage vs limits
kubectl top pods -n <namespace>

# Check the pod's resource limits
kubectl describe pod <pod-name> -n <namespace> | grep -A 5 Limits
```

## Profile the running process

```bash
# Attach py-spy to the running container
kubectl exec -it <pod> -- pip install py-spy
kubectl exec -it <pod> -- py-spy top --pid 1

# Generate a flame graph
kubectl exec -it <pod> -- py-spy record -o profile.svg --pid 1 --duration 30
kubectl cp <pod>:profile.svg ./profile.svg
```

## Common causes in this repo

- Unbounded LRU cache in `services/cache.py` — check `maxsize` parameter
- Celery worker memory leak on task queue accumulation — check `CELERYD_MAX_TASKS_PER_CHILD`
- SQLAlchemy session not closed after batch processing — look for `session.close()` missing in finally blocks
```

Now, next time you say *"the worker pod keeps getting OOMKilled"*, Chief will automatically read this skill and apply it without you explaining anything.

## Skills for Repeatable Tasks

Some tasks happen on every project. Build these as global skills:

**`~/.claude/commands/code-review/SKILL.md`**
```markdown
---
name: code-review
description: Review a PR or diff for correctness, security, test coverage, and architectural compliance
---

# Code Review Skill

## Checklist

### Correctness
- Does the change do what the PR description says?
- Are edge cases handled? (empty input, null, max values, concurrent access)
- Are errors handled and propagated correctly?

### Security
- No secrets or credentials in code
- SQL queries use parameterised inputs (no f-strings into queries)
- User input is validated before use
- Auth checks cannot be bypassed

### Tests
- New behaviour has tests
- Tests cover the failure path, not just the happy path
- Tests are independent (no shared state between test cases)

### Architecture
- Does the change follow CLAUDE.md conventions?
- Is new code in the right layer (no business logic in routes)?
- Are dependencies imported correctly (no circular imports)?

## Output format

For each issue found:
```
FILE: path/to/file.py:42
SEVERITY: HIGH / MEDIUM / LOW
ISSUE: <one sentence>
FIX: <specific suggestion>
```
```

**`~/.claude/commands/security-audit/SKILL.md`**
```markdown
---
name: security-audit
description: Audit endpoints for OWASP top-10 vulnerabilities before deployment
---
...
```

## Suggested Skills to Build

Start with whichever matches your stack:

| Skill name | Description | Build when |
|-----------|-------------|-----------|
| `db-migrate` | Safe migration patterns for your ORM | After your first tricky migration |
| `k8s-debug` | Pod health, logs, exec patterns | After debugging a pod issue |
| `api-design` | REST conventions, versioning, error format | When onboarding a new service |
| `code-review` | PR review checklist | Once, then use forever |
| `perf-profile` | How to profile your stack | After your first performance investigation |
| `ci-debug` | Reading CI logs, common failures | After debugging a flaky CI run |
| `security-audit` | OWASP checklist for your stack | Before first pen test |
| `onboarding` | New developer setup, which services run locally | When someone joins the team |

## Skill Auto-Selection

Chief uses Claude to decide which skills are relevant — not keyword matching. If you describe a task involving database schema changes, the `db-migrate` skill will be selected even if you said "add a column" not "migrate".

You don't manage skill selection manually. Write good `description` fields and Chief handles the rest.

## Verifying Skill Discovery

To see which skills Chief has found:

```bash
chief prd --no-research --dry-run "test task"
```

Or check the research report after running `chief prd`:

```
## Existing Skills / Patterns Used
- k8s-debug: Debug Kubernetes pods and memory issues
- db-migrate: Safe migration patterns
```

If a skill isn't appearing, check:
- The directory has a `SKILL.md` or `README.md`
- The `description:` field is in the YAML frontmatter
- The directory is in `.claude/commands/`, `.claude/skills/`, or `~/.claude/commands/`

## See Also

- [Voice-Driven Development](./voice-driven-development) — describe tasks via voice; skills are applied automatically
- [Developer Workflows](./developer-workflows) — types of work skills support
- [Validate & Test](./validate-and-test) — skills can also encode test patterns and validation checklists
