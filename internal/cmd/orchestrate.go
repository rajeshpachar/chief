package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/minicodemonkey/chief/internal/config"
)

// OrchestrateOptions configures a multi-service orchestration run.
type OrchestrateOptions struct {
	// BaseDir is the repo root (default: cwd)
	BaseDir string

	// CLIPath is the Claude binary (default: "claude")
	CLIPath string

	// MaxCycles is how many coordinator→fix cycles to run before giving up (default: 5)
	MaxCycles int

	// DryRun prints what would happen without calling Claude
	DryRun bool

	// Services overrides config.yaml services (for testing)
	Services []config.ServiceConfig
}

// ServiceHealth is the status of one service after a check cycle.
type ServiceHealth struct {
	Name    string
	Healthy bool
	// Last few log lines (tail only — truncated for token efficiency)
	LogTail string
	// ERROR/EXCEPTION lines extracted from recent log
	Errors []string
	// Output of HealthCheck command (if configured)
	CheckOutput string
	// Fix applied this cycle (if any)
	FixApplied string
}

// OrchestrationResult summarises the outcome of RunOrchestrate.
type OrchestrationResult struct {
	Cycles       int
	AllHealthy   bool
	ServiceNames []string
	FinalHealth  map[string]ServiceHealth
}

// RunOrchestrate runs a multi-service orchestration loop:
//  1. Read service configs from .chief/config.yaml (or opts.Services)
//  2. Start any unstarted services
//  3. Poll each service: tail log + run health check
//  4. For unhealthy services, spawn a service agent (token-efficient: log tail + code only)
//  5. Coordinator agent reads JSON health summaries → decides next action
//  6. Repeat until all healthy or MaxCycles reached
func RunOrchestrate(opts OrchestrateOptions) (*OrchestrationResult, error) {
	if opts.BaseDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get cwd: %w", err)
		}
		opts.BaseDir = cwd
	}
	if opts.CLIPath == "" {
		opts.CLIPath = "claude"
	}
	if opts.MaxCycles <= 0 {
		opts.MaxCycles = 5
	}

	// Load services from config if not provided directly
	services := opts.Services
	if len(services) == 0 {
		cfg, err := config.Load(opts.BaseDir)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		services = cfg.Project.Services
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("no services configured — add project.services to .chief/config.yaml")
	}

	// Validate service names: must be non-empty and unique
	seen := map[string]bool{}
	for _, svc := range services {
		if svc.Name == "" {
			return nil, fmt.Errorf("all services must have a name — check project.services in .chief/config.yaml")
		}
		if seen[svc.Name] {
			return nil, fmt.Errorf("duplicate service name %q — service names must be unique", svc.Name)
		}
		seen[svc.Name] = true
	}

	// Ensure logs directory exists
	logsDir := filepath.Join(opts.BaseDir, ".chief", "orchestration")
	os.MkdirAll(logsDir, 0755)

	fmt.Printf("Orchestrating %d service(s): ", len(services))
	names := make([]string, len(services))
	for i, s := range services {
		names[i] = s.Name
	}
	fmt.Println(strings.Join(names, ", "))
	fmt.Println()

	result := &OrchestrationResult{
		ServiceNames: names,
		FinalHealth:  make(map[string]ServiceHealth),
	}

	for cycle := 1; cycle <= opts.MaxCycles; cycle++ {
		fmt.Printf("── Cycle %d/%d ────────────────────────────────────────\n", cycle, opts.MaxCycles)

		// Step 1: Poll all services in parallel
		health := pollAllServices(services, opts.BaseDir)
		printHealthSummary(health)

		// Step 2: Check if all healthy
		allHealthy := true
		var unhealthy []ServiceHealth
		for _, h := range health {
			if !h.Healthy {
				allHealthy = false
				unhealthy = append(unhealthy, h)
			}
		}

		if allHealthy {
			fmt.Println("\nAll services healthy.")
			result.Cycles = cycle
			result.AllHealthy = true
			result.FinalHealth = toHealthMap(health)
			return result, nil
		}

		fmt.Printf("\n%d service(s) unhealthy: ", len(unhealthy))
		for _, u := range unhealthy {
			fmt.Printf("%s ", u.Name)
		}
		fmt.Println()

		if opts.DryRun {
			fmt.Println("[dry-run] would spawn service agents and coordinator")
			break
		}

		// Step 3: Coordinator decides what to fix
		action, err := runCoordinatorAgent(opts.CLIPath, opts.BaseDir, health, cycle, opts.MaxCycles)
		if err != nil {
			fmt.Printf("Warning: coordinator agent failed: %v\n", err)
		} else {
			fmt.Printf("\nCoordinator: %s\n\n", action.Summary)
			if action.Abort {
				fmt.Println("Coordinator: aborting orchestration — cannot proceed.")
				break
			}
		}

		// Step 4: Spawn service agents in parallel for unhealthy services
		var wg sync.WaitGroup
		fixes := make(map[string]string, len(unhealthy))
		var fixMu sync.Mutex

		for _, svc := range unhealthy {
			// Find service config
			var scfg config.ServiceConfig
			for _, s := range services {
				if s.Name == svc.Name {
					scfg = s
					break
				}
			}

			wg.Add(1)
			go func(h ServiceHealth, sc config.ServiceConfig) {
				defer wg.Done()
				fix, ferr := runServiceAgent(opts.CLIPath, opts.BaseDir, h, sc, logsDir)
				fixMu.Lock()
				defer fixMu.Unlock()
				if ferr != nil {
					fixes[h.Name] = fmt.Sprintf("agent error: %v", ferr)
				} else {
					fixes[h.Name] = fix
				}
			}(svc, scfg)
		}
		wg.Wait()

		// Print what each service agent did
		fmt.Println("── Service agent results ─────────────────────────────")
		for name, fix := range fixes {
			preview := fix
			if len(preview) > 200 {
				preview = preview[:200] + "..."
			}
			fmt.Printf("  [%s] %s\n", name, preview)
		}
		fmt.Println()

		// Brief pause before next poll
		time.Sleep(3 * time.Second)
	}

	// Final health check
	finalHealth := pollAllServices(services, opts.BaseDir)
	allHealthy := true
	for _, h := range finalHealth {
		if !h.Healthy {
			allHealthy = false
		}
	}

	result.Cycles = opts.MaxCycles
	result.AllHealthy = allHealthy
	result.FinalHealth = toHealthMap(finalHealth)

	if !allHealthy {
		fmt.Printf("\nOrchestration finished after %d cycle(s) — not all services healthy.\n", opts.MaxCycles)
		fmt.Printf("Check logs in: %s\n", logsDir)
	}
	return result, nil
}

