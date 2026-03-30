package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"

	"github.com/minicodemonkey/chief/internal/loop"
)

// ClaudeProvider implements loop.Provider for the Claude Code CLI.
type ClaudeProvider struct {
	cliPath         string
	useSubscription bool // when true, ANTHROPIC_API_KEY is stripped from child process env
}

// NewClaudeProvider returns a Provider for the Claude CLI.
// If cliPath is empty, "claude" is used.
func NewClaudeProvider(cliPath string) *ClaudeProvider {
	if cliPath == "" {
		cliPath = "claude"
	}
	return &ClaudeProvider{cliPath: cliPath}
}

// NewClaudeProviderWithOptions returns a Provider with extra options.
func NewClaudeProviderWithOptions(cliPath string, useSubscription bool) *ClaudeProvider {
	p := NewClaudeProvider(cliPath)
	p.useSubscription = useSubscription
	return p
}

// Name implements loop.Provider.
func (p *ClaudeProvider) Name() string { return "Claude" }

// CLIPath implements loop.Provider.
func (p *ClaudeProvider) CLIPath() string { return p.cliPath }

// LoopCommand implements loop.Provider.
func (p *ClaudeProvider) LoopCommand(ctx context.Context, prompt, workDir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, p.cliPath,
		"--dangerously-skip-permissions",
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
	)
	cmd.Dir = workDir
	if p.useSubscription {
		cmd.Env = stripAPIKey(os.Environ())
	}
	return cmd
}

// InteractiveCommand implements loop.Provider.
func (p *ClaudeProvider) InteractiveCommand(workDir, prompt string) *exec.Cmd {
	cmd := exec.Command(p.cliPath, prompt)
	cmd.Dir = workDir
	if p.useSubscription {
		cmd.Env = stripAPIKey(os.Environ())
	}
	return cmd
}

// ParseLine implements loop.Provider.
func (p *ClaudeProvider) ParseLine(line string) *loop.Event {
	return loop.ParseLine(line)
}

// LogFileName implements loop.Provider.
func (p *ClaudeProvider) LogFileName() string { return "claude.log" }

// CleanOutput implements loop.Provider - Claude doesn't use a special format.
func (p *ClaudeProvider) CleanOutput(output string) string { return output }

// stripAPIKey returns a copy of env with ANTHROPIC_API_KEY removed.
// This forces Claude CLI to use claude.ai subscription auth instead of API key billing.
func stripAPIKey(env []string) []string {
	result := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "ANTHROPIC_API_KEY=") {
			result = append(result, e)
		}
	}
	return result
}

// ClaudeAuthStatus holds the parsed result of `claude auth status --json`.
type ClaudeAuthStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`       // "claude.ai" | "apiKey"
	APIKeySource     string `json:"apiKeySource"`     // "ANTHROPIC_API_KEY" | ""
	SubscriptionType string `json:"subscriptionType"` // "max" | "pro" | ""
}

// AuthStatus runs `claude auth status --json` and returns the parsed result.
// When useSub is true, ANTHROPIC_API_KEY is stripped from the environment so the
// check reflects what Claude actually sees when the agent loop runs.
// Returns nil on any error (auth check is best-effort).
func AuthStatus(cliPath string, useSub bool) *ClaudeAuthStatus {
	if cliPath == "" {
		cliPath = "claude"
	}
	env := os.Environ()
	if useSub {
		env = stripAPIKey(env)
	}

	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command(cliPath, args...)
		cmd.Env = env
		return cmd.Output()
	}

	out, err := run("auth", "status", "--json")
	if err != nil {
		// Fallback for older Claude CLI versions without --json
		out, err = run("auth", "status")
		if err != nil {
			return nil
		}
	}
	var status ClaudeAuthStatus
	if err := json.Unmarshal(out, &status); err != nil {
		return nil
	}
	return &status
}

// AuthLabel returns a short human-readable label for the header, e.g. "claude.ai" or "API key".
func (s *ClaudeAuthStatus) AuthLabel() string {
	if s == nil {
		return ""
	}
	if !s.LoggedIn {
		return "not logged in"
	}
	if s.AuthMethod == "claude.ai" && s.APIKeySource == "" {
		t := s.SubscriptionType
		if t != "" {
			return "claude.ai (" + t + ")"
		}
		return "claude.ai"
	}
	if s.APIKeySource == "ANTHROPIC_API_KEY" {
		return "API key"
	}
	if s.AuthMethod == "claude.ai" {
		return "claude.ai + API key" // logged in but env key overrides
	}
	return s.AuthMethod
}
