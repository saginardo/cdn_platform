package control

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

const resticInterruptGracePeriod = 10 * time.Second

func resticCommandContext(ctx context.Context, binary string, arguments ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		err := command.Process.Signal(os.Interrupt)
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	command.WaitDelay = resticInterruptGracePeriod
	return command
}
