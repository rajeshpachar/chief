package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/minicodemonkey/chief/internal/prd"
)

// ResumeOptions contains configuration for the resume command.
type ResumeOptions struct {
	Name    string // PRD name (default: "main")
	StoryID string // Story ID to resume (e.g. "US-003")
	BaseDir string // Base directory for .chief/prds/ (default: current directory)
	// CLIPath is the path to the claude binary (default: "claude")
	CLIPath string
}

// RunResume opens an interactive claude --resume session for a completed story.
func RunResume(opts ResumeOptions) error {
	if opts.Name == "" {
		opts.Name = "main"
	}
	if opts.BaseDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
		}
		opts.BaseDir = cwd
	}
	if opts.CLIPath == "" {
		opts.CLIPath = "claude"
	}

	prdDir := filepath.Join(opts.BaseDir, ".chief", "prds", opts.Name)
	prdPath := filepath.Join(prdDir, "prd.md")

	if _, err := os.Stat(prdPath); os.IsNotExist(err) {
		return fmt.Errorf("PRD %q not found at %s", opts.Name, prdPath)
	}

	// If no story ID provided, list available sessions and let user pick
	if opts.StoryID == "" {
		return listAndPromptResume(prdPath, opts)
	}

	return resumeStory(prdPath, opts.StoryID, opts.CLIPath, opts.BaseDir)
}

// resumeStory launches claude --resume <session-id> for a specific story.
func resumeStory(prdPath, storyID, cliPath, workDir string) error {
	sessionID, err := prd.GetSession(prdPath, storyID)
	if err != nil {
		return fmt.Errorf("failed to read sessions: %w", err)
	}
	if sessionID == "" {
		return fmt.Errorf("no session found for story %q — only stories completed by Chief have resumable sessions", storyID)
	}

	fmt.Printf("Resuming session for %s (session: %s)...\n\n", storyID, sessionID)

	cmd := exec.Command(cliPath, "--resume", sessionID)
	cmd.Dir = workDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// listAndPromptResume shows available sessions and prompts the user to pick one.
func listAndPromptResume(prdPath string, opts ResumeOptions) error {
	sessions, err := prd.LoadSessions(prdPath)
	if err != nil || len(sessions) == 0 {
		return fmt.Errorf("no resumable sessions found for PRD %q — sessions are saved after Chief completes each story", opts.Name)
	}

	// Load PRD to show story titles alongside IDs
	p, _ := prd.LoadPRD(prdPath)
	titleFor := map[string]string{}
	if p != nil {
		for _, s := range p.UserStories {
			titleFor[s.ID] = s.Title
		}
	}

	fmt.Printf("Resumable sessions for PRD %q:\n\n", opts.Name)
	i := 1
	ids := make([]string, 0, len(sessions))
	for id := range sessions {
		title := titleFor[id]
		if title != "" {
			fmt.Printf("  %d. %s — %s\n", i, id, title)
		} else {
			fmt.Printf("  %d. %s\n", i, id)
		}
		ids = append(ids, id)
		i++
	}

	fmt.Printf("\nRun: chief resume %s <story-id>\n", opts.Name)
	return nil
}