// pollAllServices checks all services concurrently, returning health status for each.
func pollAllServices(services []config.ServiceConfig, baseDir string) []ServiceHealth {
	results := make([]ServiceHealth, len(services))
	var wg sync.WaitGroup
	for i, svc := range services {
		wg.Add(1)
		go func(idx int, s config.ServiceConfig) {
			defer wg.Done()
			results[idx] = pollService(s, baseDir)
		}(i, svc)
	}
	wg.Wait()
	return results
}

// pollService reads a service's log, extracts errors, and runs its health check.
// Token budget: tail is capped at logTailLines (default 50); errors are always included.
func pollService(svc config.ServiceConfig, baseDir string) ServiceHealth {
	h := ServiceHealth{Name: svc.Name, Healthy: true}

	tailLines := svc.LogTailLines
	if tailLines <= 0 {
		tailLines = 50
	}

	// Resolve log file path
	logFile := svc.LogFile
	if logFile == "" {
		logFile = filepath.Join("logs", svc.Name+".log")
	}
	if !filepath.IsAbs(logFile) {
		logFile = filepath.Join(baseDir, logFile)
	}

	// Read log tail
	tail, errors := readLogTail(logFile, tailLines)
	h.LogTail = tail
	h.Errors = errors
	if len(errors) > 0 {
		h.Healthy = false
	}

	// Run health check command
	if svc.HealthCheck != "" {
		out, err := exec.Command("sh", "-c", svc.HealthCheck).CombinedOutput()
		h.CheckOutput = strings.TrimSpace(string(out))
		if err != nil {
			h.Healthy = false
		}
	}

	return h
}

