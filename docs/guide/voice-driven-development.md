---
description: Use voice input to drive all developer work — features, bug fixes, performance, testing. Chief researches the codebase, proposes an approach, and gets confirmation before doing anything.
---

# Voice-Driven Development

You don't need to write a PRD first. You can speak your intent — one sentence or ten — and Chief will figure out the rest.

## The Core Loop

```
  You speak (or type)
        │
        ▼
  Chief transcribes
        │
        ▼
  Auto-research: reads codebase,
  git history, CLAUDE.md, skills
        │
        ▼
  Shows findings + proposed approach
        │
        ▼
  You confirm, correct, or add more
        │
        ▼
  PRD generated with concrete stories
        │
        ▼
  Agent loop runs → code committed
```

At every step you stay in control. Chief proposes — you confirm.

## Starting with Voice

```bash
chief prd --voice
```

That's it. Chief starts recording. Speak naturally:

> "The drug interaction search endpoint is returning 500 errors when you pass more than three drugs. I think it's a list slicing bug somewhere in the interaction service."

Chief transcribes, shows you the transcript, then asks:

```
Transcript: "The drug interaction search endpoint is returning 500 errors..."

[K]eep  [R]edo  [D]one — add more? [y/N]
```

**K** — accept this segment, record another
**R** — discard and re-record (background noise, false start)
**D** — done, move to research

You can speak in multiple rounds. Everything is combined before research runs.

## What Auto-Research Does

After you finish speaking, Chief runs a research pass against your repo. It reads:

- Your **CLAUDE.md** — architecture rules, conventions
- **Git history** — recent commits, what changed, ongoing work that could conflict
- **Source files** — traces the code path related to your request
- **Existing skills** — checks `.claude/commands/` and `~/.claude/commands/` for relevant patterns
- **Existing PRDs** — avoids duplicating work already in progress
- **Web** — if your request involves a library or API it's uncertain about

Then it shows you a summary:

```
── Research findings ─────────────────────────────────────
## Work Type
bug-fix

## Affected Files
| File | Why | Risk |
|------|-----|------|
| services/interaction/handler.py | contains the search endpoint | LOW |
| services/interaction/engine.py  | slicing logic at line 142     | LOW |

## Recommended Approach
Fix the slice bounds in engine.py:142. The bug is `drugs[1:3]`
should be `drugs[1:]` — it drops the last drug when len > 3.

## Information Completeness
SUFFICIENT
... (full report saved to .chief/research-fix-drug-interaction.md)
```

## Post-Research Correction Loop

After seeing the findings, if the research missed something, you can add a correction via voice:

```
── Post-research review ──────────────────────────────────
Review the findings above. Add corrections or extra context,
or press Enter to proceed to PRD generation.

Add feedback? [y/N]
```

Say: *"Also check the caching layer — we added Redis caching last week and the cache key might not include all three drugs."*

Chief appends your correction to the research context before generating the PRD.

## The PRD That Gets Generated

Chief classifies your request automatically and generates the right kind of stories. For a bug fix you get:

```markdown
## Story 1: Reproduce the bug
GIVEN three or more drugs are passed to /api/drugs/interactions
WHEN the endpoint is called
THEN it should return 200 with all pairwise interactions

Verify: pytest tests/test_interaction.py::test_three_drug_search -v

## Story 2: Fix the slice bounds in engine.py
...
```

For a new feature you get feature stories with acceptance criteria. For a test gap you get test-coverage stories. Chief knows the difference.

## Confirming Before Anything Runs

Once the PRD is generated, Chief shows you the slug and type:

```
PRD generated: fix-drug-interaction-slice-bounds (type: bug-fix)
Saved to: .chief/prds/fix-drug-interaction-slice-bounds/prd.md

Open in editor to review before running? [y/N]
```

You can inspect, edit, or reject the PRD before the agent loop starts. Nothing is committed until you say go.

## Skipping Voice (Text Mode)

If you prefer typing:

```bash
chief prd "fix the drug search endpoint — it errors with more than 3 drugs"
```

Same research pass, same PRD generation, no microphone needed.

## Skipping Research

If you already know exactly what needs doing:

```bash
chief prd --no-research "add index to users.email column"
```

Research is skipped and the PRD is generated directly from your description.

## Voice Backends

Chief picks the best available backend automatically, in this order:

| Backend | Requires | Quality | Speed |
|---------|----------|---------|-------|
| `mlx` (local) | `mlx_whisper` + `sox`/`ffmpeg` | High | Fastest (no network) |
| `openai` | `OPENAI_API_KEY` + `sox`/`ffmpeg` | Highest | Fast |
| `gemini` | `GEMINI_API_KEY` + `sox`/`ffmpeg` | High | Fast |
| `prompt` | Nothing | — | Text fallback |

Force a specific backend:

```bash
chief prd --voice --voice-backend openai
```

Install local backend (Apple Silicon):
```bash
pip install mlx-whisper
brew install sox
```

## Silence Detection

Recording stops automatically after 5 seconds of silence. You don't need to press anything.

To stop early (background noise, false start): press **Ctrl+C**. If the recording captured audio (> 44 bytes), it's used. Otherwise it's discarded and you're prompted to redo.

## See Also

- [Developer Workflows](./developer-workflows) — what type of work to use for different tasks
- [Skills](./skills) — reuse patterns across projects
- [Validate & Test](./validate-and-test) — close the loop with API tests and CI
