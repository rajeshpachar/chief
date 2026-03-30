package config

// ProjectConfig holds per-repo project settings used by chief prd.
// Lives in .chief/config.yaml under the "project:" key.
type ProjectConfig struct {
	// StagingURL is the base URL of the staging environment (e.g. https://myapp.dev)
	StagingURL string `yaml:"stagingUrl,omitempty"`

	// BacklogBase is the Backlog instance root (e.g. https://myorg.backlog.com)
	BacklogBase string `yaml:"backlogBase,omitempty"`

	// BacklogProject is the project key (e.g. ONCACCESS, MYAPP)
	BacklogProject string `yaml:"backlogProject,omitempty"`

	// NotifyUserID is the Backlog user ID to @mention on issue comments
	NotifyUserID string `yaml:"notifyUserId,omitempty"`

	// TestCommand overrides the default test runner (default: pytest)
	TestCommand string `yaml:"testCommand,omitempty"`

	// Services defines the multi-service orchestration topology.
	// Used by: chief orchestrate
	Services []ServiceConfig `yaml:"services,omitempty"`

	// Validation defines API use-case tests and CI/CD integration.
	// Used by: chief validate
	Validation ValidationConfig `yaml:"validation,omitempty"`
}

// ValidationConfig holds API test cases and CI/CD pipeline settings.
// Example .chief/config.yaml:
//
//	project:
//	  validation:
//	    stagingUrl: https://staging.example.com
//	    authHeader: "Authorization: Bearer ${STAGING_TOKEN}"
//	    apiTests:
//	      - name: "drug interaction search returns results"
//	        method: GET
//	        path: /api/drugs/interactions?drug1=aspirin&drug2=warfarin
//	        expectStatus: 200
//	        expectBodyContains: '"interactions":'
//	    cicd:
//	      provider: github
//	      branch: dev
//	      pollIntervalSec: 30
//	      maxWaitSec: 600
type ValidationConfig struct {
	// StagingUrl overrides project.stagingUrl for validation (optional)
	StagingUrl string `yaml:"stagingUrl,omitempty"`

	// AuthHeader is an HTTP header to attach to all API test requests.
	// Supports env var expansion: "Authorization: Bearer ${STAGING_TOKEN}"
	// Ignored when Login is set — the token from the login response is used instead.
	AuthHeader string `yaml:"authHeader,omitempty"`

	// Login describes how to obtain an auth token before running API tests.
	// Chief will POST to Login.Path, extract the token, and inject it as the
	// auth header for all subsequent API test requests.
	// Takes precedence over AuthHeader.
	Login LoginConfig `yaml:"login,omitempty"`

	// CredentialsFile is a path to a .env-style file (key=value, one per line)
	// that chief loads before expanding ${VAR} references in Login.Body and AuthHeader.
	// Relative paths are resolved from the repo root.
	// Example: .chief/staging.creds  (add to .gitignore)
	CredentialsFile string `yaml:"credentialsFile,omitempty"`

	// ApiURL is the base URL of the backend API for API tests.
	// Use this when running chief from a frontend repo where stagingUrl points
	// to the UI — set apiUrl to the backend API base URL instead.
	ApiURL string `yaml:"apiUrl,omitempty"`

	// ApiTests is the list of use-case API tests to run against the staging server.
	ApiTests []ApiTestConfig `yaml:"apiTests,omitempty"`

	// CICD configures how chief pushes and polls the CI/CD pipeline.
	CICD CICDConfig `yaml:"cicd,omitempty"`
}

// LoginConfig describes a credential-based login step performed before API tests.
// Chief POSTs to the login endpoint, extracts the token from the response, and
// injects it into the Authorization header for all subsequent API test requests.
//
// Example .chief/config.yaml:
//
//	project:
//	  validation:
//	    credentialsFile: .chief/staging.creds   # EMAIL=test@example.com\nPASSWORD=secret
//	    login:
//	      path: /api/auth/login
//	      body: '{"email":"${EMAIL}","password":"${PASSWORD}"}'
//	      tokenField: data.accessToken          # dot-path into JSON response
//	      headerPrefix: "Bearer "               # prepended to token value (default: "Bearer ")
type LoginConfig struct {
	// Path is the login endpoint path (e.g. /api/auth/login)
	Path string `yaml:"path,omitempty"`

	// Body is the request body. Supports ${ENV_VAR} expansion.
	// For JSON:        '{"email":"${EMAIL}","password":"${PASSWORD}"}'
	// For form-encoded: 'username=${USERNAME}&password=${PASSWORD}'
	Body string `yaml:"body,omitempty"`

	// ContentType sets the Content-Type header for the login request.
	// Default: "application/json"
	// Use "application/x-www-form-urlencoded" for FastAPI OAuth2 / form-based login.
	ContentType string `yaml:"contentType,omitempty"`

	// TokenField is the dot-separated JSON path to the token in the response.
	// Examples: "token", "data.accessToken", "access_token", "auth.jwt"
	TokenField string `yaml:"tokenField,omitempty"`

	// HeaderName is the header name to inject the token into (default: "Authorization")
	HeaderName string `yaml:"headerName,omitempty"`

	// HeaderPrefix is prepended to the token value (default: "Bearer ")
	HeaderPrefix string `yaml:"headerPrefix,omitempty"`
}

