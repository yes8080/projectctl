package codexexec

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const testSchema = `{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`

// The Go test binary doubles as the fake CLI, including on Windows. It uses no
// shell scripts, network, installed Codex executable, or API credentials.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		if strings.Contains(filepath.Base(os.Args[0]), "wrong-version") {
			fmt.Println("codex-cli 0.0.0")
		} else {
			fmt.Println(Version)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "--no-daemon" {
		fakeCLI()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fakeRequest struct {
	Mode   string `json:"mode"`
	Signal string `json:"signal"`
}
type fakeObservation struct {
	Workspace string   `json:"workspace"`
	Arguments []string `json:"arguments"`
	Names     []string `json:"environment_names"`
	Files     int      `json:"workspace_files"`
	ReadOnly  bool     `json:"read_only"`
	ThreadID  string   `json:"thread_id"`
	Prompt    string   `json:"prompt"`
}

func fakeCLI() {
	input, _ := io.ReadAll(io.LimitReader(os.Stdin, maxPrompt+1))
	var request fakeRequest
	_ = json.Unmarshal(input, &request)
	workspace, _ := os.Getwd()
	if request.Signal != "" {
		_ = os.WriteFile(request.Signal, []byte(workspace), 0600)
	}
	switch request.Mode {
	case "timeout":
		time.Sleep(time.Minute)
	case "stdout flood":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), maxStdout+1024))
		return
	case "stderr flood":
		_, _ = os.Stderr.Write(bytes.Repeat([]byte("x"), maxStderr+1024))
		return
	case "failed":
		fmt.Fprintln(os.Stderr, "private-provider-detail SECRET_TOKEN")
		os.Exit(3)
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	hexID := hex.EncodeToString(id[:])
	thread := hexID[:8] + "-" + hexID[8:12] + "-" + hexID[12:16] + "-" + hexID[16:20] + "-" + hexID[20:]
	emit := func(value any) { _ = json.NewEncoder(os.Stdout).Encode(value) }
	emit(map[string]any{"type": "thread.started", "thread_id": thread})
	emit(map[string]any{"type": "turn.started"})
	if request.Mode == "tool" {
		emit(map[string]any{"type": "item.started", "item": map[string]any{"type": "command_execution", "command": "SECRET_TOKEN"}})
		return
	}
	observation := fakeObservation{Workspace: workspace, Arguments: os.Args[1:], ThreadID: "model-claimed-not-runtime", Prompt: string(input)}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		observation.Names = append(observation.Names, name)
	}
	files, _ := os.ReadDir(workspace)
	observation.Files = len(files)
	if info, err := os.Stat(workspace); err == nil {
		observation.ReadOnly = info.Mode().Perm()&0222 == 0
	}
	output, _ := json.Marshal(observation)
	if request.Mode == "output flood" {
		output = []byte(`{"text":"` + strings.Repeat("x", maxOutput+1) + `"}`)
	}
	emit(map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": string(output)}})
	emit(map[string]any{"type": "turn.completed", "usage": map[string]any{"input_tokens": 17, "output_tokens": 23}})
}

