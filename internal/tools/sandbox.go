package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	pathpkg "path"
	"regexp"
	"strings"
	"time"

	agentruntime "easygo-agent/internal/agent/runtime"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const (
	maxSandboxCommandBytes = 32 * 1024
	maxSandboxPathBytes    = 4096
	maxSandboxStdinBytes   = 1024 * 1024
	maxSandboxTimeoutSecs  = 10 * 60
)

var sandboxApplicationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// SandboxControllerConfig configures the single internal controller client
// shared by all sandbox tools.
type SandboxControllerConfig struct {
	BaseURL        string
	AuthToken      string
	RequestTimeout time.Duration
	MaxOutputBytes int
}

// SandboxApplication is the controller's lifecycle view. Container IDs and
// ownership metadata deliberately never cross this interface.
type SandboxApplication struct {
	ID                 string `json:"id"`
	State              string `json:"state"`
	CreatedAt          string `json:"created_at"`
	LastUsedAt         string `json:"last_used_at,omitempty"`
	IdleExpiresAt      string `json:"idle_expires_at,omitempty"`
	HardExpiresAt      string `json:"hard_expires_at"`
	IdleTTLSeconds     int64  `json:"idle_ttl_seconds"`
	DistinctRuns       int    `json:"distinct_runs"`
	WorkspaceBytes     int64  `json:"workspace_bytes"`
	WorkspacePreserved bool   `json:"workspace_preserved"`
}

type SandboxApplyInput struct{}

type SandboxApplyResult struct {
	ApplicationID string             `json:"application_id"`
	Application   SandboxApplication `json:"application"`
	Created       bool               `json:"created"`
}

type SandboxApplicationResult struct {
	Application SandboxApplication `json:"application"`
}

type SandboxApplicationInput struct {
	ApplicationID string `json:"application_id" jsonschema:"required,description=Application ID returned by sandbox_apply"`
}

type SandboxExecInput struct {
	ApplicationID  string `json:"application_id" jsonschema:"required,description=Application ID returned by sandbox_apply"`
	Command        string `json:"command" jsonschema:"required,description=Shell command to execute inside the sandbox"`
	CWD            string `json:"cwd,omitempty" jsonschema:"description=Absolute working directory under /workspace"`
	Stdin          string `json:"stdin,omitempty" jsonschema:"description=Optional standard input for the command"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"description=Optional command timeout in seconds; defaults to 120 and cannot exceed 600"`
}

