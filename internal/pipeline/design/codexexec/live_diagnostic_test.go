package codexexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This is the independent Round 1 acceptance's sanitized native shape. Neither
// error's private message was supplied, so placeholders are not claimed captures.
func TestRoundOneErrorShapeRemainsRejected(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(testEvents()), "\n")
	stream := strings.Join([]string{lines[0], `{"type":"item.completed","item":{"id":"item_0","type":"error","message":"REDACTED"}}`, `{"type":"item.completed","item":{"id":"item_1","type":"error","message":"REDACTED"}}`, lines[1], lines[2], lines[3]}, "\n")
	_, err := parseEvents([]byte(stream))
	var detail *EventError
	if !errors.As(err, &detail) || detail.Reason != "item_before_turn" || detail.Line != 2 {
		t.Fatal("native pre-turn errors must not become accepted output", err)
	}
}

// Diagnostics return only a fixed vocabulary. Raw native error text, unknown
// keys, model prose, paths, account identifiers and credentials are never logged.
func nativeErrorCategories(message string) []string {
	var result []string
	for _, known := range []string{"under-development features", "experimental", "deprecated", "unknown feature flag", "configuration", "shell_environment_policy", "ignore_default_excludes", "inherit", "filters", "skip_host_skill_discovery", "suppress_unstable_features_warning", "shell_tool", "unified_exec", "mcp", "sandbox", "authentication", "permission", "network", "failed", "model metadata", "fallback", "model", "not found", "unsupported", "disabled", "not supported", "project", "config.toml", "agents.md", "skill", "directory", "world-writable", "trusted", "rate", "limit", "budget", "credit", "quota", "read-only", "remote", "proxy", "connection", "version", "update", "file", "load", "command", "instruction", "api", "login", "tool", "capabilit", "cache", "storage", "history", "ephemeral", "guardian", "reasoning", "verbosity", "host", "legacy", "ignored", "warning", "available", "notice", "no longer", "enable", "terminal", "context", "snapshot"} {
		if strings.Contains(strings.ToLower(message), known) {
			result = append(result, known)
		}
	}
	for _, known := range []string{"config", "trust", "unable", "could not", "reviewer", "approval", "auto_review", "remote_plugin", "code_mode", "plugins", "apps"} {
		if strings.Contains(strings.ToLower(message), known) {
			result = append(result, known)
		}
	}
	return result
}

func TestDiagnosticDoesNotExposeContent(t *testing.T) {
	got := strings.Join(nativeErrorCategories("SECRET_TOKEN /private/account shell_environment_policy is deprecated; use filters"), ",")
	if got != "deprecated,shell_environment_policy,filters" || strings.Contains(got, "SECRET") {
		t.Fatal("unsafe diagnostic")
	}
}

func TestPinnedConfigurationAcknowledgesKnownDevelopmentFeature(t *testing.T) {
	args := arguments("workspace", "schema", "")
	if !hasArgumentPair(args, "--enable", "skip_host_skill_discovery") || !hasArgumentPair(args, "--config", "suppress_unstable_features_warning=true") {
		t.Fatal("the deliberately selected host-skill isolation feature must be explicitly acknowledged at configuration time")
	}
}

func TestExplicitModelRequiredWithoutAutomaticSelection(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty model must fail before invoking the CLI; no default or fallback", err)
	}
	for _, model := range []string{"gpt-5.5", "gpt-6-astra"} {
		args := arguments("workspace", "schema", model)
		if !hasArgumentPair(args, "--model", model) {
			t.Fatal("caller-selected model was replaced or omitted")
		}
		for _, feature := range []string{"code_mode", "code_mode_host"} {
			if !hasArgumentPair(args, "--disable", feature) || hasArgumentPair(args, "--enable", feature) {
				t.Fatal("model selection silently enabled a code execution feature")
			}
		}
	}
}

// This opt-in diagnostic is exactly one real CLI process, no hidden retries.
// Its expected parser failure is preserved; it cannot stand in for Adapter.Run.
func TestCodexExecNativeErrorDiagnostic(t *testing.T) {
	if os.Getenv("PROJECTCTL_CODEXEXEC_DIAGNOSTIC") != "1" {
		t.Skip("separate explicit one-call diagnostic authorization required")
	}
	a, err := New(Config{Model: os.Getenv("PROJECTCTL_CODEXEXEC_MODEL")})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	workspace, schema := filepath.Join(root, "workspace"), filepath.Join(root, "schema.json")
	if os.Mkdir(workspace, 0500) != nil || os.WriteFile(schema, []byte(testSchema), 0600) != nil {
		t.Fatal("temporary input setup failed")
	}
	defer os.Chmod(workspace, 0700)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.binary, arguments(workspace, schema, a.model)...)
	cmd.Dir, cmd.Env = workspace, allowedEnvironment(os.Environ())
	cmd.Stdin = strings.NewReader(`Return only {"ok":true}. Do not use any tools or read any files.`)
	out, diagnostic := newBoundedWriter(maxStdout, cancel), newBoundedWriter(maxStderr, cancel)
	cmd.Stdout, cmd.Stderr = out, diagnostic
	start := time.Now()
	runErr := runProcess(cmd)
	raw := out.bytes()
	t.Logf("cli_success=%t elapsed=%s stdout_bytes=%d stdout_sha256=%x stderr_bytes=%d", runErr == nil, time.Since(start), len(raw), sha256.Sum256(raw), len(diagnostic.bytes()))
	for i, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"item"`
		}
		if json.Unmarshal(line, &event) != nil {
			t.Fatal("invalid native event JSON")
		}
		if event.Type == "item.completed" && event.Item.Type == "error" {
			t.Logf("native_error_line=%d message_bytes=%d message_sha256=%x fixed_categories=%v", i+1, len(event.Item.Message), sha256.Sum256([]byte(event.Item.Message)), nativeErrorCategories(event.Item.Message))
		}
	}
	if runErr != nil || ctx.Err() != nil || out.overflowed() || diagnostic.overflowed() {
		t.Fatal("native execution failed; private diagnostic withheld")
	}
	if _, err := parseEvents(raw); err != nil {
		t.Fatal(err)
	}
}
