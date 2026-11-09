package managed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// waitClaudeShortCommand is the sole waiter. Cancellation kills only the child
// handle; group cleanup is performed by the pinned-leader waiter before reaping.
func waitClaudeShortCommand(ctx context.Context, cmd *exec.Cmd, cfg ClaudeCodeProcessConfig, path string) error {
	marker, err := processStartMarker(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		cfg.blockUnconfirmedClaudeStop(errClaudeCodeGroupUnconfirmed)
		return &claudeStartCleanupError{err: fmt.Errorf("%w: identify child: %w", errClaudeCodeGroupUnconfirmed, err)}
	}
	identity := ProcessIdentity{PID: cmd.Process.Pid, StartMarker: marker, Incarnation: claudeCodeNewInputID()}
	if path != "" {
		record, readErr := readClaudeCodeOrphan(path)
		if readErr != nil || record.Identity.PID != identity.PID || record.Identity.StartMarker != identity.StartMarker {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			cfg.blockUnconfirmedClaudeStop(errClaudeCodeGroupUnconfirmed)
			return &claudeStartCleanupError{err: errors.Join(errClaudeCodeGroupUnconfirmed, readErr)}
		}
		identity = record.Identity
	}
	done := make(chan error, 1)
	go func() {
		wait := cfg.waitForCleanup
		if wait == nil {
			wait = waitClaudeCodeCommandConfirmed
		}
		confirmed := false
		var removalErr error
		err := wait(cmd, identity, func() {
			confirmed = true
			if path != "" {
				removalErr = removeClaudeCodeOrphan(path, identity)
			}
		})
		if removalErr != nil {
			cfg.blockUnconfirmedClaudeStop(removalErr)
			err = errors.Join(err, &claudeStartCleanupError{err: removalErr})
		}
		if !confirmed {
			cfg.blockUnconfirmedClaudeStop(errClaudeCodeGroupUnconfirmed)
			err = &claudeStartCleanupError{err: errors.Join(errClaudeCodeGroupUnconfirmed, err)}
		}
		done <- err
	}()
	select {
	case err = <-done:
		return err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		err = <-done
		var cleanupErr *claudeStartCleanupError
		if errors.As(err, &cleanupErr) {
			return err
		}
		return ctx.Err()
	}
}

// startClaudeShortCommand holds the executable behind a gate until its group
// identity is durable, so a crash cannot leave an untracked CLI process.
func startClaudeShortCommand(cmd *exec.Cmd, gate *os.File, cfg ClaudeCodeProcessConfig) (string, error) {
	if err := cmd.Start(); err != nil {
		return "", err
	}
	_ = cmd.ExtraFiles[0].Close()
	path := ""
	if cfg.OrphanRoot != "" {
		var err error
		path, err = persistClaudeCodeOrphan(cfg.OrphanRoot, cmd.Process.Pid, claudeCodeNewInputID())
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			cfg.blockUnconfirmedClaudeStop(err)
			return "", &claudeStartCleanupError{err: fmt.Errorf("persist Claude orphan identity: %w", err)}
		}
	}
	release := func() (int, error) { return gate.Write([]byte("ready\n")) }
	var n int
	var releaseErr error
	if cfg.releaseGate != nil {
		n, releaseErr = cfg.releaseGate(release)
	} else {
		n, releaseErr = release()
	}
	if err := releaseErr; err != nil || n != len("ready\n") {
		if err == nil {
			err = io.ErrShortWrite
		}
		_ = cmd.Process.Kill()
		cleanup := waitClaudeShortCommand(context.Background(), cmd, cfg, path)
		return "", errors.Join(err, cleanup)
	}
	return path, nil
}
