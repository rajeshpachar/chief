package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/minicodemonkey/chief/internal/agent"
	"github.com/minicodemonkey/chief/internal/cmd"
	"github.com/minicodemonkey/chief/internal/config"
	"github.com/minicodemonkey/chief/internal/git"
	"github.com/minicodemonkey/chief/internal/loop"
	"github.com/minicodemonkey/chief/internal/prd"
	"github.com/minicodemonkey/chief/internal/tui"
)

// Version is set at build time via ldflags
var Version = "dev"

// TUIOptions holds the parsed command-line options for the TUI
type TUIOptions struct {
	PRDPath       string
	MaxIterations int
	Verbose       bool
	Merge         bool
	Force         bool
	NoRetry       bool
	Agent         string   // --agent claude|codex|opencode|cursor
	AgentPath     string   // --agent-path
	AddDirs       []string // --add-dir (repeatable)
}

func main() {
	// Handle subcommands first
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "new":
			runNew()
			return
		case "edit":
			runEdit()
			return
		case "status":
			runStatus()
			return
		case "list":
			runList()
			return
		case "help":
			printHelp()
			return
		case "--help", "-h":
			printHelp()
			return
		case "--version", "-v":
			fmt.Printf("chief version %s\n", Version)
			return
		case "resume":
			runResume()
			return
		case "prd":
			runGenerate()
			return
		case "orchestrate":
			runOrchestrate()
			return
		case "validate":
			runValidate()
			return
		case "setup":
			runSetup()
			return
		case "backlog":
			runBacklog()
			return
		case "review":
			runReview()
			return
		case "login":
			runLogin()
			return
		case "auth":
			runAuthStatus()
			return
		case "update":
			runUpdate()
			return
		case "voice-capture":
			runVoiceCapture()
			return
		case "wiggum":
			printWiggum()
			return
		}
	}

	// Non-blocking version check on startup (for interactive TUI sessions)
	cmd.CheckVersionOnStartup(Version)

	// Parse flags for TUI mode
	opts := parseTUIFlags()

	// Handle special flags that were parsed
	if opts == nil {
		// Already handled (--help or --version)
		return
	}

	// Run the TUI
	runTUIWithOptions(opts)
}

// findAvailablePRD looks for any available PRD in .chief/prds/
// Returns the path to the first PRD found, or empty string if none exist.
func findAvailablePRD() string {
	prdsDir := ".chief/prds"
	entries, err := os.ReadDir(prdsDir)
	if err != nil {
		return ""
	}

	for _, entry := range entries {
		if entry.IsDir() {
			prdPath := filepath.Join(prdsDir, entry.Name(), "prd.md")
			if _, err := os.Stat(prdPath); err == nil {
				return prdPath
			}
		}
	}
	return ""
}

// listAvailablePRDs returns all PRD names in .chief/prds/
func listAvailablePRDs() []string {
	prdsDir := ".chief/prds"
	entries, err := os.ReadDir(prdsDir)
	if err != nil {
		return nil
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			prdPath := filepath.Join(prdsDir, entry.Name(), "prd.md")
			if _, err := os.Stat(prdPath); err == nil {
				names = append(names, entry.Name())
			}
		}
	}
	return names
}

// parseAgentFlags extracts --agent and --agent-path from args[startIdx:],
// returning the agent name, agent path, remaining args (with agent flags removed),
// and the updated index offsets. It exits on missing values.
func parseAgentFlags(args []string, startIdx int) (agentName, agentPath string, remaining []string) {
	for i := startIdx; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--agent":
			if i+1 < len(args) {
				i++
				agentName = args[i]
			} else {
				fmt.Fprintf(os.Stderr, "Error: --agent requires a value (claude, codex, opencode, or cursor)\n")
				os.Exit(1)
			}
		case strings.HasPrefix(arg, "--agent="):
			agentName = strings.TrimPrefix(arg, "--agent=")
		case arg == "--agent-path":
			if i+1 < len(args) {
				i++
				agentPath = args[i]
			} else {
				fmt.Fprintf(os.Stderr, "Error: --agent-path requires a value\n")
				os.Exit(1)
			}
		case strings.HasPrefix(arg, "--agent-path="):
			agentPath = strings.TrimPrefix(arg, "--agent-path=")
		default:
			remaining = append(remaining, arg)
		}
	}
	return
}

