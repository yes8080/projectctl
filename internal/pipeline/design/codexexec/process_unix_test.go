//go:build darwin || linux

package codexexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These subprocesses are synthetic; no installed CLI, model or network is used.
func init() {
	if len(os.Args) < 2 {
		return
	}
	if os.Args[1] == "--owned-process-child" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if os.Args[1] == "--owned-process-parent" {
		child := exec.Command(os.Args[0], "--owned-process-child")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if child.Start() != nil {
			os.Exit(2)
		}
		fmt.Println(child.Process.Pid)
		if len(os.Args) > 2 && os.Args[2] == "wait" {
			time.Sleep(time.Minute)
		}
		os.Exit(0)
	}
	if filepath.Base(os.Args[0]) != "owned-process-fixture" || os.Args[1] != "--no-daemon" {
		return
	}
	input, _ := io.ReadAll(os.Stdin)
	var request fakeRequest
	if json.Unmarshal(input, &request) != nil {
		os.Exit(2)
	}
	child := exec.Command(os.Args[0], "--owned-process-child")
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if request.Mode == "child closes pipes" {
		child.Stdout, child.Stderr = nil, nil
	}
	if child.Start() != nil {
		os.Exit(2)
	}
	workspace, _ := os.Getwd()
	data, _ := json.Marshal(ownedProcessObservation{Child: child.Process.Pid, Leader: os.Getpid(), Workspace: workspace})
	if os.WriteFile(request.Signal, data, 0600) != nil {
		os.Exit(2)
	}
	switch request.Mode {
	case "nonzero":
		os.Exit(3)
	case "timeout", "cancel":
		time.Sleep(time.Minute)
	case "stdout limit":
		fmt.Print(strings.Repeat("x", maxStdout+1024))
		time.Sleep(time.Minute)
	case "stderr limit":
		fmt.Fprint(os.Stderr, strings.Repeat("x", maxStderr+1024))
		time.Sleep(time.Minute)
	default:
		fmt.Print(testEvents())
	}
	os.Exit(0)
}

type ownedProcessObservation struct {
	Child     int
	Leader    int
	Workspace string
}

func ownedProcessAdapter(t *testing.T) *Adapter {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "owned-process-fixture")
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{Binary: path, Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func ownedProcessRunning(t *testing.T, pid int) bool {
	t.Helper()
	data, err := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return false
		}
		t.Fatalf("cannot inspect owned process %d: %v", pid, err)
	}
	state := strings.TrimSpace(string(data))
	return state != "" && !strings.HasPrefix(state, "Z")
}

func TestOwnedProcessGroupEveryReturn(t *testing.T) {
	a := ownedProcessAdapter(t)
	for _, mode := range []string{"parent exits", "child closes pipes", "nonzero", "timeout", "cancel", "stdout limit", "stderr limit"} {
		t.Run(mode, func(t *testing.T) {
			signal := filepath.Join(t.TempDir(), "owned.json")
			prompt, _ := json.Marshal(fakeRequest{Mode: mode, Signal: signal})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout := 5 * time.Second
			if mode == "timeout" {
				timeout = 300 * time.Millisecond
			}
			cancelDone := make(chan struct{})
			if mode == "cancel" {
				go func() {
					defer close(cancelDone)
					ticker := time.NewTicker(time.Millisecond)
					defer ticker.Stop()
					for {
						if _, err := os.Stat(signal); err == nil {
							cancel()
							return
						}
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
						}
					}
				}()
			} else {
				close(cancelDone)
			}
			_, runErr := a.Run(ctx, Request{Prompt: string(prompt), Schema: []byte(testSchema), Timeout: timeout})
			cancel()
			<-cancelDone
			data, err := os.ReadFile(signal)
			if err != nil {
				t.Fatal(err)
			}
			var got ownedProcessObservation
			if json.Unmarshal(data, &got) != nil || got.Child <= 1 || got.Leader <= 1 {
				t.Fatal("invalid fixture process identity")
			}
			// Cleanup only the synthetic child, including when exercising the RED baseline.
			defer func() {
				group, groupErr := syscall.Getpgid(got.Child)
				if groupErr == nil && group == got.Leader && ownedProcessRunning(t, got.Child) {
					_ = syscall.Kill(got.Child, syscall.SIGKILL)
				}
			}()
			if ownedProcessRunning(t, got.Child) {
				t.Fatalf("owned child survived Adapter.Run (mode=%s, err=%v)", mode, runErr)
			}
			if syscall.Kill(got.Leader, 0) != syscall.ESRCH {
				t.Fatal("direct child was not reaped")
			}
			if _, err := os.Stat(filepath.Dir(got.Workspace)); !os.IsNotExist(err) {
				t.Fatalf("workspace remained: %v", err)
			}
			switch mode {
			case "parent exits", "child closes pipes":
				if runErr != nil {
					t.Fatalf("valid completed runtime failed: %v", runErr)
				}
			case "nonzero":
				if !errors.Is(runErr, ErrRuntime) {
					t.Fatalf("nonzero: %v", runErr)
				}
			case "timeout":
				if !errors.Is(runErr, context.DeadlineExceeded) {
					t.Fatalf("timeout: %v", runErr)
				}
			case "cancel":
				if !errors.Is(runErr, context.Canceled) {
					t.Fatalf("cancel: %v", runErr)
				}
			default:
				if !errors.Is(runErr, ErrLimit) {
					t.Fatalf("limit: %v", runErr)
				}
			}
		})
	}
}