type sandboxExecRequest struct {
	Command        string `json:"command"`
	CWD            string `json:"cwd,omitempty"`
	Stdin          string `json:"stdin,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type SandboxExecResult struct {
	Application SandboxApplication `json:"application"`
	ExitCode    int                `json:"exit_code"`
	Stdout      string             `json:"stdout"`
	Stderr      string             `json:"stderr"`
	Truncated   bool               `json:"truncated"`
	DurationMS  int64              `json:"duration_ms"`
}

type SandboxWriteFileInput struct {
	ApplicationID string `json:"application_id" jsonschema:"required,description=Application ID returned by sandbox_apply"`
	Path          string `json:"path" jsonschema:"required,description=Absolute destination path under /workspace"`
	Content       string `json:"content" jsonschema:"required,description=UTF-8 file content"`
	Append        bool   `json:"append,omitempty" jsonschema:"description=Append instead of replacing the file"`
	Executable    bool   `json:"executable,omitempty" jsonschema:"description=Make the resulting file executable"`
}

type sandboxWriteFileRequest struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Append     bool   `json:"append,omitempty"`
	Executable bool   `json:"executable,omitempty"`
}

type SandboxWriteFileResult struct {
	Application SandboxApplication `json:"application"`
	Path        string             `json:"path"`
	SizeBytes   int64              `json:"size_bytes"`
}

type SandboxReadFileInput struct {
	ApplicationID string `json:"application_id" jsonschema:"required,description=Application ID returned by sandbox_apply"`
	Path          string `json:"path" jsonschema:"required,description=Absolute source path under /workspace"`
	Offset        int64  `json:"offset,omitempty" jsonschema:"description=Nonnegative byte offset for paginated reads"`
	MaxBytes      int    `json:"max_bytes,omitempty" jsonschema:"description=Maximum bytes to return; defaults to the configured limit"`
}

type sandboxReadFileRequest struct {
	Path     string `json:"path"`
	Offset   int64  `json:"offset,omitempty"`
	MaxBytes int    `json:"max_bytes,omitempty"`
}

type SandboxReadFileResult struct {
	Application SandboxApplication `json:"application"`
	Path        string             `json:"path"`
	Content     string             `json:"content"`
	SizeBytes   int64              `json:"size_bytes"`
	NextOffset  int64              `json:"next_offset"`
	EOF         bool               `json:"eof"`
}

type SandboxDestroyResult struct {
	ApplicationID string `json:"application_id"`
	Destroyed     bool   `json:"destroyed"`
}

type sandboxControllerErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type sandboxControllerClient struct {
	baseURL        string
	authToken      string
	client         *http.Client
	maxOutputBytes int
}

// NewSandboxTools constructs the complete persistent-workspace tool surface.
func NewSandboxTools(cfg SandboxControllerConfig) ([]tool.BaseTool, error) {
	client, err := newSandboxControllerClient(cfg)
	if err != nil {
		return nil, err
	}
	constructors := []func() (tool.InvokableTool, error){
		func() (tool.InvokableTool, error) {
			return utils.InferTool("sandbox_apply", "Apply for sandbox capacity for this conversation. Call this first; it is idempotent and returns an application_id.", client.apply)
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("sandbox_create", "Create or resume the applied sandbox container and persistent /workspace. Call sandbox_apply before this tool.", client.create)
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("sandbox_exec", "Run a shell command in an existing sandbox. Files in /workspace persist until sandbox_destroy or automatic expiry. Runtime networking is disabled.", client.execute)
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("sandbox_write_file", "Create, replace, or append a UTF-8 file under /workspace in an existing sandbox.", client.writeFile)
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("sandbox_read_file", "Read a UTF-8 file under /workspace. Use offset and next_offset to continue a paginated read.", client.readFile)
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("sandbox_status", "Inspect sandbox state, expiry, reuse count, and whether its workspace was preserved.", client.status)
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("sandbox_release", "Release active use and hibernate the sandbox while retaining /workspace until its expiry. Use sandbox_create to resume it.", client.release)
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("sandbox_destroy", "Immediately and irreversibly destroy the sandbox and its workspace.", client.destroy)
		},
	}
	result := make([]tool.BaseTool, 0, len(constructors))
	for _, construct := range constructors {
		item, err := construct()
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func newSandboxControllerClient(cfg SandboxControllerConfig) (*sandboxControllerClient, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	parsedURL, err := url.ParseRequestURI(baseURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return nil, errors.New("sandbox controller base URL must be an absolute http or https URL without credentials, query, or fragment")
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsedURL.Hostname()), ".")
	hostIP := net.ParseIP(hostname)
	if hostname != "localhost" && (hostIP == nil || !hostIP.IsLoopback()) {
		return nil, errors.New("sandbox controller base URL must use a loopback host")
	}
	if cfg.RequestTimeout <= 0 {
		return nil, errors.New("sandbox controller request timeout must be greater than zero")
	}
	authToken := strings.TrimSpace(cfg.AuthToken)
	if len(authToken) < 32 || strings.IndexFunc(authToken, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return nil, errors.New("sandbox controller auth token must contain at least 32 printable ASCII characters without spaces")
	}
	if cfg.MaxOutputBytes <= 0 || cfg.MaxOutputBytes > 1024*1024 {
		return nil, errors.New("sandbox controller output limit must be between 1 and 1048576 bytes")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &sandboxControllerClient{
		baseURL:   baseURL,
		authToken: authToken,
		client: &http.Client{
			Timeout:   cfg.RequestTimeout,
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		maxOutputBytes: cfg.MaxOutputBytes,
	}, nil
}

func (client *sandboxControllerClient) apply(ctx context.Context, _ SandboxApplyInput) (SandboxApplyResult, error) {
	var result SandboxApplyResult
	err := client.do(ctx, http.MethodPost, "/v1/applications", struct{}{}, &result)
	result.ApplicationID = result.Application.ID
	return result, err
}

func (client *sandboxControllerClient) create(ctx context.Context, input SandboxApplicationInput) (SandboxApplicationResult, error) {
	endpoint, err := applicationEndpoint(input.ApplicationID, "/create")
	if err != nil {
		return SandboxApplicationResult{}, err
	}
	var result SandboxApplicationResult
	err = client.do(ctx, http.MethodPost, endpoint, struct{}{}, &result)
	return result, err
}

func (client *sandboxControllerClient) execute(ctx context.Context, input SandboxExecInput) (SandboxExecResult, error) {
	endpoint, err := applicationEndpoint(input.ApplicationID, "/exec")
	if err != nil {
		return SandboxExecResult{}, err
	}
	input.Command = strings.TrimSpace(input.Command)
	if input.Command == "" || len(input.Command) > maxSandboxCommandBytes || strings.ContainsRune(input.Command, '\x00') {
		return SandboxExecResult{}, fmt.Errorf("sandbox command must contain 1 to %d bytes without NUL", maxSandboxCommandBytes)
	}
	if err := validateSandboxPath(input.CWD, true); err != nil {
		return SandboxExecResult{}, fmt.Errorf("sandbox cwd: %w", err)
	}
	if len(input.Stdin) > maxSandboxStdinBytes {
		return SandboxExecResult{}, fmt.Errorf("sandbox stdin exceeds %d bytes", maxSandboxStdinBytes)
	}
	if input.TimeoutSeconds < 0 || input.TimeoutSeconds > maxSandboxTimeoutSecs {
		return SandboxExecResult{}, fmt.Errorf("sandbox timeout_seconds must be between 0 and %d", maxSandboxTimeoutSecs)
	}
	var result SandboxExecResult
	err = client.do(ctx, http.MethodPost, endpoint, sandboxExecRequest{Command: input.Command, CWD: input.CWD, Stdin: input.Stdin, TimeoutSeconds: input.TimeoutSeconds}, &result)
	return result, err
}

func (client *sandboxControllerClient) writeFile(ctx context.Context, input SandboxWriteFileInput) (SandboxWriteFileResult, error) {
	endpoint, err := applicationEndpoint(input.ApplicationID, "/files/write")
	if err != nil {
		return SandboxWriteFileResult{}, err
	}
	if err := validateSandboxPath(input.Path, false); err != nil {
		return SandboxWriteFileResult{}, fmt.Errorf("sandbox file path: %w", err)
	}
	if len(input.Content) > maxSandboxStdinBytes {
		return SandboxWriteFileResult{}, fmt.Errorf("sandbox file content exceeds %d bytes", maxSandboxStdinBytes)
	}
	var result SandboxWriteFileResult
	err = client.do(ctx, http.MethodPost, endpoint, sandboxWriteFileRequest{Path: input.Path, Content: input.Content, Append: input.Append, Executable: input.Executable}, &result)
	return result, err
}

func (client *sandboxControllerClient) readFile(ctx context.Context, input SandboxReadFileInput) (SandboxReadFileResult, error) {
	endpoint, err := applicationEndpoint(input.ApplicationID, "/files/read")
	if err != nil {
		return SandboxReadFileResult{}, err
	}
	if err := validateSandboxPath(input.Path, false); err != nil {
		return SandboxReadFileResult{}, fmt.Errorf("sandbox file path: %w", err)
	}
	if input.Offset < 0 {
		return SandboxReadFileResult{}, errors.New("sandbox file offset cannot be negative")
	}
	if input.MaxBytes == 0 {
		input.MaxBytes = client.maxOutputBytes
	}
	if input.MaxBytes < 1 || input.MaxBytes > client.maxOutputBytes {
		return SandboxReadFileResult{}, fmt.Errorf("sandbox max_bytes must be between 1 and %d", client.maxOutputBytes)
	}
	var result SandboxReadFileResult
	err = client.do(ctx, http.MethodPost, endpoint, sandboxReadFileRequest{Path: input.Path, Offset: input.Offset, MaxBytes: input.MaxBytes}, &result)
	return result, err
}

func (client *sandboxControllerClient) status(ctx context.Context, input SandboxApplicationInput) (SandboxApplicationResult, error) {
	endpoint, err := applicationEndpoint(input.ApplicationID, "")
	if err != nil {
		return SandboxApplicationResult{}, err
	}
	var result SandboxApplicationResult
	err = client.do(ctx, http.MethodGet, endpoint, nil, &result)
	return result, err
}

func (client *sandboxControllerClient) release(ctx context.Context, input SandboxApplicationInput) (SandboxApplicationResult, error) {
	endpoint, err := applicationEndpoint(input.ApplicationID, "/release")
	if err != nil {
		return SandboxApplicationResult{}, err
	}
	var result SandboxApplicationResult
	err = client.do(ctx, http.MethodPost, endpoint, struct{}{}, &result)
	return result, err
}

func (client *sandboxControllerClient) destroy(ctx context.Context, input SandboxApplicationInput) (SandboxDestroyResult, error) {
	endpoint, err := applicationEndpoint(input.ApplicationID, "")
	if err != nil {
		return SandboxDestroyResult{}, err
	}
	err = client.do(ctx, http.MethodDelete, endpoint, nil, nil)
	return SandboxDestroyResult{ApplicationID: input.ApplicationID, Destroyed: err == nil}, err
}

func (client *sandboxControllerClient) do(ctx context.Context, method, endpoint string, body, output any) error {
	identity, ok := agentruntime.InvocationIdentityFromContext(ctx)
	if !ok {
		return errors.New("sandbox tool requires trusted session and run identity")
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode sandbox controller request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+endpoint, reader)
	if err != nil {
		return fmt.Errorf("create sandbox controller request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+client.authToken)
	request.Header.Set("X-EasyGo-Session-ID", identity.SessionID)
	request.Header.Set("X-EasyGo-Run-ID", identity.RunID)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.client.Do(request)
	if err != nil {
		return fmt.Errorf("call sandbox controller: %w", err)
	}
	defer response.Body.Close()
	maxBodyBytes := int64(client.maxOutputBytes*12 + 128*1024)
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil {
		return fmt.Errorf("read sandbox controller response: %w", err)
	}
	if int64(len(responseBody)) > maxBodyBytes {
		return errors.New("sandbox controller response exceeds configured output limit")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var envelope sandboxControllerErrorEnvelope
		_ = json.Unmarshal(responseBody, &envelope)
		message := strings.TrimSpace(envelope.Error.Message)
		if message == "" {
			message = http.StatusText(response.StatusCode)
		}
		code := strings.TrimSpace(envelope.Error.Code)
		if code != "" {
			message = code + ": " + message
		}
		if retryAfter := strings.TrimSpace(response.Header.Get("Retry-After")); retryAfter != "" {
			message += " (retry after " + retryAfter + ")"
		}
		return fmt.Errorf("sandbox controller returned %d: %s", response.StatusCode, message)
	}
	if output == nil {
		return nil
	}
	if len(responseBody) == 0 {
		return errors.New("sandbox controller returned an empty response")
	}
	if err := json.Unmarshal(responseBody, output); err != nil {
		return fmt.Errorf("decode sandbox controller response: %w", err)
	}
	return nil
}

func applicationEndpoint(applicationID, suffix string) (string, error) {
	applicationID = strings.TrimSpace(applicationID)
	if !sandboxApplicationIDPattern.MatchString(applicationID) {
		return "", errors.New("sandbox application_id must contain 1 to 128 letters, digits, dots, underscores, or hyphens")
	}
	return "/v1/applications/" + url.PathEscape(applicationID) + suffix, nil
}

func validateSandboxPath(value string, optional bool) error {
	if value == "" && optional {
		return nil
	}
	if value == "" || len(value) > maxSandboxPathBytes || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("must contain 1 to %d bytes without NUL", maxSandboxPathBytes)
	}
	cleaned := pathpkg.Clean(value)
	if cleaned != value {
		return errors.New("must be a canonical path without traversal")
	}
	if cleaned != "/workspace" && !strings.HasPrefix(cleaned, "/workspace/") {
		return errors.New("must be an absolute path under /workspace")
	}
	return nil
}
