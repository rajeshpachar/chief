---
description: Use chief setup to intelligently configure .chief/config.yaml for your repo. Chief studies your codebase, asks a few targeted questions, and produces a complete config with real test commands, auth flows, and API test cases.
---

# Setup

`chief setup` reads your repo and produces a complete `.chief/config.yaml`. It studies what's actually in your codebase and asks the right questions to fill in the gaps.

If a config already exists, setup updates and corrects it rather than replacing it from scratch.

## Run It

```bash
chief setup
```

Chief reads your repo first, then only asks about what it genuinely couldn't figure out.

```
═══════════════════════════════════════════════════════
Chief Setup — studying your repo to configure .chief/config.yaml
═══════════════════════════════════════════════════════

Asking Claude to read your repo and draft the config...

── What Claude found ─────────────────────────────────────
Node.js/TypeScript frontend repo (Next.js 14). Test command: npm run test (vitest).
GitHub Actions CI on main branch (ci.yml). No staging URL in .env.example.
Found /api/auth/login endpoint in src/app/api/auth/login/route.ts — returns
{ data: { accessToken } }. No static token pattern found — login-based auth assumed.

── 2 things Claude couldn't determine ─────────────────────
  (No URL found in .env.example or any config file)
Backend API URL: https://metacrm.k8s-dev.hlthclub.in
  (Login response shape unclear — multiple token field names possible)
JSON path to auth token in login response [token]: data.accessToken

Finalising config with your answers...

── Proposed .chief/config.yaml ──────────────────────────
project:
  testCommand: "npm run test"

  validation:
    stagingUrl: https://metacrm.k8s-dev.hlthclub.in
    credentialsFile: .chief/staging.creds  # add to .gitignore — never commit
    login:
      path: /api/auth/login
      body: '{"email":"${EMAIL}","password":"${PASSWORD}"}'
      tokenField: data.accessToken
      headerPrefix: "Bearer "
    apiTests:
      - name: "health check"
        path: /api/health
        expectStatus: 200
      - name: "login returns access token"
        method: POST
        path: /api/auth/login
        body: '{"email":"test@example.com","password":"test123"}'
        expectStatus: 200
        expectBodyContains: '"accessToken"'
      ...
    cicd:
      provider: github
      branch: main
      workflow: ci.yml
      pollIntervalSec: 30
      maxWaitSec: 600
─────────────────────────────────────────────────────────

Write this config? [y/N]: y

Created .chief/config.yaml
Run 'chief validate' to test it.
```

## How It Works

**Claude reads the repo first.** Before asking you anything, Claude uses its file tools to read:

| File | What it looks for |
|---|---|
| `package.json` | `"scripts"."test"` — exact test command |
| `go.mod`, `requirements.txt` | Language + default test runner |
| `Makefile` | test/lint/run targets |
| `.env.example`, `.env.sample` | Staging URL, auth env var names |
| `.github/workflows/*.yml` | CI provider, push branch, workflow file |
| `.gitlab-ci.yml` | GitLab CI config |
| `CLAUDE.md` | Architecture rules |
| API route files | Endpoint structure, login path, response shape |
| `services/`, `apps/`, `packages/` | Multi-service topology |

**Then Claude only asks about genuine gaps.** If your `.env.example` has `STAGING_URL=https://staging.myapp.com`, Claude uses it — no question asked. If the login endpoint is clearly `POST /api/auth/login` returning `{ token }`, Claude knows the `tokenField` — no question asked.

Typical sessions need 0–3 questions, usually just the staging URL (rarely in the repo) and sometimes the token field path (varies by app).

---

## What to Give Claude

The setup command works best when you give Claude the context it can't find in source code. This is everything that matters.

### 1. A Creds File

The single most important input. Create `.chief/staging.creds` before or right after setup:

```bash
# .chief/staging.creds — DO NOT COMMIT
# Environments
STAGING_URL=https://metacrm.k8s-dev.hlthclub.in
LOCAL_URL=http://localhost:8000

# Default test user
USERNAME=test@example.com
PASSWORD=secret123

# Admin user
ADMIN_USERNAME=admin@example.com
ADMIN_PASSWORD=adminpass

# Tenant B user
TENANT_B_USERNAME=user@tenantb.com
TENANT_B_PASSWORD=tbpass
TENANT_B_ID=tenant-b-uuid

# Service account / long-lived token (if you have one)
API_TOKEN=eyJhbGciOiJIUzI1NiJ9...
```

Chief loads this file before every API test run. All `${VAR}` references in the config expand from it.

**Always add to `.gitignore`:**
```bash
echo ".chief/staging.creds" >> .gitignore
```

### 2. An API Docs URL or Swagger

If your API has an OpenAPI spec or Swagger page, pass it at setup time. Claude fetches it and writes much more accurate tests:

```bash
chief setup --docs https://metacrm.k8s-dev.hlthclub.in/api/docs
```

