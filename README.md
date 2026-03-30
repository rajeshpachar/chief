# Chief

<p align="center">
  <img src="assets/hero.png" alt="Chief" width="500">
</p>

Build big projects with Claude. Chief breaks your work into tasks and runs Claude Code in a loop until they're done.

**[Documentation](https://minicodemonkey.github.io/chief/)** · **[Quick Start](https://minicodemonkey.github.io/chief/guide/quick-start)**

![Chief TUI](https://minicodemonkey.github.io/chief/images/tui-screenshot.png)

## Install

```bash
brew install minicodemonkey/chief/chief
```

Or via install script:

```bash
curl -fsSL https://raw.githubusercontent.com/MiniCodeMonkey/chief/refs/heads/main/install.sh | sh
```

## Commands

```
chief [<name>]                Run the TUI for a PRD (default: main)
chief new [name]              Create a new PRD interactively
chief edit [name]             Edit an existing PRD interactively
chief status [name]           Show progress for a PRD
chief list                    List all PRDs with progress
chief resume [name] [story]   Resume a completed story's Claude session
chief review [name]           Audit implementation vs PRD acceptance criteria
```

**Generate PRDs from descriptions or voice:**
```
chief prd "description"       Auto-research codebase + generate PRD
chief prd --voice             Multi-round voice input → research → PRD
chief prd --no-research       Skip research pass
chief prd --type feature      Force PRD type (bug-fix, feature, exploration, ...)
chief prd --list-types        Show all supported PRD types
```

**Backlog issue tracker:**
```
chief backlog <ISSUE-KEY>     Fetch issue, read repo, generate fix PRD
chief backlog --batch         Fetch all open issues, group related ones, batch PRD
```
Requires `BACKLOG_API_KEY` env var and `.chief/config.yaml`:
```yaml
project:
  backlogBase: https://yourorg.backlog.com
  backlogProject: MYPROJECT
  notifyUserId: "123456"
```

**Repo setup and validation:**
```
chief setup                   Study repo, produce .chief/config.yaml
chief setup --docs <url>      Feed extra docs/API spec to the setup
chief validate                Run API tests → auto-fix → push → wait for CI
chief validate --fix          Auto-fix test failures with Claude
chief validate --fix --push   Fix + push + wait for CI pipeline
chief validate --init         Write a blank config template
```

**Multi-service orchestration:**
```
chief orchestrate             Monitor + auto-fix multiple services in parallel
chief orchestrate --cycles N  Run N fix cycles
```

**Global flags:**
```
--agent claude|codex|opencode|cursor   Choose the agent backend
--agent-path <path>                    Custom path to agent binary
--add-dir <path>                       Expose extra directory to the agent
--max-iterations N, -n N               Max iterations per session
--no-retry                             Disable auto-retry on crashes
--verbose                              Show raw agent output
--version                              Show version
--help                                 Show help
```

Chief runs Claude in a [Ralph Wiggum loop](https://ghuntley.com/ralph/): each iteration starts with a fresh context window, but progress is persisted between runs. This lets Claude work through large projects without hitting context limits.

## How It Works

1. **Describe your project** as a series of tasks (or use `chief prd` to generate them)
2. **Chief runs Claude** in a loop, one task at a time
3. **One commit per task** — clean git history, easy to review

See the [documentation](https://minicodemonkey.github.io/chief/concepts/how-it-works) for details.

## Requirements

- **[Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code)**, **[Codex CLI](https://developers.openai.com/codex/cli/reference)**, or **[OpenCode CLI](https://opencode.ai)** installed and authenticated

Use Claude by default, or configure Codex or OpenCode in `.chief/config.yaml`:

```yaml
agent:
  provider: opencode
  cliPath: /usr/local/bin/opencode   # optional
```

Or run with `chief --agent opencode` or set `CHIEF_AGENT=opencode`.

## License

MIT

## Acknowledgments

- [@Simon-BEE](https://github.com/Simon-BEE) — Multi-agent architecture and Codex CLI integration
- [@tpaulshippy](https://github.com/tpaulshippy) — OpenCode CLI support and NDJSON parser
- [snarktank/ralph](https://github.com/snarktank/ralph) — The original Ralph implementation that inspired this project
- [Geoffrey Huntley](https://ghuntley.com/ralph/) — For coining the "Ralph Wiggum loop" pattern
- [Bubble Tea](https://github.com/charmbracelet/bubbletea) — TUI framework
- [Lip Gloss](https://github.com/charmbracelet/lipgloss) — Terminal styling
