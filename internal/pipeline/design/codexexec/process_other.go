//go:build !darwin && !linux

package codexexec

import (
	"os/exec"
	"time"
)

func configureProcess(cmd *exec.Cmd) {
	// CommandContext kills the direct process. Windows process-tree isolation is
	// an additional host job-object boundary, not claimed by this adapter.
	cmd.WaitDelay = time.Second
}

func runProcess(cmd *exec.Cmd) error {
	configureProcess(cmd)
	return cmd.Run()
}
