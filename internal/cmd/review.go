package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/minicodemonkey/chief/internal/prd"
)

// ReviewOptions configures the post-PRD review command.
type ReviewOptions struct {
	// Name is the PRD name (default: "main")
	Name string

	// BaseDir is the repo root (default: cwd)
	BaseDir string

	// CLIPath is the agent binary (unused for inject-only; kept for future use)
	CLIPath string
}

// ReviewResult is returned by RunReview.
type ReviewResult struct {
	// Injected is true if the RV-000 story was freshly added.
	Injected bool

	// Message is a human-readable status.
	Message string
}

// RunReview injects a post-implementation review story (RV-000) into the PRD.
// The story tells Claude to audit every acceptance criterion, check the git diff,
// and either finish clean or add specific fix stories to prd.md.
//
// After injection, run `chief` (or press `r` in the TUI) to execute the review.
// The review runs as a normal agent session with full log visibility.
func RunReview(opts ReviewOptions) (*ReviewResult, error) {
	if opts.Name == "" {
		opts.Name = "main"
	}
	if opts.BaseDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get cwd: %w", err)
		}
		opts.BaseDir = cwd
	}

	prdPath := filepath.Join(opts.BaseDir, ".chief", "prds", opts.Name, "prd.md")
	if _, err := os.Stat(prdPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("PRD %q not found at %s", opts.Name, prdPath)
	}

	injected, err := prd.InjectReviewStory(prdPath, "")
	if err != nil {
		return nil, fmt.Errorf("inject review story: %w", err)
	}

	if !injected {
		return &ReviewResult{
			Injected: false,
			Message:  "review story already present — run 'chief' to execute",
		}, nil
	}

	return &ReviewResult{
		Injected: true,
		Message:  "RV-000 added — run 'chief' to execute the review",
	}, nil
}