func TestOwnedProcessVersionLifecycle(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--version")
	out := newBoundedWriter(4096, cancel)
	cmd.Stdout, cmd.Stderr = out, out
	if err := runProcess(cmd); err != nil {
		t.Fatalf("version lifecycle: %v, output=%q", err, out.bytes())
	}
	if string(out.bytes()) != Version+"\n" {
		t.Fatalf("version output: %q", out.bytes())
	}
}

func TestOwnedProcessPreservesUnrelatedAndDisarmsBeforeReap(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	unrelated := exec.Command(binary, "--owned-process-child")
	unrelated.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--version")
	if err := runProcess(cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatal("leader not reaped")
	}
	if !ownedProcessRunning(t, unrelated.Process.Pid) {
		t.Fatal("unrelated process was killed")
	}
	// Simulate reuse of the stored process number without needing the kernel
	// to recycle a PID: the disarmed cancellation path must never signal it.
	cmd.Process = unrelated.Process
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("late cancellation was not disarmed: %v", err)
	}
	if !ownedProcessRunning(t, unrelated.Process.Pid) {
		t.Fatal("late cancellation killed an unrelated group")
	}
}

func TestOwnedProcessStartFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(t.TempDir(), "nonexistent"))
	if err := runProcess(cmd); err == nil {
		t.Fatal("missing executable accepted")
	}
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("failed start cancellation: %v", err)
	}
}

func TestOwnedProcessInstalledVersionOptIn(t *testing.T) {
	if os.Getenv("PROJECTCTL_CODEXEXEC_VERSION") != "1" {
		t.Skip("installed CLI version probe is opt-in; no model execution")
	}
	if _, err := New(Config{Model: "version-probe-only"}); err != nil {
		t.Fatalf("installed CLI version probe: %v", err)
	}
}

func TestOwnedProcessUnconfirmedShutdownPreservesWorkspace(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--version")
	// The actual fixture exits without descendants; only the observation is
	// fault-injected, so this test never intentionally leaves a live process.
	err = runProcessWithProbe(cmd, func(context.Context, int) (bool, error) { return false, errors.New("synthetic unavailable probe") })
	if !errors.Is(err, ErrContainment) {
		t.Fatalf("unconfirmed shutdown not blocked: %v", err)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatal("direct child not reaped on containment failure")
	}
	root := filepath.Join(t.TempDir(), "private-run")
	workspace := filepath.Join(root, "workspace")
	if mkdirErr := os.MkdirAll(workspace, 0700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	result := Result{}
	cleanupWorkspace(root, workspace, &result, &err)
	if _, statErr := os.Stat(workspace); statErr != nil {
		t.Fatalf("unconfirmed workspace removed: %v", statErr)
	}
	if !errors.Is(err, ErrContainment) {
		t.Fatal("cleanup lost containment blocker")
	}
}

func TestOwnedProcessSnapshotRequiresPositiveLeaderEvidence(t *testing.T) {
	for _, test := range []struct {
		name, snapshot string
		alive, valid   bool
	}{
		{"empty", "", false, false},
		{"unrelated only", "15 15 S\n", false, false},
		{"partial line", "42 42 Z\n43 42\n", false, false},
		{"wrong group", "42 99 Z\n", false, false},
		{"leader zombie", "42 42 Z\n", false, true},
		{"zombies only", "42 42 Z\n43 42 Z+\n", false, true},
		{"child still live", "42 42 Z\n43 42 S\n", true, true},
		{"leader still live", "42 42 S\n", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			alive, err := runningGroupSnapshot([]byte(test.snapshot), 42)
			if (err == nil) != test.valid || alive != test.alive {
				t.Fatalf("snapshot alive=%v err=%v", alive, err)
			}
		})
	}
}

func TestOwnedProcessRuntimeEntryParentModes(t *testing.T) {
	for _, mode := range []string{"exit", "wait"} {
		t.Run(mode, func(t *testing.T) {
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			timeout := 5 * time.Second
			if mode == "wait" {
				timeout = 300 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "--owned-process-parent", mode)
			out := newBoundedWriter(4096, cancel)
			cmd.Stdout, cmd.Stderr = out, out
			runErr := runProcess(cmd)
			pid, err := strconv.Atoi(strings.TrimSpace(string(out.bytes())))
			if err != nil || pid <= 1 {
				t.Fatal("fixture did not publish its child PID")
			}
			defer func() {
				if group, e := syscall.Getpgid(pid); e == nil && group == cmd.Process.Pid {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}()
			if ownedProcessRunning(t, pid) {
				t.Fatalf("child survived managed runtime entry: %v", runErr)
			}
			if mode == "exit" && runErr != nil {
				t.Fatalf("normal parent exit: %v", runErr)
			}
			if mode == "wait" && runErr == nil {
				t.Fatal("deadline did not fail execution")
			}
			if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
				t.Fatalf("late cancel after reaping: %v", err)
			}
		})
	}
}