// parseTUIFlags parses command-line flags for TUI mode
func parseTUIFlags() *TUIOptions {
	opts := &TUIOptions{
		PRDPath:       "", // Will be resolved later
		MaxIterations: 0,  // 0 signals dynamic calculation (remaining stories + 5)
		Verbose:       false,
		Merge:         false,
		Force:         false,
		NoRetry:       false,
	}

	// Pre-extract agent flags so they don't interfere with positional arg parsing
	opts.Agent, opts.AgentPath, _ = parseAgentFlags(os.Args, 1)

	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]

		switch {
		case arg == "--help" || arg == "-h":
			printHelp()
			return nil
		case arg == "--version" || arg == "-v":
			fmt.Printf("chief version %s\n", Version)
			return nil
		case arg == "--verbose":
			opts.Verbose = true
		case arg == "--merge":
			opts.Merge = true
		case arg == "--force":
			opts.Force = true
		case arg == "--no-retry":
			opts.NoRetry = true
		case arg == "--add-dir":
			if i+1 < len(os.Args) {
				i++
				opts.AddDirs = append(opts.AddDirs, os.Args[i])
			} else {
				fmt.Fprintf(os.Stderr, "Error: --add-dir requires a value\n")
				os.Exit(1)
			}
		case strings.HasPrefix(arg, "--add-dir="):
			opts.AddDirs = append(opts.AddDirs, strings.TrimPrefix(arg, "--add-dir="))
		case arg == "--agent" || arg == "--agent-path":
			i++ // skip value (already parsed by parseAgentFlags)
		case strings.HasPrefix(arg, "--agent=") || strings.HasPrefix(arg, "--agent-path="):
			// already parsed by parseAgentFlags
		case arg == "--max-iterations" || arg == "-n":
			// Next argument should be the number
			if i+1 < len(os.Args) {
				i++
				n, err := strconv.Atoi(os.Args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: invalid value for %s: %s\n", arg, os.Args[i])
					os.Exit(1)
				}
				if n < 1 {
					fmt.Fprintf(os.Stderr, "Error: --max-iterations must be at least 1\n")
					os.Exit(1)
				}
				opts.MaxIterations = n
			} else {
				fmt.Fprintf(os.Stderr, "Error: %s requires a value\n", arg)
				os.Exit(1)
			}
		case strings.HasPrefix(arg, "--max-iterations="):
			val := strings.TrimPrefix(arg, "--max-iterations=")
			n, err := strconv.Atoi(val)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid value for --max-iterations: %s\n", val)
				os.Exit(1)
			}
			if n < 1 {
				fmt.Fprintf(os.Stderr, "Error: --max-iterations must be at least 1\n")
				os.Exit(1)
			}
			opts.MaxIterations = n
		case strings.HasPrefix(arg, "-n="):
			val := strings.TrimPrefix(arg, "-n=")
			n, err := strconv.Atoi(val)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid value for -n: %s\n", val)
				os.Exit(1)
			}
			if n < 1 {
				fmt.Fprintf(os.Stderr, "Error: -n must be at least 1\n")
				os.Exit(1)
			}
			opts.MaxIterations = n
		case strings.HasPrefix(arg, "-"):
			// Unknown flag
			fmt.Fprintf(os.Stderr, "Error: unknown flag: %s\n", arg)
			fmt.Fprintf(os.Stderr, "Run 'chief --help' for usage.\n")
			os.Exit(1)
		default:
			// Positional argument: PRD name or path
			if strings.HasSuffix(arg, ".md") || strings.HasSuffix(arg, ".json") || strings.HasSuffix(arg, "/") {
				opts.PRDPath = arg
			} else {
				// Treat as PRD name
				opts.PRDPath = fmt.Sprintf(".chief/prds/%s/prd.md", arg)
			}
		}
	}

	return opts
}

