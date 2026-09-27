//go:build darwin || linux

package codexexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// ownedProcess is disarmed before reaping the leader. The unreaped direct
// child anchors its PID/PGID, so no signal can target a subsequently reused ID.
type ownedProcess struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	disarmed bool
}

func configureProcess(cmd *exec.Cmd) *ownedProcess {
	owned := &ownedProcess{cmd: cmd}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = owned.stop
	cmd.WaitDelay = time.Second
	return owned
}

func (p *ownedProcess) stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.disarmed || p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

// runProcess must be the sole waiter for cmd. Calling cmd.Run directly would
// reap the leader before pipe draining and lose this ownership guarantee.
// Descendants must remain in this process group: hostile setsid/setpgid escapes
// require a host containment boundary, not a read-only workspace.
func runProcess(cmd *exec.Cmd) error {
	return runProcessWithProbe(cmd, groupRunning)
}

func runProcessWithProbe(cmd *exec.Cmd, probe func(context.Context, int) (bool, error)) error {
	owned := configureProcess(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	observeErr := observeExit(cmd.Process.Pid)
	var stopErr error
	if errors.Is(observeErr, syscall.ECHILD) {
		// A host-side competing waiter violated sole ownership. Do not signal a
		// number which it may already have released for reuse.
		owned.mu.Lock()
		owned.disarmed = true
		owned.mu.Unlock()
	} else {
		stopErr = owned.quiesce(probe)
	}
	// Group termination precedes Wait, including normal/nonzero leader exit.
	// Wait reaps our direct child and joins the inherited-pipe copy goroutines.
	waitErr := cmd.Wait()
	if observeErr != nil {
		observeErr = fmt.Errorf("observe exit: %w", observeErr)
	}
	if stopErr != nil {
		stopErr = fmt.Errorf("stop owned group: %w", stopErr)
	}
	if observeErr != nil || stopErr != nil {
		return errors.Join(ErrContainment, observeErr, stopErr, waitErr)
	}
	return waitErr
}

// A group kill is not a wait: descendants which closed all inherited pipes
// still need to have stopped before temporary workspace cleanup. The trusted
// host ps utility supplies only native PID/PGID/state metadata (never args or
// environment). The leader remains unreaped throughout every snapshot/signal.
func (p *ownedProcess) quiesce(probe func(context.Context, int) (bool, error)) error {
	defer func() { p.mu.Lock(); p.disarmed = true; p.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		stopErr := p.stop()
		alive, err := probe(ctx, p.cmd.Process.Pid)
		if err != nil {
			return err
		}
		if !alive {
			// Darwin killpg skips zombies and reports EPERM when only zombies
			// remain. A positive native empty/non-running snapshot distinguishes
			// this case from a real permission failure; never ignore it blindly.
			return nil
		}
		if stopErr != nil && !errors.Is(stopErr, os.ErrProcessDone) {
			return stopErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

func groupRunning(ctx context.Context, pgid int) (bool, error) {
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "/bin/ps", "-ax", "-o", "pid=,pgid=,stat=")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	out := newBoundedWriter(2<<20, cancel)
	cmd.Stdout, cmd.Stderr = out, io.Discard
	if err := cmd.Run(); err != nil {
		return false, err
	}
	if out.overflowed() {
		return false, ErrLimit
	}
	return runningGroupSnapshot(out.bytes(), pgid)
}

func runningGroupSnapshot(snapshot []byte, pgid int) (bool, error) {
	seenLeader, alive := false, false
	for _, line := range strings.Split(string(snapshot), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 {
			return false, ErrRuntime
		}
		pid, pidErr := strconv.Atoi(fields[0])
		group, groupErr := strconv.Atoi(fields[1])
		if pidErr != nil || groupErr != nil || pid < 0 || group < 0 {
			return false, ErrRuntime
		}
		if pid == pgid && group == pgid {
			seenLeader = true
		}
		if group == pgid && !strings.HasPrefix(fields[2], "Z") {
			alive = true
		}
	}
	// The unreaped leader must still be present. An empty/partial/filtered
	// snapshot is not positive evidence of successful shutdown.
	if !seenLeader {
		return false, ErrRuntime
	}
	return alive, nil
}

func observeExit(pid int) error {
	for {
		// siginfo_t is at most 128 bytes on these platforms; use an oversized,
		// naturally aligned buffer. Only Darwin's common first three int32
		// fields are inspected (Linux WEXITED cannot report a stopped child).
		var info [32]uint64
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, 1 /* P_PID */, uintptr(pid), uintptr(unsafe.Pointer(&info[0])), uintptr(syscall.WEXITED|syscall.WNOWAIT), 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return errno
		}
		if runtime.GOOS == "darwin" {
			// Darwin can report a stop despite WEXITED (Go issue 19314).
			code := *(*int32)(unsafe.Pointer(uintptr(unsafe.Pointer(&info[0])) + 8))
			if code < 1 || code > 3 {
				continue
			} // CLD_EXITED/KILLED/DUMPED
		}
		return nil
	}
}