func fakeAdapter(t *testing.T) *Adapter {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{Binary: binary, Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAdapterFreshIsolatedBoundedWorker(t *testing.T) {
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN", "OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN", "OPENAI_BASE_URL", "HTTP_PROXY", "HTTPS_PROXY", "DYLD_INSERT_LIBRARIES", "LD_PRELOAD", "AWS_SECRET_ACCESS_KEY", "SSH_AUTH_SOCK"} {
		t.Setenv(name, "SECRET_TOKEN")
	}
	a := fakeAdapter(t)
	var previous Result
	var previousWorkspace string
	for i := 0; i < 2; i++ {
		got, err := a.Run(context.Background(), Request{Prompt: `{"mode":"normal"}`, Schema: []byte(testSchema), Timeout: 5 * time.Second})
		if err != nil || got.InputTokens != 17 || got.OutputTokens != 23 || !threadPattern.MatchString(got.ThreadID) {
			t.Fatalf("worker failed: %+v %v", got, err)
		}
		var observation fakeObservation
		if err := json.Unmarshal(got.Output, &observation); err != nil {
			t.Fatal(err)
		}
		if observation.ThreadID == got.ThreadID || observation.Files != 0 || (runtime.GOOS != "windows" && !observation.ReadOnly) {
			t.Fatalf("fake identity or nonempty/writable workspace: %+v", observation)
		}
		if _, err := os.Stat(filepath.Dir(observation.Workspace)); !os.IsNotExist(err) {
			t.Fatalf("private temporary directory was not cleaned: %v", err)
		}
		if previous.ThreadID == got.ThreadID || previousWorkspace == observation.Workspace {
			t.Fatal("run reused previous thread/workspace")
		}
		args := observation.Arguments
		for _, flag := range []string{"--no-daemon", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check", "--json", "--output-schema"} {
			if !hasArgument(args, flag) {
				t.Fatalf("missing required flag %s", flag)
			}
		}
		for _, bad := range []string{"resume", "fork", "--last", "--worktree", "--add-dir", "--dangerously-bypass-approvals-and-sandbox", observation.Prompt} {
			if hasArgument(args, bad) {
				t.Fatalf("unsafe or prompt-bearing argv: %s", bad)
			}
		}
		for _, pair := range [][2]string{{"--sandbox", "read-only"}, {"--ask-for-approval", "never"}, {"--model", "test-model"}, {"--config", `forced_login_method="chatgpt"`}, {"--config", `mcp_servers={}`}, {"--config", `project_doc_max_bytes=0`}, {"--config", `web_search="disabled"`}, {"--disable", "shell_tool"}, {"--disable", "plugins"}, {"--disable", "apps"}, {"--disable", "hooks"}, {"--disable", "multi_agent"}, {"--enable", "skip_host_skill_discovery"}} {
			if !hasArgumentPair(args, pair[0], pair[1]) {
				t.Fatalf("missing restriction %v", pair)
			}
		}
		for _, name := range observation.Names {
			if strings.Contains(name, "TOKEN") || strings.Contains(name, "KEY") || name == "HTTP_PROXY" || name == "HTTPS_PROXY" || name == "OPENAI_BASE_URL" || name == "DYLD_INSERT_LIBRARIES" || name == "LD_PRELOAD" || name == "SSH_AUTH_SOCK" {
				t.Fatalf("credential/override inherited: %s", name)
			}
		}
		previous, previousWorkspace = got, observation.Workspace
	}
}

func hasArgument(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
func hasArgumentPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestAdapterFailureLimitsTimeoutAndCleanup(t *testing.T) {
	a := fakeAdapter(t)
	for _, test := range []struct {
		mode string
		want error
	}{
		{"stdout flood", ErrLimit}, {"stderr flood", ErrLimit}, {"output flood", ErrEvent}, {"failed", ErrRuntime}, {"tool", ErrEvent}, {"timeout", context.DeadlineExceeded},
	} {
		t.Run(test.mode, func(t *testing.T) {
			signal := filepath.Join(t.TempDir(), "workspace-path")
			prompt, _ := json.Marshal(fakeRequest{Mode: test.mode, Signal: signal})
			duration := 5 * time.Second
			if test.mode == "timeout" {
				duration = 300 * time.Millisecond
			}
			start := time.Now()
			got, err := a.Run(context.Background(), Request{Prompt: string(prompt), Schema: []byte(testSchema), Timeout: duration})
			if !errors.Is(err, test.want) || got.ThreadID != "" || got.Output != nil || strings.Contains(err.Error(), "SECRET_TOKEN") || strings.Contains(err.Error(), "private-provider-detail") {
				t.Fatalf("unexpected failure or leaked output: %+v %v", got, err)
			}
			if time.Since(start) > 5*time.Second {
				t.Fatal("termination did not bound the process")
			}
			workspace, err := os.ReadFile(signal)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Dir(string(workspace))); !os.IsNotExist(err) {
				t.Fatalf("failed run retained temporary workspace: %v", err)
			}
		})
	}
}

func TestAdapterRejectsRequestsBeforeExecution(t *testing.T) {
	a := fakeAdapter(t)
	for _, schema := range []string{"", "null", "[]", `{"type":"array"}`, `{"type":"object","type":"object"}`, `{"type":"object","properties":{"x":{"type":"string","type":"number"}}}`, `{"type":"object"} {}`, strings.Repeat(" ", maxSchema+1)} {
		if _, err := a.Run(context.Background(), Request{Prompt: "input", Schema: []byte(schema), Timeout: time.Second}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad schema accepted: %v", err)
		}
	}
	for _, mutate := range []func(*Request){
		func(r *Request) { r.Prompt = "" }, func(r *Request) { r.Prompt = "\x00" }, func(r *Request) { r.Prompt = string([]byte{0xff}) }, func(r *Request) { r.Prompt = strings.Repeat("x", maxPrompt+1) }, func(r *Request) { r.Timeout = 0 }, func(r *Request) { r.Timeout = -1 }, func(r *Request) { r.Timeout = maxTimeout + time.Second },
	} {
		r := Request{Prompt: "input", Schema: []byte(testSchema), Timeout: time.Second}
		mutate(&r)
		if _, err := a.Run(context.Background(), r); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Run(ctx, Request{Prompt: "input", Schema: []byte(testSchema), Timeout: time.Second}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := (*Adapter)(nil).Run(context.Background(), Request{}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestAdapterRequiresTrustedAbsolutePinnedBinary(t *testing.T) {
	for _, config := range []Config{{Binary: "codex", Model: "test-model"}, {Binary: t.TempDir(), Model: "test-model"}, {Binary: filepath.Join(t.TempDir(), "missing"), Model: "test-model"}, {Model: "bad\nmodel"}} {
		if _, err := New(config); err == nil {
			t.Fatal("invalid executable configuration accepted")
		}
	}
	binary, _ := os.Executable()
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	wrong := filepath.Join(t.TempDir(), "wrong-version")
	if runtime.GOOS == "windows" {
		wrong += ".exe"
	}
	if err := os.WriteFile(wrong, data, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Binary: wrong, Model: "test-model"}); !errors.Is(err, ErrRuntime) {
		t.Fatal("unexpected CLI version accepted")
	}
}

func TestEnvironmentAllowlist(t *testing.T) {
	got := allowedEnvironment([]string{"PATH=/bin", "HOME=/host", "CODEX_HOME=/auth-handle", "SystemRoot=C:\\Windows", "GH_TOKEN=secret", "OPENAI_API_KEY=secret", "CODEX_ACCESS_TOKEN=secret", "DYLD_LIBRARY_PATH=/evil", "HTTP_PROXY=http://secret", "UNRELATED=value", "path=/other"})
	want := []string{"PATH=/bin", "HOME=/host", "CODEX_HOME=/auth-handle", "SystemRoot=C:\\Windows", "TERM=dumb", "NO_COLOR=1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("unexpected allowed environment: %v", got)
	}
}

// Opt-in only with a separately authorized, explicitly selected model. Default
// package tests never start a real model, choose a fallback, or change auth.
func TestCodexExecIntegration(t *testing.T) {
	if os.Getenv("PROJECTCTL_CODEXEXEC_LIVE") != "1" {
		t.Skip("explicit opt-in required; uses existing ChatGPT entitlement")
	}
	a, err := New(Config{Model: os.Getenv("PROJECTCTL_CODEXEXEC_MODEL")})
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Run(context.Background(), Request{Prompt: `Return only {"ok":true}. Do not use any tools or read any files.`, Schema: []byte(testSchema), Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal(result.Output, &output) != nil || !output.OK || !threadPattern.MatchString(result.ThreadID) {
		t.Fatal("runtime identity or exact output absent")
	}
	t.Logf("runtime thread=%s input_tokens=%d output_tokens=%d", result.ThreadID, result.InputTokens, result.OutputTokens)
}