func runNew() {
	opts := cmd.NewOptions{}

	// Parse arguments: chief new [name] [context...] [--agent X] [--agent-path X]
	flagAgent, flagPath, positional := parseAgentFlags(os.Args, 2)
	// Filter out remaining flags, keep only positional args
	var args []string
	for _, a := range positional {
		if !strings.HasPrefix(a, "-") {
			args = append(args, a)
		}
	}
	if len(args) > 0 {
		opts.Name = args[0]
	}
	if len(args) > 1 {
		opts.Context = strings.Join(args[1:], " ")
	}

	opts.Provider = resolveProvider(flagAgent, flagPath)
	if err := cmd.RunNew(opts); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runEdit() {
	opts := cmd.EditOptions{}

	// Parse arguments: chief edit [name] [--agent X] [--agent-path X]
	flagAgent, flagPath, remaining := parseAgentFlags(os.Args, 2)
	for _, arg := range remaining {
		if opts.Name == "" && !strings.HasPrefix(arg, "-") {
			opts.Name = arg
		}
	}

	opts.Provider = resolveProvider(flagAgent, flagPath)
	if err := cmd.RunEdit(opts); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runStatus() {
	opts := cmd.StatusOptions{}

	// Parse arguments: chief status [name]
	if len(os.Args) > 2 && !strings.HasPrefix(os.Args[2], "-") {
		opts.Name = os.Args[2]
	}

	if err := cmd.RunStatus(opts); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runResume() {
	opts := cmd.ResumeOptions{}
	provider := resolveProvider("", "")
	opts.CLIPath = provider.CLIPath()

	// Parse: chief resume [name] [story-id]
	args := os.Args[2:]
	switch len(args) {
	case 0:
		// chief resume — use default PRD, list sessions
	case 1:
		// chief resume US-003 — default PRD, specific story
		// OR chief resume auth — named PRD, list sessions
		if isValidStoryID(args[0]) {
			opts.StoryID = args[0]
		} else {
			opts.Name = args[0]
		}
	case 2:
		// chief resume auth US-003
		opts.Name = args[0]
		opts.StoryID = args[1]
	}

	if err := cmd.RunResume(opts); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// isValidStoryID returns true if the string looks like a story ID (e.g. "US-003", "FIX-01").
func isValidStoryID(s string) bool {
	return len(s) > 2 && strings.Contains(s, "-")
}

func runGenerate() {
	opts := cmd.GenerateOptions{}

	// Parse: chief prd [flags] [description...]
	// Flags: --voice, --type <type>, --no-research, --duration <sec>, --voice-backend <backend>
	var descParts []string
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--voice":
			opts.Voice = true
		case "--no-research":
			opts.SkipResearch = true
		case "--type":
			if i+1 < len(args) {
				i++
				opts.ForcedType = args[i]
			}
		case "--duration":
			if i+1 < len(args) {
				i++
				fmt.Sscanf(args[i], "%d", &opts.VoiceDuration)
			}
		case "--voice-backend":
			if i+1 < len(args) {
				i++
				opts.VoiceBackend = args[i]
			}
		case "--list-types":
			printPRDTypes()
			return
		// Legacy flags — silently accepted for compatibility
		case "--confirm", "--research":
		default:
			if !strings.HasPrefix(args[i], "-") {
				descParts = append(descParts, args[i])
			}
		}
	}
	opts.Description = strings.Join(descParts, " ")

	provider := resolveProvider("", "")
	opts.CLIPath = provider.CLIPath()

	if err := cmd.RunGenerate(opts); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func printPRDTypes() {
	fmt.Println(`chief prd — available types (use --type <name> to force):

  bug-fix        Fix a reported issue, regression, or wrong behavior
  feature        Build new functionality that does not exist yet
  test-coverage  Write missing tests, find coverage gaps
  robustness     Harden existing feature: error handling, edge cases, retries
  exploration    Understand/map/document how something works, find gaps
  server-debug   Something broken on staging/prod: investigate and fix
  refactor       Improve code structure without changing external behavior
  security       Find vulnerabilities, fix critical ones, audit auth/inputs
  perf           Profile, find bottleneck, optimize
  migration      Move from old pattern/library to new one
  review         Post-implementation audit: architecture, tests, design
  batch-fix      Process multiple issues in one cycle`)
}

func runValidate() {
	opts := cmd.ValidateOptions{}
	initConfig := false
	stagingURL := ""

	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--fix":
			opts.Fix = true
		case "--push":
			opts.Push = true
		case "--fix-push":
		case "--cycles":
			if i+1 < len(args) {
				i++
				fmt.Sscanf(args[i], "%d", &opts.MaxCycles)
			}
		case "--test-cmd":
			if i+1 < len(args) {
				i++
				opts.LocalTestCmd = args[i]
			}
		case "--init":
			initConfig = true
		case "--staging-url":
			if i+1 < len(args) {
				i++
				stagingURL = args[i]
			}
		}
	}

	if initConfig {
		cwd, _ := os.Getwd()
		if err := cmd.WriteValidateConfig(cwd, stagingURL); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Created .chief/config.yaml with validation section.")
		fmt.Println("Edit it to add your API tests, then run: chief validate")
		return
	}

	provider := resolveProvider("", "")
	opts.CLIPath = provider.CLIPath()

	if err := cmd.RunValidate(opts); err != nil {
		fmt.Fprintf(os.Stderr, "Validation failed: %v\n", err)
		os.Exit(1)
	}
}

func runOrchestrate() {
	opts := cmd.OrchestrateOptions{}

	// Parse: chief orchestrate [flags]
	// Flags: --cycles N, --dry-run
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--cycles":
			if i+1 < len(args) {
				i++
				fmt.Sscanf(args[i], "%d", &opts.MaxCycles)
			}
		case "--dry-run":
			opts.DryRun = true
		}
	}

	provider := resolveProvider("", "")
	opts.CLIPath = provider.CLIPath()

	result, err := cmd.RunOrchestrate(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if result.AllHealthy {
		fmt.Printf("All services healthy after %d cycle(s).\n", result.Cycles)
	} else {
		fmt.Printf("Orchestration complete (%d cycles). Not all services healthy.\n", result.Cycles)
		os.Exit(1)
	}
}

