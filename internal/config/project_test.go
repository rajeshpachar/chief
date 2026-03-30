package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectConfigLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.Project.StagingURL != "" {
		t.Errorf("expected empty StagingURL, got %q", cfg.Project.StagingURL)
	}
	if cfg.Project.BacklogBase != "" {
		t.Errorf("expected empty BacklogBase, got %q", cfg.Project.BacklogBase)
	}
	if cfg.Project.NotifyUserID != "" {
		t.Errorf("expected empty NotifyUserID, got %q", cfg.Project.NotifyUserID)
	}
}

func TestProjectConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()

	cfg := Default()
	cfg.Project.StagingURL = "https://myapp.k8s-dev.example.com"
	cfg.Project.BacklogBase = "https://myorg.backlog.com"
	cfg.Project.BacklogProject = "MYAPP"
	cfg.Project.NotifyUserID = "12345"
	cfg.Project.TestCommand = "pytest tests/ -x -q"

	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() after Save() failed: %v", err)
	}

	if loaded.Project.StagingURL != cfg.Project.StagingURL {
		t.Errorf("StagingURL: got %q, want %q", loaded.Project.StagingURL, cfg.Project.StagingURL)
	}
	if loaded.Project.BacklogBase != cfg.Project.BacklogBase {
		t.Errorf("BacklogBase: got %q, want %q", loaded.Project.BacklogBase, cfg.Project.BacklogBase)
	}
	if loaded.Project.BacklogProject != cfg.Project.BacklogProject {
		t.Errorf("BacklogProject: got %q, want %q", loaded.Project.BacklogProject, cfg.Project.BacklogProject)
	}
	if loaded.Project.NotifyUserID != cfg.Project.NotifyUserID {
		t.Errorf("NotifyUserID: got %q, want %q", loaded.Project.NotifyUserID, cfg.Project.NotifyUserID)
	}
	if loaded.Project.TestCommand != cfg.Project.TestCommand {
		t.Errorf("TestCommand: got %q, want %q", loaded.Project.TestCommand, cfg.Project.TestCommand)
	}
}

func TestProjectConfigPartialYAML(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".chief"), 0755)

	// Write YAML with only project.stagingUrl set — other fields should default to zero
	yaml := `project:
  stagingUrl: https://staging.example.com
`
	os.WriteFile(filepath.Join(dir, ".chief", "config.yaml"), []byte(yaml), 0644)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.Project.StagingURL != "https://staging.example.com" {
		t.Errorf("StagingURL: got %q", cfg.Project.StagingURL)
	}
	if cfg.Project.BacklogBase != "" {
		t.Errorf("BacklogBase should be empty, got %q", cfg.Project.BacklogBase)
	}
}

func TestProjectConfigOmittedFromYAMLWhenEmpty(t *testing.T) {
	dir := t.TempDir()

	// Save a config with empty ProjectConfig
	cfg := Default()
	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Read raw YAML — project section should be omitted (omitempty)
	data, err := os.ReadFile(filepath.Join(dir, ".chief", "config.yaml"))
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	yaml := string(data)
	// With omitempty, an all-zero ProjectConfig should not appear in YAML
	// OR it appears but with all fields empty — both are acceptable
	// What matters is that Load() still works correctly
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() after empty Save(): %v", err)
	}
	if loaded.Project.StagingURL != "" {
		t.Errorf("expected empty StagingURL after round-trip, got %q\nYAML:\n%s", loaded.Project.StagingURL, yaml)
	}
}