// readLogTail reads the last n lines of a log file and extracts ERROR/EXCEPTION lines.
// Returns: (tail string, error lines slice).
// If file doesn't exist, returns empty (service may not have started yet).
func readLogTail(path string, n int) (tail string, errors []string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}

	lines := strings.Split(string(data), "\n")
	// Remove trailing empty line
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	// Tail last n lines
	start := len(lines) - n
	if start < 0 {
		start = 0
	}
	tail = strings.Join(lines[start:], "\n")

	// Extract error lines from full log (not just tail) — last 20 only
	var errLines []string
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "error") ||
			strings.Contains(lower, "exception") ||
			strings.Contains(lower, "traceback") ||
			strings.Contains(lower, "fatal") ||
			strings.Contains(lower, "panic") {
			errLines = append(errLines, line)
		}
	}
	if len(errLines) > 20 {
		errLines = errLines[len(errLines)-20:]
	}
	return tail, errLines
}

// coordinatorAction is the structured response from the coordinator agent.
type coordinatorAction struct {
	// Summary is a one-sentence description of what the coordinator decided
	Summary string `json:"summary"`
	// ServicesToFix lists service names that need a fix agent spawned
	ServicesToFix []string `json:"services_to_fix"`
	// Abort signals that the coordinator cannot make progress and gives up
	Abort bool `json:"abort"`
}

// runCoordinatorAgent calls Claude with a compact health JSON and returns a fix decision.
// Token budget: health JSON only (no logs, no code) — coordinator never reads source.
func runCoordinatorAgent(cliPath, baseDir string, health []ServiceHealth, cycle, maxCycles int) (*coordinatorAction, error) {
	// Build compact health JSON — NO log content, just status + error count
	type compactStatus struct {
		Name       string `json:"name"`
		Healthy    bool   `json:"healthy"`
		ErrorCount int    `json:"error_count"`
		// Last error line only (not full log)
		LastError   string `json:"last_error,omitempty"`
		CheckOutput string `json:"check_output,omitempty"`
	}
	statuses := make([]compactStatus, len(health))
	for i, h := range health {
		lastErr := ""
		if len(h.Errors) > 0 {
			lastErr = h.Errors[len(h.Errors)-1]
		}
		statuses[i] = compactStatus{
			Name:        h.Name,
			Healthy:     h.Healthy,
			ErrorCount:  len(h.Errors),
			LastError:   lastErr,
			CheckOutput: h.CheckOutput,
		}
	}
	statusJSON, _ := json.MarshalIndent(statuses, "", "  ")

	prompt := fmt.Sprintf(`You are the coordinator of a multi-service system. Cycle %d of %d.

Service health (compact summary — no logs):
%s

Decide what to do next. Respond with ONLY valid JSON matching this schema:
{
  "summary": "<one sentence: what you decided and why>",
  "services_to_fix": ["<name>", ...],
  "abort": false
}

Rules:
- services_to_fix: list the names of services that need a fix agent
- If a service is healthy, do NOT include it
- If the same service has failed 3+ consecutive cycles, set abort=true
- Keep summary to one sentence
- Output ONLY the JSON object, nothing else`, cycle, maxCycles, string(statusJSON))

	out, err := runClaudeNonInteractive(cliPath, baseDir, prompt)
	if err != nil {
		return &coordinatorAction{Summary: "coordinator unavailable", ServicesToFix: unhealthyNames(health)}, err
	}

	// Extract JSON from output (Claude may wrap it in markdown)
	jsonStr := extractJSON(out)
	var action coordinatorAction
	if err := json.Unmarshal([]byte(jsonStr), &action); err != nil {
		// Fallback: fix all unhealthy services
		return &coordinatorAction{
			Summary:       out,
			ServicesToFix: unhealthyNames(health),
		}, nil
	}
	return &action, nil
}