func runSetup() {
	opts := cmd.SetupOptions{}

	// Parse: chief setup [flags]
	// Flags: --force, --docs <url> (repeatable)
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--force":
			opts.Force = true
		case "--docs":
			if i+1 < len(args) {
				i++
				opts.DocsURLs = append(opts.DocsURLs, args[i])
			} else {
				fmt.Fprintf(os.Stderr, "Error: --docs requires a URL\n")
				os.Exit(1)
			}
		default:
			if strings.HasPrefix(args[i], "--docs=") {
				opts.DocsURLs = append(opts.DocsURLs, strings.TrimPrefix(args[i], "--docs="))
			}
		}
	}

	provider := resolveProvider("", "")
	opts.CLIPath = provider.CLIPath()

	// Respect the useSubscription setting so chief setup works with claude.ai plans.
	cwd, _ := os.Getwd()
	if cfg, err := config.Load(cwd); err == nil {
		opts.UseSubscription = cfg.Agent.UseSubscriptionEnabled()
	} else {
		opts.UseSubscription = true // safe default: prefer subscription billing
	}

	if _, err := cmd.RunSetup(opts); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runBacklog() {
	opts := cmd.BacklogOptions{}

	// Parse: chief backlog [ISSUE-KEY] [--batch] [--force]
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--batch":
			opts.Batch = true
		case "--force":
			opts.Force = true
		default:
			if !strings.HasPrefix(args[i], "-") && opts.IssueKey == "" {
				opts.IssueKey = args[i]
			}
		}
	}

	provider := resolveProvider("", "")
	opts.CLIPath = provider.CLIPath()

	if _, err := cmd.RunBacklog(opts); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runLogin() {
	provider := resolveProvider("", "")
	c := exec.Command(provider.CLIPath(), "auth", "login")
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Login failed: %v\n", err)
		os.Exit(1)
	}
}

