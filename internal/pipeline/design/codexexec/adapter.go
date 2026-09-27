// Package codexexec runs the preselected Codex CLI as a bounded, disposable
// document-only worker. The executable and host authentication are trusted.
package codexexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

const (
	DefaultBinary = "/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex"
	Version       = "codex-cli 0.158.0-alpha.2.1"
	maxPrompt     = 128 << 10
	maxSchema     = 64 << 10
	maxOutput     = 128 << 10
	maxStdout     = 2 << 20
	maxStderr     = 64 << 10
	maxTimeout    = 30 * time.Minute
)

var (
	ErrInvalid = errors.New("invalid Codex execution request")
	ErrRuntime = errors.New("Codex runtime unavailable or unsuccessful")
	ErrLimit   = errors.New("Codex execution exceeded output bound")
	ErrEvent   = errors.New("invalid or unsafe Codex runtime event stream")
)

type Config struct {
	Binary string
	Model  string
}

type Request struct {
	Prompt  string
	Schema  []byte
	Timeout time.Duration
}

type Result struct {
	ThreadID     string
	Output       []byte
	InputTokens  int64
	OutputTokens int64
}

type Adapter struct {
	binary string
	model  string
}

// New checks the exact preselected CLI version without starting a model. An
// absolute Binary is a trusted host setting, never model-supplied configuration.
func New(config Config) (*Adapter, error) {
	if config.Binary == "" {
		config.Binary = DefaultBinary
	}
	if !filepath.IsAbs(config.Binary) || strings.ContainsAny(config.Binary, "\x00\r\n") || len(config.Model) > 128 || (config.Model != "" && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`).MatchString(config.Model)) {
		return nil, ErrInvalid
	}
	info, err := os.Stat(config.Binary)
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrRuntime
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, config.Binary, "--version")
	cmd.Dir = os.TempDir()
	cmd.Env = allowedEnvironment(os.Environ())
	configureProcess(cmd)
	out, diagnostic := newBoundedWriter(4096, cancel), newBoundedWriter(maxStderr, cancel)
	cmd.Stdout, cmd.Stderr = out, diagnostic
	if err := cmd.Run(); err != nil || out.overflowed() || diagnostic.overflowed() || strings.TrimSpace(string(out.bytes())) != Version {
		return nil, ErrRuntime
	}
	return &Adapter{binary: config.Binary, model: config.Model}, nil
}

// Run creates a fresh CLI process/thread; it never resumes/forks or opens a
// repository checkout. Schema constrains generation, not semantic validation:
// callers must strictly decode and validate Output against their own contract.
func (a *Adapter) Run(ctx context.Context, request Request) (result Result, err error) {
	if a == nil || a.binary == "" || ctx == nil || request.Timeout <= 0 || request.Timeout > maxTimeout || strings.TrimSpace(request.Prompt) == "" || len(request.Prompt) > maxPrompt || !utf8.ValidString(request.Prompt) || strings.ContainsRune(request.Prompt, '\x00') || len(request.Schema) == 0 || len(request.Schema) > maxSchema {
		return Result{}, ErrInvalid
	}
	schema, err := protocol.CanonicalJSON(request.Schema)
	if err != nil {
		return Result{}, ErrInvalid
	}
	var shape map[string]json.RawMessage
	if json.Unmarshal(schema, &shape) != nil || shape == nil || string(shape["type"]) != `"object"` {
		return Result{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	root, err := os.MkdirTemp("", "projectctl-codexexec-")
	if err != nil {
		return Result{}, ErrRuntime
	}
	workspace := filepath.Join(root, "workspace")
	defer func() {
		// Only this freshly allocated private directory is ever removed. Chmod
		// permits cleanup of our read-only workspace on non-root Unix hosts.
		_ = os.Chmod(workspace, 0700)
		if cleanupErr := os.RemoveAll(root); cleanupErr != nil {
			result, err = Result{}, ErrRuntime
		}
	}()
	if os.Mkdir(workspace, 0500) != nil {
		return Result{}, ErrRuntime
	}
	schemaPath := filepath.Join(root, "schema.json")
	if os.WriteFile(schemaPath, schema, 0600) != nil {
		return Result{}, ErrRuntime
	}
	cmd := exec.CommandContext(ctx, a.binary, arguments(workspace, schemaPath, a.model)...)
	cmd.Dir = workspace
	cmd.Env = allowedEnvironment(os.Environ())
	cmd.Stdin = strings.NewReader(request.Prompt)
	configureProcess(cmd)
	out, diagnostic := newBoundedWriter(maxStdout, cancel), newBoundedWriter(maxStderr, cancel)
	cmd.Stdout, cmd.Stderr = out, diagnostic
	runErr := cmd.Run()
	if out.overflowed() || diagnostic.overflowed() {
		return Result{}, ErrLimit
	}
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if runErr != nil {
		return Result{}, ErrRuntime
	}
	return parseEvents(out.bytes())
}

func arguments(workspace, schema, model string) []string {
	args := []string{"--no-daemon", "--ask-for-approval", "never", "exec", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--sandbox", "read-only", "--skip-git-repo-check", "--json", "--color", "never", "--cd", workspace, "--output-schema", schema}
	for _, config := range []string{`forced_login_method="chatgpt"`, `web_search="disabled"`, `project_doc_max_bytes=0`, `mcp_servers={}`, `shell_environment_policy.inherit="none"`, `shell_environment_policy.ignore_default_excludes=false`, `history.persistence="none"`} {
		args = append(args, "--config", config)
	}
	for _, feature := range []string{"shell_tool", "unified_exec", "shell_snapshot", "plugins", "apps", "hooks", "multi_agent", "browser_use", "computer_use", "code_mode", "code_mode_host", "remote_plugin", "image_generation", "view_image", "memories", "goals", "sleep_tool", "workspace_dependencies", "skill_search", "skill_mcp_dependency_install", "tool_suggest", "auth_elicitation", "in_app_browser"} {
		args = append(args, "--disable", feature)
	}
	// This feature is present in the pinned executable's `features list`.
	args = append(args, "--enable", "skip_host_skill_discovery")
	if model != "" {
		args = append(args, "--model", model)
	}
	return append(args, "-")
}

// Authentication remains in the host's existing Codex home/keychain. Never
// forward API keys, GitHub credentials, endpoint overrides, proxies, loader
// injection variables, or an arbitrary caller-supplied environment map.
func allowedEnvironment(environment []string) []string {
	allowed := map[string]bool{"HOME": true, "CODEX_HOME": true, "USERPROFILE": true, "HOMEDRIVE": true, "HOMEPATH": true, "SYSTEMROOT": true, "WINDIR": true, "COMSPEC": true, "PATH": true, "TMPDIR": true, "TMP": true, "TEMP": true, "LANG": true, "LC_ALL": true, "TZ": true}
	var result []string
	seen := map[string]bool{}
	for _, value := range environment {
		key, _, ok := strings.Cut(value, "=")
		upper := strings.ToUpper(key)
		if ok && allowed[upper] && !seen[upper] && !strings.ContainsRune(value, '\x00') {
			seen[upper] = true
			result = append(result, value)
		}
	}
	return append(result, "TERM=dumb", "NO_COLOR=1")
}

type boundedWriter struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func newBoundedWriter(limit int, cancel context.CancelFunc) *boundedWriter {
	return &boundedWriter{limit: limit, cancel: cancel}
}
func (w *boundedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.overflow || len(data) > w.limit-w.buffer.Len() {
		w.overflow = true
		w.cancel()
		return 0, ErrLimit
	}
	return w.buffer.Write(data)
}
func (w *boundedWriter) overflowed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.overflow
}
func (w *boundedWriter) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buffer.Bytes()...)
}