// ApiTestConfig defines one API use-case test.
type ApiTestConfig struct {
	// Name describes what this test verifies (e.g. "login with valid credentials")
	Name string `yaml:"name"`

	// Method is the HTTP method (default: GET)
	Method string `yaml:"method,omitempty"`

	// Path is the URL path + query string (e.g. /api/drugs?name=aspirin)
	Path string `yaml:"path"`

	// Body is the request body for POST/PUT/PATCH (JSON string)
	Body string `yaml:"body,omitempty"`

	// Headers are extra HTTP headers for this request only (key: value)
	Headers map[string]string `yaml:"headers,omitempty"`

	// LoginBody overrides the default validation.login.body for this test only.
	// Use this to run a test as a different user or tenant without changing the
	// global login config. Supports ${ENV_VAR} expansion from the credentials file.
	//
	// Example — test as a different tenant:
	//   loginBody: 'username=${TENANT_B_USERNAME}&password=${TENANT_B_PASSWORD}&tenant_id=${TENANT_B_ID}'
	//
	// Chief caches tokens by login body so re-logins only happen when the user changes.
	LoginBody string `yaml:"loginBody,omitempty"`

	// ExpectStatus is the required HTTP status code (default: 200)
	ExpectStatus int `yaml:"expectStatus,omitempty"`

	// ExpectBodyContains is a substring that must appear in the response body
	ExpectBodyContains string `yaml:"expectBodyContains,omitempty"`

	// ExpectBodyNotContains is a substring that must NOT appear in the response body
	ExpectBodyNotContains string `yaml:"expectBodyNotContains,omitempty"`
}

// CICDConfig tells chief how to push and monitor the CI/CD pipeline.
type CICDConfig struct {
	// Provider: "github", "gitlab", "circleci", or "none" (just push, don't poll)
	Provider string `yaml:"provider,omitempty"`

	// Branch to push to (default: current git branch)
	Branch string `yaml:"branch,omitempty"`

	// Workflow is the GitHub Actions workflow file name (e.g. "ci.yml")
	// Only used when provider=github
	Workflow string `yaml:"workflow,omitempty"`

	// PollIntervalSec is how often to check pipeline status (default: 30)
	PollIntervalSec int `yaml:"pollIntervalSec,omitempty"`

	// MaxWaitSec is the maximum time to wait for CI to complete (default: 600)
	MaxWaitSec int `yaml:"maxWaitSec,omitempty"`
}

// ServiceConfig defines one service in a multi-service orchestration.
// Example .chief/config.yaml:
//
//	project:
//	  services:
//	    - name: api
//	      startCmd: "uvicorn app.main:app --port 8000"
//	      logFile: "logs/api.log"
//	      codeDir: "services/api"
//	      healthCheck: "curl -sf http://localhost:8000/health"
//	    - name: worker
//	      startCmd: "celery -A tasks worker --loglevel=info"
//	      logFile: "logs/worker.log"
//	      codeDir: "services/worker"
type ServiceConfig struct {
	// Name is the service identifier (e.g. "api", "worker", "db")
	Name string `yaml:"name"`

	// StartCmd is the shell command to start the service (optional — if omitted, service
	// is assumed to be already running and chief just monitors its log)
	StartCmd string `yaml:"startCmd,omitempty"`

	// LogFile is the path to the service log file (relative to BaseDir).
	// If empty, chief will try logs/<name>.log
	LogFile string `yaml:"logFile,omitempty"`

	// CodeDir is the directory containing this service's source code.
	// The service agent will only read files under this path (token efficiency).
	CodeDir string `yaml:"codeDir,omitempty"`

	// HealthCheck is a shell command that exits 0 when the service is healthy.
	// Example: "curl -sf http://localhost:8000/health"
	// If empty, chief considers the service healthy when its log has no recent errors.
	HealthCheck string `yaml:"healthCheck,omitempty"`

	// LogTailLines is how many recent log lines to feed the service agent (default: 50).
	// Keep low to preserve tokens — errors are always included regardless.
	LogTailLines int `yaml:"logTailLines,omitempty"`
}