func runAuthStatus() {
	provider := resolveProvider("", "")

	// Load config to check effective useSubscription setting.
	cwd, _ := os.Getwd()
	cfg, err := config.Load(cwd)
	if err != nil {
		cfg = config.Default()
	}
	useSub := cfg.Agent.UseSubscriptionEnabled()

	// Check auth as the agent will actually see it (with or without the API key).
	status := agent.AuthStatus(provider.CLIPath(), useSub)

	if status == nil {
		fmt.Println("Could not determine auth status. Is Claude CLI installed?")
		fmt.Println("Run: chief login")
		return
	}
	if !status.LoggedIn {
		fmt.Println("Not logged in.")
		fmt.Println("Run: chief login")
		return
	}

	label := status.AuthLabel()
	fmt.Printf("Logged in  •  %s\n", label)

	if !useSub && status.APIKeySource == "ANTHROPIC_API_KEY" {
		fmt.Println()
		fmt.Println("Warning: ANTHROPIC_API_KEY is set and useSubscription is disabled.")
		fmt.Println("API credits are billed instead of your Max/Pro plan.")
		fmt.Println("Remove 'useSubscription: false' from .chief/config.yaml to use subscription billing.")
	}
}

func runReview() {
	opts := cmd.ReviewOptions{}

	// Parse: chief review [name]
	args := os.Args[2:]
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") && opts.Name == "" {
			opts.Name = arg
		}
	}

	result, err := cmd.RunReview(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(result.Message)
	if result.Injected {
		fmt.Println("Run 'chief' to start the review (or press 'r' in the TUI).")
	}
}

// runVoiceCapture records a voice note and writes the transcript to --out <file>.
// Used internally by the TUI via tea.ExecProcess to capture voice context for reviews.
func runVoiceCapture() {
	outFile := ""
	args := os.Args[2:]
	for i, arg := range args {
		if arg == "--out" && i+1 < len(args) {
			outFile = args[i+1]
		}
	}
	if outFile == "" {
		fmt.Fprintln(os.Stderr, "voice-capture: --out <file> is required")
		os.Exit(1)
	}

	transcript, err := cmd.CollectVoiceInput(cmd.VoiceOptions{})
	if err != nil {
		// Write empty file so the TUI can detect failure gracefully.
		_ = os.WriteFile(outFile, []byte(""), 0o644)
		fmt.Fprintf(os.Stderr, "voice capture failed: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(outFile, []byte(transcript), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write transcript: %v\n", err)
		os.Exit(1)
	}
}

