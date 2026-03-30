# Chief — Architecture & Development Guide

## Build & Test

```bash
go build ./...                                      # build everything
go test ./... -run "^Test[^R]"                      # unit tests (no Claude API calls)
go test ./internal/cmd/ -run "TestRunXxx"           # integration tests (need real Claude)
go build -o /tmp/chief ./cmd/chief/ && echo OK      # build binary
```

Tests prefixed `TestRun` may call the real Claude API. Never run them in CI without credentials.

## Core Principle: Claude Decides, Go Orchestrates

**Claude makes judgment calls. Go handles deterministic work.**

| Use `claude -p` for | Use Go for |
|---------------------|-----------|
| Which skills are relevant to a task | Discovering skill files on disk |
| Whether a prompt or output is correct | Parsing structured output we defined |
| Interpreting ambiguous CI/log output | Decoding JSON we asked Claude to produce |

Never write Go logic to classify, rank, or evaluate — delegate to `claude -p`.
Never use `claude -p` for structured data we explicitly told Claude to produce (parse it directly).

## Package Layout

```
cmd/chief/          entry point — flag parsing, subcommand routing only
internal/cmd/       command implementations (generate, research, orchestrate, validate, voice)
internal/config/    YAML config structs — no logic, pure data
internal/agent/     provider abstraction (Claude, Codex, OpenCode, Cursor)
internal/loop/      Ralph Wiggum agent loop
internal/prd/       PRD parsing and story management
internal/tui/       Bubble Tea TUI
```

## Key Patterns

**Options structs** — every public command function takes an `XxxOptions` struct, never raw args.

**`runClaudeNonInteractive(cliPath, baseDir, prompt)`** — the only way to call Claude from Go. Always pass `baseDir` so Claude reads the right files.

**`extractJSON(s)`** — strips markdown code fences before unmarshalling Claude output. Always use this before `json.Unmarshal` on Claude responses.

**`sanitizeSlug(s)`** — converts free text to a valid slug. Use for all file/directory names derived from user input.

## Token Budget Rules

These are enforced limits, not suggestions:

- Log tail injected into service agents: **50 lines max** (`LogTailLines`)
- Error lines extracted from logs: **20 lines max**
- API response bodies in fix agent context: **truncate at 500 chars** (`truncate(body, 500)`)
- Research report injected into PRD generation: **use `summariseResearch()`**, never raw `RawReport`
- Git history in research context: **20 commits max**

Violating these causes context overflow in multi-service cycles.

## Research & PRD Generation

- Research always runs unless `--no-research` flag is set.
- Use `parseResearchSufficiency(report)` to check if research is complete — reads the `## Information Completeness` section Claude writes. Do not add another `claude -p` call for this.
- Use `selectRelevantSkills(cliPath, taskDesc, skills)` in fix agents and validate — it calls `claude -p` once to pick relevant skills. Do not call it in the research hot path (research agent reads skills itself via file tools).
- `summariseResearch(report)` extracts high-signal sections only. Always use it before injecting research into a PRD generation prompt.

## Skills System

Skills live in `.claude/commands/`, `.claude/skills/`, or `~/.claude/commands/` (global).

Every skill directory needs a `SKILL.md` with YAML frontmatter:
```markdown
---
name: skill-name
description: One sentence — this is what Claude reads to decide relevance
---
```

`discoverAllSkills(baseDir)` finds all skills. Repo-local takes precedence over global by same name.

## Validate Command

- Fails with a clear error if no `testCommand` and no `apiTests` are configured. No silent defaults.
- `parseGitHubCIStatus` and `parseGitLabCIStatus` use `encoding/json` — never string-contains on JSON.
- Auth header split uses `strings.SplitN(header, ": ", 2)` — limit 2 preserves colons in values.

## Orchestrate Command

- Service names must be unique and non-empty — validated at startup before any Claude calls.
- Coordinator receives compact JSON only (`name`, `healthy`, `error_count`, `last_error`). Never full logs.
- Service agents receive only their own log tail + error lines + `codeDir` hint.

## Voice Input

Backend priority: `mlx` (local) → `openai` → `gemini` → `prompt`.
`detectVoiceBackend()` checks tool availability before API keys.
`sendToOpenAI` and `sendToGemini` are separated from recording so they're testable without audio hardware.

## Error Handling

- Wrap all errors with context: `fmt.Errorf("load config: %w", err)` not bare `err`.
- `runClaudeNonInteractive` failures should name the operation: `"research pass failed: %w"`.
- Config load errors from `os.IsNotExist` return `Default()`, not an error. Other read errors always propagate.

## Testing Conventions

- New functions that don't call Claude: prefix `Test` (included in standard run).
- Functions calling real Claude: prefix `TestRun` (excluded from standard run).
- Integration tests for voice/transcription use `minimalWAV(t)` — a real WAV header + silence, no microphone needed.
- Table-driven tests for all parsing functions (`parseGitHubCIStatus`, `parseResearchSufficiency`, etc.).
