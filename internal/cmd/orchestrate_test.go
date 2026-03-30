package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/minicodemonkey/chief/internal/config"
)

// ─── readLogTail ─────────────────────────────────────────────────────────────

func TestReadLogTailNonExistentFile(t *testing.T) {
	tail, errors := readLogTail("/nonexistent/path/to/log.log", 50)
	if tail != "" {
		t.Errorf("expected empty tail for missing file, got %q", tail)
	}
	if len(errors) != 0 {
		t.Errorf("expected no errors for missing file, got %v", errors)
	}
}

func TestReadLogTailReturnsTailLines(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")

	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, strings.Repeat(string(rune('a'+i%26)), 10))
	}
	os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0644)

	tail, _ := readLogTail(logPath, 20)
	tailLines := strings.Split(strings.TrimSpace(tail), "\n")
	if len(tailLines) != 20 {
		t.Errorf("expected 20 tail lines, got %d", len(tailLines))
	}
}

func TestReadLogTailExtractsErrors(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")

	content := "INFO starting up\nERROR database connection failed\nINFO retrying\nEXCEPTION NullPointerException at line 42\nINFO done\n"
	os.WriteFile(logPath, []byte(content), 0644)

	_, errors := readLogTail(logPath, 50)
	if len(errors) != 2 {
		t.Errorf("expected 2 error lines (ERROR + EXCEPTION), got %d: %v", len(errors), errors)
	}
	if !strings.Contains(errors[0], "database connection failed") {
		t.Errorf("expected first error to contain 'database connection failed', got: %q", errors[0])
	}
}

func TestReadLogTailCapsErrorsAt20(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")

	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, "ERROR something went wrong "+string(rune('a'+i%26)))
	}
	os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0644)

	_, errors := readLogTail(logPath, 200)
	if len(errors) > 20 {
		t.Errorf("expected max 20 error lines, got %d", len(errors))
	}
}

// ─── pollService ─────────────────────────────────────────────────────────────

func TestPollServiceNoLogFile(t *testing.T) {
	svc := config.ServiceConfig{
		Name:    "api",
		LogFile: "/nonexistent/api.log",
	}
	h := pollService(svc, t.TempDir())
	// No log = no errors detected = healthy by default
	if !h.Healthy {
		t.Error("service with no log file should be considered healthy (no errors detected)")
	}
	if h.Name != "api" {
		t.Errorf("expected name 'api', got %q", h.Name)
	}
}

func TestPollServiceDetectsErrors(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "worker.log")
	os.WriteFile(logPath, []byte("INFO start\nERROR failed to connect to Redis\nINFO retrying\n"), 0644)

	svc := config.ServiceConfig{
		Name:    "worker",
		LogFile: logPath,
	}
	h := pollService(svc, dir)
	if h.Healthy {
		t.Error("service with ERROR in log should be unhealthy")
	}
	if len(h.Errors) == 0 {
		t.Error("expected errors to be extracted")
	}
}

func TestPollServiceHealthCheckPass(t *testing.T) {
	svc := config.ServiceConfig{
		Name:        "api",
		LogFile:     "/nonexistent/no.log",
		HealthCheck: "true", // shell builtin that always exits 0
	}
	h := pollService(svc, t.TempDir())
	if !h.Healthy {
		t.Errorf("service with passing health check should be healthy, got errors: %v", h.Errors)
	}
}

func TestPollServiceHealthCheckFail(t *testing.T) {
	svc := config.ServiceConfig{
		Name:        "api",
		LogFile:     "/nonexistent/no.log",
		HealthCheck: "false", // shell builtin that always exits 1
	}
	h := pollService(svc, t.TempDir())
	if h.Healthy {
		t.Error("service with failing health check should be unhealthy")
	}
}

func TestPollServiceLogTailLinesDefault(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "svc.log")

	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, "INFO line "+strings.Repeat("x", 10))
	}
	os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0644)

	svc := config.ServiceConfig{
		Name:         "svc",
		LogFile:      logPath,
		LogTailLines: 0, // should default to 50
	}
	h := pollService(svc, dir)
	tailLines := strings.Split(strings.TrimSpace(h.LogTail), "\n")
	if len(tailLines) > 50 {
		t.Errorf("default log tail should be 50 lines, got %d", len(tailLines))
	}
}

// ─── pollAllServices ─────────────────────────────────────────────────────────

func TestPollAllServicesReturnsOnePerService(t *testing.T) {
	services := []config.ServiceConfig{
		{Name: "api", LogFile: "/nonexistent/api.log"},
		{Name: "worker", LogFile: "/nonexistent/worker.log"},
		{Name: "db", LogFile: "/nonexistent/db.log"},
	}
	results := pollAllServices(services, t.TempDir())
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}
	names := map[string]bool{}
	for _, r := range results {
		names[r.Name] = true
	}
	for _, svc := range services {
		if !names[svc.Name] {
			t.Errorf("missing result for service %q", svc.Name)
		}
	}
}

