---
description: Run Chief across multiple services simultaneously. Each service gets its own fix agent with only its own logs and code — token-efficient, parallel, and coordinated.
---

# Multi-Service Orchestration

When your application runs as multiple services (API, worker, database, cache), a single broken service can cascade. `chief orchestrate` monitors all services concurrently, identifies what's broken, and applies targeted fixes.

## How It Works

```
  Poll all services (parallel)
  ┌──────────┬──────────┬──────────┐
  │   API    │  Worker  │   DB     │
  │ healthy  │ erroring │ healthy  │
  └──────────┴──────────┴──────────┘
        │
        ▼
  Coordinator sees compact JSON summary:
  [{name:"worker", healthy:false, errors:["ConnectionError: Redis..."]}]
        │
        ▼
  Service agent for "worker" spawns
  (sees only: worker log tail + error lines + worker code)
        │
        ▼
  Fix applied → repeat
```

The coordinator never sees full logs or source code — only a compact JSON health summary. Individual service agents see only their own service's context. This keeps token usage proportional to the number of broken services, not the total size of your codebase.

## Setup

Add to `.chief/config.yaml`:

```yaml
project:
  services:
    - name: api
      startCmd: "uvicorn app.main:app --port 8000 --reload"
      logFile: "logs/api.log"
      codeDir: "services/api"
      healthCheck: "curl -sf http://localhost:8000/health"
      logTailLines: 50

    - name: worker
      startCmd: "celery -A tasks worker --loglevel=info"
      logFile: "logs/worker.log"
      codeDir: "services/worker"
      healthCheck: "celery -A tasks inspect ping -d celery@$HOSTNAME"
      logTailLines: 50

    - name: db
      # No startCmd — db is assumed already running (e.g. Docker)
      logFile: "logs/postgres.log"
      codeDir: "migrations"
      healthCheck: "pg_isready -h localhost -p 5432"
```

### Service Config Fields

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Unique identifier. Used in log filenames and agent prompts. |
| `startCmd` | No | Shell command to start the service. Omit if service runs externally (Docker, systemd). |
| `logFile` | No | Path to log file, relative to repo root. Defaults to `logs/<name>.log`. |
| `codeDir` | No | Directory containing this service's source code. Limits agent to relevant files. |
| `healthCheck` | No | Shell command that exits 0 when healthy. If omitted, service is considered healthy when log has no recent errors. |
| `logTailLines` | No | How many recent log lines to send the agent (default: 50). Keep low — error lines are always included regardless. |

## Running Orchestration

```bash
chief orchestrate
```

**Limit cycles:**
```bash
chief orchestrate --cycles 3
```

**Dry run (see what would happen, no Claude calls):**
```bash
chief orchestrate --dry-run
```

## Token Efficiency

Token usage is deliberately bounded. For 3 services where 1 is broken:

```
Coordinator:  ~200 tokens  (compact JSON: name + healthy flag + error count + last error)
Service agent: ~2000 tokens  (50 log lines + extracted error lines + codeDir hint)

Total: ~2200 tokens vs ~50,000+ if everything was sent to one agent
```

The coordinator makes decisions with minimal context (which services to fix) and delegates the heavy work to individual service agents that only see their own service.

## What the Service Agent Sees

For the broken `worker` service, the agent prompt contains:

```
Service: worker
Status: UNHEALTHY

Recent errors (extracted):
  [2026-01-15 14:23:01] ERROR celery.worker: ConnectionError: Redis connection refused (localhost:6379)
  [2026-01-15 14:23:02] ERROR celery.worker: Retrying in 3 seconds...
  [2026-01-15 14:23:05] ERROR celery.worker: ConnectionError: Redis connection refused (localhost:6379)

Recent log (last 50 lines):
  [2026-01-15 14:22:58] INFO  celery.worker: Starting worker
  [2026-01-15 14:23:01] ERROR celery.worker: ConnectionError: Redis...
  ...

Code directory: services/worker
(read relevant files from services/worker/ to understand the code)

Relevant skills:
### Skill: k8s-debug
...if the k8s-debug skill was judged relevant...
```

The agent reads the code, finds the root cause (e.g. `REDIS_URL` env var not set), and applies the fix.

## Handling Service Dependencies

If your services have dependencies (API depends on DB, Worker depends on Redis), configure them so that Chief knows to fix infrastructure services before application services.

::: tip Order services in dependency order
List services with no dependencies first. Chief processes health checks in parallel, but fix agents run in the order services are defined. DB before API before Worker.
:::

```yaml
project:
  services:
    - name: db          # no dependencies
      ...
    - name: redis       # no dependencies
      ...
    - name: api         # depends on db
      ...
    - name: worker      # depends on redis
      ...
```

## Watching Logs During Orchestration

Chief prints a status line per service on each cycle:

```
── Cycle 1/5 ────────────────────────────────────────────
  api     ✓ healthy  (health check passed)
  worker  ✗ unhealthy  (3 errors in last 50 lines)
  db      ✓ healthy  (health check passed)

Coordinator: fix [worker]
Spawning service agent: worker...
  Applied fix: set REDIS_URL env var in worker/.env.example and config loader

── Cycle 2/5 ────────────────────────────────────────────
  api     ✓ healthy
  worker  ✓ healthy  (health check passed)
  db      ✓ healthy

✓ All services healthy after 2 cycle(s)
```

## When a Service Won't Start

If `startCmd` is set and the service process exits immediately, Chief captures stderr and includes it as error context for the fix agent. Common causes it can fix:

- Missing environment variables
- Import errors / missing dependencies
- Config file not found
- Port already in use (it will identify the conflicting process)

## Combining with Validate

Orchestrate and validate complement each other:

```bash
# First: make sure all services are healthy
chief orchestrate

# Then: verify the use cases work end-to-end
chief validate --fix --push
```

Or in CI:
```yaml
- name: Fix services
  run: chief orchestrate --cycles 3

- name: Validate use cases
  run: chief validate --fix --push
```

## See Also

- [Validate & Test](./validate-and-test) — API use-case tests and CI integration
- [Skills](./skills) — build service-specific debug skills (k8s-debug, redis-debug, etc.)
- [Developer Workflows](./developer-workflows) — server-debug workflow for targeted investigation