You can pass multiple URLs:
```bash
chief setup \
  --docs https://api.example.com/openapi.json \
  --docs https://api.example.com/auth
```

Claude reads the actual routes, request/response shapes, and auth scheme — meaning the generated `apiTests` will use real field names and real endpoint paths instead of guesses.

### 3. An Existing Context File

If you already have a credentials or environment notes file (Markdown, `.env`, anything), point Claude at it:

```bash
chief setup --docs ~/creds/k8s-dev-datacloud.md
```

Claude reads whatever format it's in and understands it. Your existing docs become part of the config generation context — login paths, token structures, tenant IDs, environment URLs — Claude pulls it all in automatically.

### 4. Your Non-Obvious Auth Pattern

Tell Claude during setup if your auth is unusual:

- "We use FastAPI OAuth2 — form-encoded body, `username` field, response is `access_token`"
- "We have a separate admin API at `/admin/api/...` that uses a different token"
- "Tenants are identified by `X-Tenant-ID` header, not URL path"
- "We have a service account token in `API_TOKEN` — use that for read-only tests, login for write tests"

Say it in plain English when asked (or in a `--docs` markdown file). Claude figures out the config from the description.

---

## Multiple Users, Tenants, and Environments

Chief's multi-user support is built around two things: the creds file and per-test `loginBody` overrides.

### Creds File as Multi-User Config

Put all users and tenants in the creds file with distinct prefixes:

```bash
# Default user
USERNAME=user@example.com
PASSWORD=pass

# Admin
ADMIN_USERNAME=admin@example.com
ADMIN_PASSWORD=adminpass

# Tenant B
TENANT_B_USERNAME=b-user@example.com
TENANT_B_PASSWORD=bpass
TENANT_B_ID=tenant-b

# Tenant C
TENANT_C_USERNAME=c-user@example.com
TENANT_C_PASSWORD=cpass
TENANT_C_ID=tenant-c
```

### Per-Test User Override

Use `loginBody` on any test to run it as a specific user:

```yaml
project:
  validation:
    credentialsFile: .chief/staging.creds
    login:
      path: /api/auth/login
      body: 'username=${USERNAME}&password=${PASSWORD}'
      contentType: application/x-www-form-urlencoded
      tokenField: access_token
    apiTests:
      - name: "user sees own data only"
        path: /api/patients
        expectStatus: 200

      - name: "admin sees all patients"
        path: /api/patients?all=true
        loginBody: 'username=${ADMIN_USERNAME}&password=${ADMIN_PASSWORD}'
        expectStatus: 200

      - name: "tenant B user cannot see tenant A data"
        path: /api/patients
        loginBody: 'username=${TENANT_B_USERNAME}&password=${TENANT_B_PASSWORD}'
        expectBodyNotContains: '"tenant_id":"tenant-a"'
```

Chief caches tokens by login body — each distinct user logs in once per `chief validate` run. If three tests share the same credentials, they share one token.

### Switching Environments

```bash
# Test against localhost without touching config
CHIEF_API_URL=http://localhost:8000 chief validate

# Test against staging (default from config)
chief validate

# Override with a different cluster
CHIEF_API_URL=https://staging2.example.com chief validate
```

Or put environment URLs in the creds file and reference them via `apiUrl` in config:

```yaml
project:
  validation:
    apiUrl: ${STAGING_URL}   # resolved from .chief/staging.creds
```

---

## Credential-Based Login

When your API requires logging in to get a token:

```yaml
project:
  validation:
    credentialsFile: .chief/staging.creds   # add to .gitignore
    login:
      path: /api/auth/login
      body: '{"email":"${EMAIL}","password":"${PASSWORD}"}'
      tokenField: data.accessToken          # dot-path into JSON response
      headerPrefix: "Bearer "               # prepended to extracted token
```

Before each API test run, chief:
1. Loads `.chief/staging.creds` into the environment
2. POSTs the login body (with `${VARS}` expanded) to the login path
3. Extracts the token at the `tokenField` dot-path in the response JSON
4. Injects `Authorization: Bearer <token>` into all API test requests

### FastAPI OAuth2

FastAPI's built-in OAuth2 uses form-encoded body and requires the field to be named `username`:

```yaml
login:
  path: /api/auth/login
  body: 'username=${USERNAME}&password=${PASSWORD}'
  contentType: application/x-www-form-urlencoded   # required for FastAPI
  tokenField: access_token
  headerPrefix: "Bearer "
```

`chief validate --fix` will auto-diagnose and fix this if you get a 422 error on login.

### `tokenField` dot-path examples

| Response JSON | `tokenField` |
|---|---|
| `{"token":"abc"}` | `token` |
| `{"data":{"accessToken":"abc"}}` | `data.accessToken` |
| `{"auth":{"tokens":{"access":"abc"}}}` | `auth.tokens.access` |
| `{"access_token":"abc"}` | `access_token` |