func runUpdate() {
	if err := cmd.RunUpdate(cmd.UpdateOptions{
		Version: Version,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runList() {
	opts := cmd.ListOptions{}

	if err := cmd.RunList(opts); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// resolveProvider loads config and resolves the agent provider, exiting on error.
func resolveProvider(flagAgent, flagPath string) loop.Provider {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to load .chief/config.yaml: %v\n", err)
		os.Exit(1)
	}
	provider, err := agent.Resolve(flagAgent, flagPath, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if err := agent.CheckInstalled(provider); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	return provider
}

func runTUIWithOptions(opts *TUIOptions) {
	provider := resolveProvider(opts.Agent, opts.AgentPath)

	prdPath := opts.PRDPath

	// If no PRD specified, try to find one
	if prdPath == "" {
		// Try "main" first
		mainPath := ".chief/prds/main/prd.md"
		if _, err := os.Stat(mainPath); err == nil {
			prdPath = mainPath
		} else {
			// Look for any available PRD
			prdPath = findAvailablePRD()
		}

		// If still no PRD found, run first-time setup
		if prdPath == "" {
			cwd, _ := os.Getwd()
			showGitignore := git.IsGitRepo(cwd) && !git.IsChiefIgnored(cwd)

			// Run the first-time setup TUI
			result, err := tui.RunFirstTimeSetup(cwd, showGitignore)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			if result.Cancelled {
				return
			}

			// Save config from setup
			cfg := config.Default()
			cfg.OnComplete.Push = result.PushOnComplete
			cfg.OnComplete.CreatePR = result.CreatePROnComplete
			if err := config.Save(cwd, cfg); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to save config: %v\n", err)
			}

			// Create the PRD
			newOpts := cmd.NewOptions{
				Name:     result.PRDName,
				Provider: provider,
			}
			if err := cmd.RunNew(newOpts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			// Restart TUI with the new PRD
			opts.PRDPath = fmt.Sprintf(".chief/prds/%s/prd.md", result.PRDName)
			runTUIWithOptions(opts)
			return
		}
	}

	prdDir := filepath.Dir(prdPath)

	// Auto-migrate: if prd.json exists alongside prd.md, migrate status
	jsonPath := filepath.Join(prdDir, "prd.json")
	if _, err := os.Stat(jsonPath); err == nil {
		fmt.Println("Migrating status from prd.json to prd.md...")
		if err := prd.MigrateFromJSON(prdDir); err != nil {
			fmt.Printf("Warning: migration failed: %v\n", err)
		} else {
			fmt.Println("Migration complete (prd.json renamed to prd.json.bak).")
		}
	}

	app, err := tui.NewAppWithOptions(prdPath, opts.MaxIterations, provider)
	if err != nil {
		// Check if this is a missing PRD file error
		if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
			fmt.Printf("PRD not found: %s\n", prdPath)
			fmt.Println()
			// Show available PRDs if any exist
			available := listAvailablePRDs()
			if len(available) > 0 {
				fmt.Println("Available PRDs:")
				for _, name := range available {
					fmt.Printf("  chief %s\n", name)
				}
				fmt.Println()
			}
			fmt.Println("Or create a new one:")
			fmt.Println("  chief new               # Create default PRD")
			fmt.Println("  chief new <name>        # Create named PRD")
		} else {
			fmt.Printf("Error: %v\n", err)
		}
		os.Exit(1)
	}

	// Set verbose mode if requested
	if opts.Verbose {
		app.SetVerbose(true)
	}

	// Disable retry if requested
	if opts.NoRetry {
		app.DisableRetry()
	}

	// Merge CLI --add-dir flags with any dirs from config
	if len(opts.AddDirs) > 0 {
		app.SetAddDirs(opts.AddDirs)
	}

	p := tea.NewProgram(app, tea.WithAltScreen())
	model, err := p.Run()
	if err != nil {
		fmt.Printf("Error running program: %v\n", err)
		os.Exit(1)
	}

	// Check for post-exit actions
	if finalApp, ok := model.(tui.App); ok {
		switch finalApp.PostExitAction {
		case tui.PostExitInit:
			// Run new command then restart TUI
			newOpts := cmd.NewOptions{
				Name:     finalApp.PostExitPRD,
				Provider: provider,
			}
			if err := cmd.RunNew(newOpts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			// Restart TUI with the new PRD
			opts.PRDPath = fmt.Sprintf(".chief/prds/%s/prd.md", finalApp.PostExitPRD)
			runTUIWithOptions(opts)

		case tui.PostExitEdit:
			// Run edit command then restart TUI
			editOpts := cmd.EditOptions{
				Name:     finalApp.PostExitPRD,
				Provider: provider,
			}
			if err := cmd.RunEdit(editOpts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			// Restart TUI with the edited PRD
			opts.PRDPath = fmt.Sprintf(".chief/prds/%s/prd.md", finalApp.PostExitPRD)
			runTUIWithOptions(opts)
		}
	}
}

func printHelp() {
	fmt.Println(`Chief - Autonomous PRD Agent

Usage:
  chief [options] [<name>|<path/to/prd.md>]
  chief <command> [arguments]

Commands:
  prd [description]         Generate a PRD (auto-research + optional voice loop)
  backlog <KEY>             Fetch a Backlog issue, read the repo, generate a fix PRD
  backlog --batch           Fetch all open issues, group related ones, generate batch PRD
  setup                     Study repo and configure .chief/config.yaml intelligently
  validate                  Run API use-case tests → fix → push → CI/CD loop
  orchestrate               Monitor + auto-fix multiple services in parallel
  new [name] [context]      Create a new PRD interactively
  edit [name] [options]     Edit an existing PRD interactively
  resume [name] [story-id]  Resume a completed story's Claude session
  review [name]             Audit implementation vs PRD acceptance criteria
  auth                      Show current authentication status and billing mode
  login                     Log in to Claude (opens browser for claude.ai subscription)
  status [name]             Show progress for a PRD (default: main)
  list                      List all PRDs with progress
  update                    Update Chief to the latest version
  help                      Show this help message

Global Options:
  --agent <provider>        Agent CLI to use: claude (default), codex, opencode, or cursor
  --agent-path <path>       Custom path to agent CLI binary
  --add-dir <path>          Expose additional directory to the agent (repeatable; Claude only)
  --max-iterations N, -n N  Set maximum iterations (default: dynamic)
  --no-retry                Disable auto-retry on agent crashes
  --verbose                 Show raw agent output in log
  --merge                   Auto-merge progress on conversion conflicts
  --force                   Auto-overwrite on conversion conflicts
  --help, -h                Show this help message
  --version, -v             Show version number

Edit Options:
  --merge                   Auto-merge progress on conversion conflicts
  --force                   Auto-overwrite on conversion conflicts

Positional Arguments:
  <name>                    PRD name (loads .chief/prds/<name>/prd.md)
  <path/to/prd.md>        Direct path to a prd.md file

Examples:
  chief                     Launch TUI with default PRD (.chief/prds/main/)
  chief auth                Launch TUI with named PRD (.chief/prds/auth/)
  chief ./my-prd.md       Launch TUI with specific PRD file
  chief -n 20               Launch with 20 max iterations
  chief --max-iterations=5 auth
                            Launch auth PRD with 5 max iterations
  chief --verbose           Launch with raw agent output visible
  chief --agent codex       Use Codex CLI instead of Claude
  chief --agent cursor      Use Cursor CLI as agent
  chief new                 Create PRD in .chief/prds/main/
  chief new auth            Create PRD in .chief/prds/auth/
  chief new auth "JWT authentication for REST API"
                            Create PRD with context hint
  chief edit                Edit PRD in .chief/prds/main/
  chief edit auth           Edit PRD in .chief/prds/auth/
  chief edit auth --merge   Edit and auto-merge progress
  chief prd "fix auth bug"           Auto-research + generate PRD
  chief prd --voice                  Multi-round voice loop → auto-research → PRD
  chief prd --voice --no-research    Voice → PRD (skip research pass)
  chief prd --no-research "add X"    Generate PRD without research pass
  chief prd --type feature "add X"   Force PRD type
  chief prd --duration 60 --voice    Longer recording per round (default: 30s)
  chief prd --voice-backend gemini   Force Gemini transcription
  chief prd --list-types             Show all supported PRD types
  chief resume              List resumable sessions for default PRD
  chief resume US-003       Resume story US-003's Claude session
  chief resume auth US-003  Resume story US-003 in the auth PRD
  chief status              Show progress for default PRD
  chief status auth         Show progress for auth PRD
  chief list                List all PRDs with progress
  chief --version           Show version number`)
}

func printWiggum() {
	// ANSI color codes
	blue := "\033[34m"
	yellow := "\033[33m"
	reset := "\033[0m"

	art := blue + `
                                                                 -=
                                      +%#-   :=#%#**%-
                                     ##+**************#%*-::::=*-
                                   :##***********************+***#
                                 :@#********%#%#******************#*
                                 :##*****%+-:::-%%%%%##************#:
                                   :#%###%%-:::+#*******##%%%*******#%*:
                                      -+%**#%%@@%%%%%%%%%#****#%##*##%%=
                                      -@@%%%%%%%%%%%%%%@*#%%#*##:::
                                    +%%%%%%%%%%%%%%@#+--=#--=#@+:
                                   -@@@@@%@@@@#%#=-=**--+*-----=#:
` + yellow + `                                       :*     *-   - :#-:*=-----=#:
                                       %::%@- *:  *@# +::=*--#=:-%:
                                       #- =+**##-    =*:::#*#-++:*:
                                        #+:-::+--%***-::::::::-*##
                                      :+#:+=:-==-*:::::::::::::::-%
                                     *=::::::::::::::-=*##*:::::::-+
                                     *-::::::::-=+**+-+%%%%+:::::--+
                                      :*%##**==++%%%######%:::::--%-
                                        :-=#--%####%%%%@@+:::::--%=
` + blue + `                     -#%%%%#-` + yellow + `          *:::+%%##%%#%%*:::::::-*#%-
                   :##++++=+++%:` + yellow + `        :@%*:::::::::::::::-=##*%%*%=
                  :%++++@%#+=++#` + yellow + `         %%%=--:::::---=+%%****%##@%#%%*:
                -%=-:-%%%*=+++##` + yellow + `      :*@%***@%%%###*********%%#%********%-
               *#+==**%++++++#*-` + yellow + `   :*%@*+*%*%%%%@*********%%**##****%=--#%*#
             *%#%-:+*++++*%#=#-` + yellow + `  :%#%#*+***#@%%%@%#%%%@%#*****%****%::::::##%-
            :*::::*-%@%@#=*%-` + yellow + `  :%*#%+*******%%%@#*************%****%-::::::**%=
             +==%*+-----+%` + yellow + `    %#*%#********#@%%@********%*%***#%**+*%-:::::*#*%:
              *=::----##**%:` + yellow + `+%#*@**********@%%%%*+***%-::::::#*%#****%#:::-%***%-
               #-:+@#***+*@%` + yellow + `**#%**********%%%#%%*****%::::::-#**%***************%
               =%*****+%%+**` + yellow + `@#%***********@%#%%#******%:::::%****@*********+****##
` + blue + `                %*#%@#*+++**#%` + yellow + `************%%%%%#********###*******@**************%:
                =#**++***+**@` + yellow + `************%%%%#%%*******************%*************##
                 %*++******@#` + yellow + `************@%%#%%@*******************#@*************@:
                  #***+***%#*` + yellow + `************@%%%%%@#*******************#%*************+
                   +#***##%**` + yellow + `************@%%%%%%%********************%************%
                     :######**` + yellow + `*+**********%%%%%%%%*********************%************%
                       :+%@#**` + yellow + `*******+*****#%@@%#******+***************#@*****+*****%:
` + blue + `                         @*********************************************##*+**+*****#+
                        =%%%%%@@@%%#**************************##%%@@@%%%@**********##
                        =%%#%%%%%%%%%%%%%----====%%%%%%%%%%%%%%%%#%%#%%%%%******#%#*%
                        :@@%%#%%%%%%%%%%#::::::::*%%%%%%%%%%%%%%%%%%#%%%@@#%%%##***#%
                          %*##%%@@@@%%%%%::::::::#%%%%%%%@@@@@@%%####****##****#%#==#
                          :%*********************************************#%#*+=-----*-
                           :%************************************+********@:::::----=+
                             ##**********+******************+************##::-::=--#-%
                              =%******************+*+*********************%:=-*:++:#-%
                               *#*****************************************@*#:*:*=:*+=
                                %*********#%#**************************+*%   -#+%**=:
                                **************#%%%%###*******************#
                                =#***************%      #****************#
                                :@***+**********##      *****************#
                                 %**************#=      =#+******+*******#
                                 =#*************%:      :@***************#
                                 :#****+********#        #***************#
                                 :#**************        =#**************#
                                 :%************%-        :%*************##
                                  #***********##          %*************%=
                                -%@@@%######%@@+          =%#***#*#%@@%#@:
                              :%%%%%%%%%%%%%%%%#         +@%%%%%%%%%%%%%%*
                             +@%%%%%%%%%%%%%%%%+       :%%%%%%%%%%%%%%##@+
                             #%%%%%%%%%%%@%@%@*       :@%%%%%%%%%%%%@%%@*
` + reset + `
                         "Bake 'em away, toys!"
                               - Chief Wiggum
`
	fmt.Print(art)
}