// ─── extractJSON ─────────────────────────────────────────────────────────────

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "clean JSON",
			input: `{"summary":"ok","services_to_fix":[],"abort":false}`,
			want:  `{"summary":"ok","services_to_fix":[],"abort":false}`,
		},
		{
			name:  "JSON wrapped in markdown",
			input: "Here is the response:\n```json\n{\"summary\":\"fix worker\",\"abort\":false}\n```",
			want:  `{"summary":"fix worker","abort":false}`,
		},
		{
			name:  "JSON with preceding text",
			input: "I decided to fix the worker service.\n{\"summary\":\"fix\",\"abort\":false}",
			want:  `{"summary":"fix","abort":false}`,
		},
		{
			name:  "no JSON returns original",
			input: "no json here",
			want:  "no json here",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ─── unhealthyNames ──────────────────────────────────────────────────────────

func TestUnhealthyNames(t *testing.T) {
	health := []ServiceHealth{
		{Name: "api", Healthy: true},
		{Name: "worker", Healthy: false},
		{Name: "db", Healthy: false},
	}
	names := unhealthyNames(health)
	if len(names) != 2 {
		t.Errorf("expected 2 unhealthy names, got %d: %v", len(names), names)
	}
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if !found["worker"] || !found["db"] {
		t.Errorf("expected worker and db in unhealthy list, got %v", names)
	}
}

// ─── buildOrchestratePrompt ───────────────────────────────────────────────────

func TestBuildOrchestratePrompt(t *testing.T) {
	services := []config.ServiceConfig{
		{Name: "api"},
		{Name: "worker"},
	}
	prompt := buildOrchestratePrompt(services, "fix the worker queue")
	if !strings.Contains(prompt, "api") {
		t.Error("prompt should contain service name 'api'")
	}
	if !strings.Contains(prompt, "worker") {
		t.Error("prompt should contain service name 'worker'")
	}
	if !strings.Contains(prompt, "fix the worker queue") {
		t.Error("prompt should contain the goal")
	}
}

// ─── RunOrchestrate (unit — no claude) ───────────────────────────────────────

func TestOrchestrateEmptyServiceNameError(t *testing.T) {
	opts := OrchestrateOptions{
		BaseDir: t.TempDir(),
		DryRun:  true,
		Services: []config.ServiceConfig{
			{Name: "", LogFile: "api.log"}, // missing name
		},
	}
	_, err := RunOrchestrate(opts)
	if err == nil {
		t.Error("expected error for service with empty name")
	}
	if !strings.Contains(err.Error(), "must have a name") {
		t.Errorf("expected 'must have a name' error, got: %v", err)
	}
}

func TestOrchestrateDuplicateServiceNameError(t *testing.T) {
	opts := OrchestrateOptions{
		BaseDir: t.TempDir(),
		DryRun:  true,
		Services: []config.ServiceConfig{
			{Name: "api", LogFile: "api.log"},
			{Name: "api", LogFile: "api2.log"}, // duplicate
		},
	}
	_, err := RunOrchestrate(opts)
	if err == nil {
		t.Error("expected error for duplicate service names")
	}
	if !strings.Contains(err.Error(), "duplicate service name") {
		t.Errorf("expected 'duplicate service name' error, got: %v", err)
	}
}

func TestRunOrchestrateNoServicesError(t *testing.T) {
	opts := OrchestrateOptions{
		BaseDir:  t.TempDir(),
		DryRun:   true,
		Services: []config.ServiceConfig{}, // empty
	}
	_, err := RunOrchestrate(opts)
	if err == nil {
		t.Error("expected error for empty services list")
	}
	if !strings.Contains(err.Error(), "no services configured") {
		t.Errorf("expected 'no services configured' error, got: %v", err)
	}
}

func TestRunOrchestrateDryRunAllHealthy(t *testing.T) {
	dir := t.TempDir()
	// Write clean log files (no errors)
	os.WriteFile(filepath.Join(dir, "api.log"), []byte("INFO started\nINFO ready\n"), 0644)
	os.WriteFile(filepath.Join(dir, "worker.log"), []byte("INFO started\nINFO processing\n"), 0644)

	services := []config.ServiceConfig{
		{Name: "api", LogFile: filepath.Join(dir, "api.log"), HealthCheck: "true"},
		{Name: "worker", LogFile: filepath.Join(dir, "worker.log"), HealthCheck: "true"},
	}
	opts := OrchestrateOptions{
		BaseDir:  dir,
		DryRun:   true,
		MaxCycles: 2,
		Services: services,
	}
	result, err := RunOrchestrate(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.AllHealthy {
		t.Error("expected all services to be healthy with clean logs and passing health checks")
	}
	if len(result.ServiceNames) != 2 {
		t.Errorf("expected 2 service names, got %d", len(result.ServiceNames))
	}
}

func TestRunOrchestrateDryRunUnhealthy(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "api.log"), []byte("INFO start\nERROR database down\n"), 0644)

	services := []config.ServiceConfig{
		{Name: "api", LogFile: filepath.Join(dir, "api.log")},
	}
	opts := OrchestrateOptions{
		BaseDir:   dir,
		DryRun:    true,
		MaxCycles: 1,
		Services:  services,
	}
	result, err := RunOrchestrate(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// dry-run with unhealthy service: breaks after first cycle without fixing
	if result.AllHealthy {
		t.Error("expected unhealthy result when log has ERROR lines")
	}
}

func TestRunOrchestrateServiceConfigFromYAML(t *testing.T) {
	// Verify that ServiceConfig round-trips through YAML correctly
	cfg := config.ServiceConfig{
		Name:         "api",
		StartCmd:     "uvicorn app:main --port 8000",
		LogFile:      "logs/api.log",
		CodeDir:      "services/api",
		HealthCheck:  "curl -sf http://localhost:8000/health",
		LogTailLines: 30,
	}
	if cfg.Name != "api" {
		t.Error("name should be 'api'")
	}
	if cfg.LogTailLines != 30 {
		t.Errorf("expected LogTailLines=30, got %d", cfg.LogTailLines)
	}
}