### Non-Bearer auth

```yaml
login:
  tokenField: apiKey
  headerName: X-API-Key    # default: Authorization
  headerPrefix: ""          # no prefix — just the raw token
```

---

## Frontend Repos: Two URLs

When you're on a frontend repo:

- **`stagingUrl`** — the URL of the deployed frontend (used for end-to-end reference)
- **`apiUrl`** — the backend API base URL — **this is what API tests actually call**

```yaml
project:
  validation:
    stagingUrl: https://staging.myapp.com      # frontend
    apiUrl: https://metacrm.k8s-dev.hlthclub.in  # backend API
```

Without `apiUrl`, chief would try to run API tests against the frontend URL (wrong).

---

## Creds File Format

`.chief/staging.creds` is a plain `.env`-style file:

```bash
# Staging credentials — DO NOT COMMIT

# Environments
STAGING_URL=https://metacrm.k8s-dev.hlthclub.in
LOCAL_URL=http://localhost:8000

# Default user
USERNAME=test@example.com
PASSWORD=secret

# Admin
ADMIN_USERNAME=admin@example.com
ADMIN_PASSWORD=adminpass

# Service account (long-lived token)
API_TOKEN=eyJhbGciOiJIUzI1...
```

Rules:
- One `KEY=VALUE` pair per line
- `#` starts a comment
- Values can be unquoted or quoted (`"value"` or `'value'`)
- Variables already set in the shell are NOT overwritten (safe to combine with CI secrets)

---

## Updating an Existing Config

If `.chief/config.yaml` already exists, setup reads it first and passes it to Claude with the instruction to preserve what's correct and only update what needs fixing.

Common reasons to re-run setup:
- You moved to a different staging environment
- Auth changed from static token to login-based
- Added new services to orchestrate
- CI pipeline changed (GitHub → GitLab, branch renamed)

```bash
chief setup
```

Chief will show what changed before writing.

---

## Flags

| Flag | Description |
|---|---|
| `--force` | Write config without asking for confirmation |
| `--docs <url>` | Fetch a docs/API spec URL, file, or context markdown and include it in Claude's context (repeatable) |

```bash
# Point Claude at your OpenAPI spec
chief setup --docs https://api.example.com/openapi.json

# Give Claude your existing creds/env notes
chief setup --docs ~/creds/k8s-dev-datacloud.md

# Multiple context sources
chief setup \
  --docs https://api.example.com/openapi.json \
  --docs ~/creds/k8s-dev-datacloud.md

# Skip confirmation prompt
chief setup --force
```

---

## Quick Reference by Repo Type

**Monorepo (single service):**
- Just run `chief setup` — it will detect the test command and CI config
- Say `n` to "frontend repo?" unless this specific package is pure UI

**Frontend (React/Next.js/Vue):**
- Pass `--docs <swagger-url>` for accurate API tests
- Provide the backend API URL when asked
- Choose auth option 1 (login with creds) or 2 (static token)

**Multi-service / microservices:**
- Run setup from the repo root — it will detect `services/`, `apps/`, `packages/` subdirs
- The generated config will include `project.services` for `chief orchestrate`
- Each service needs a `healthCheck` command — add that after setup if it wasn't auto-detected

**Kubernetes / cloud-deployed staging:**
- The staging URL is the K8s ingress / load balancer URL (e.g. `https://metacrm.k8s-dev.hlthclub.in`)
- Auth is typically login-based — use option 1
- If you have a service account with a long-lived token, put it in the creds file as `API_TOKEN`

**FastAPI backend:**
- Auth is almost always `application/x-www-form-urlencoded`, field `username`, response `access_token`
- `chief validate --fix` will auto-fix the login config if you get a 422

**CI-only validation (no local tests):**
- Set `project.testCommand: "none"` — chief skips local test run
- Only API tests run against staging

---

## After Setup

```bash
# Verify the config looks right
cat .chief/config.yaml

# Create your creds file (if using login auth)
cat > .chief/staging.creds << 'EOF'
STAGING_URL=https://api.staging.myapp.com
USERNAME=test@example.com
PASSWORD=yourpassword
ADMIN_USERNAME=admin@example.com
ADMIN_PASSWORD=adminpass
EOF
echo ".chief/staging.creds" >> .gitignore

# Test it — report only, no fixes
chief validate

# Test + auto-fix failures (including bad login config)
chief validate --fix

# Full loop: fix + push + wait for CI
chief validate --fix --push

# Test against localhost
CHIEF_API_URL=http://localhost:8000 chief validate
```

## See Also

- [Validate & Test](./validate-and-test) — full details on the validate loop, API tests, CI/CD integration
- [Orchestration](./orchestration) — multi-service setup and monitoring
- [Skills](./skills) — encode test patterns for future use