// runServiceAgent spawns a Claude agent for a single unhealthy service.
// Token budget: log tail (capped) + error lines + service code directory only.
func runServiceAgent(cliPath, baseDir string, h ServiceHealth, svc config.ServiceConfig, logsDir string) (string, error) {
	// Build token-efficient context
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Service: %s\n\n", h.Name))

	// Error lines first — most important, always included
	if len(h.Errors) > 0 {
		sb.WriteString("## Recent errors (from log)\n")
		for _, e := range h.Errors {
			sb.WriteString(e + "\n")
		}
		sb.WriteString("\n")
	}

	// Log tail — capped at 50 lines
	if h.LogTail != "" {
		sb.WriteString("## Recent log (last 50 lines)\n")
		sb.WriteString(h.LogTail)
		sb.WriteString("\n\n")
	}

	// Health check output
	if h.CheckOutput != "" {
		sb.WriteString("## Health check output\n")
		sb.WriteString(h.CheckOutput)
		sb.WriteString("\n\n")
	}

	// Code dir hint — agent can read it using its tools
	codeDir := svc.CodeDir
	if codeDir == "" {
		codeDir = svc.Name // try service name as directory
	}
	if codeDir != "" {
		sb.WriteString(fmt.Sprintf("## Service code directory\n%s/\n\n", filepath.Join(baseDir, codeDir)))
	}

	prompt := fmt.Sprintf(`You are a service repair agent for service "%s".
Your job: diagnose why this service is failing and fix it.

%s

Instructions:
1. Read the error messages and log above carefully
2. Use Read/Grep/Glob to explore the code in the service directory
3. Identify the root cause
4. Apply a minimal targeted fix
5. Verify the fix doesn't break other functionality
6. Respond with a one-paragraph summary of: root cause + fix applied

Rules:
- Fix ONLY what is causing the errors shown
- Do NOT refactor or improve other code
- Do NOT change API contracts or function signatures
- If the root cause requires info you don't have, state what's missing
- Keep your response concise — max 300 words`, h.Name, sb.String())

	out, err := runClaudeNonInteractive(cliPath, baseDir, prompt)
	if err != nil {
		return "", fmt.Errorf("service agent for %s: %w", h.Name, err)
	}

	// Save agent output to orchestration log
	logPath := filepath.Join(logsDir, fmt.Sprintf("%s-agent-%d.md", h.Name, time.Now().Unix()))
	os.WriteFile(logPath, []byte(out), 0644)

	return out, nil
}

// extractJSON pulls the first JSON object out of a string that may contain markdown.
func extractJSON(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start == -1 || end == -1 || end <= start {
		return s
	}
	return s[start : end+1]
}

// unhealthyNames returns the names of all unhealthy services.
func unhealthyNames(health []ServiceHealth) []string {
	var names []string
	for _, h := range health {
		if !h.Healthy {
			names = append(names, h.Name)
		}
	}
	return names
}

// toHealthMap converts a slice of ServiceHealth to a name-keyed map.
func toHealthMap(health []ServiceHealth) map[string]ServiceHealth {
	m := make(map[string]ServiceHealth, len(health))
	for _, h := range health {
		m[h.Name] = h
	}
	return m
}

// printHealthSummary prints a compact status table to stdout.
func printHealthSummary(health []ServiceHealth) {
	for _, h := range health {
		status := "✓ healthy"
		if !h.Healthy {
			status = fmt.Sprintf("✗ unhealthy (%d error lines)", len(h.Errors))
		}
		fmt.Printf("  %-20s %s\n", h.Name, status)
	}
}

// buildOrchestratePrompt builds the research prompt for understanding a multi-service system.
// Used by: chief prd --type exploration when the request mentions multiple services.
func buildOrchestratePrompt(services []config.ServiceConfig, goal string) string {
	var names []string
	for _, s := range services {
		names = append(names, s.Name)
	}
	return fmt.Sprintf(`Multi-service orchestration research for: %q

Services: %s

For each service, identify:
1. What it does and what other services it depends on
2. Common failure modes (from log patterns)
3. Health check strategy
4. Fix scope (what can be fixed without touching other services)

Then describe the orchestration topology: which services are upstream/downstream,
what breaks if each one fails, and recommended fix order.`,
		goal, strings.Join(names, ", "))
}

// OrchestratePromptFromConfig generates an orchestrate prompt from config.
// Exported for use in PRD generation.
func OrchestratePromptFromConfig(baseDir, goal string) string {
	cfg, err := config.Load(baseDir)
	if err != nil || len(cfg.Project.Services) == 0 {
		return ""
	}
	return buildOrchestratePrompt(cfg.Project.Services, goal)
}
