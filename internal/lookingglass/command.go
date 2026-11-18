package lookingglass

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

type commandBuffer struct {
	bytes.Buffer
	overflow bool
	cancel   context.CancelFunc
}

func (b *commandBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := MaxOutputBytes - b.Len()
	if n > remaining {
		b.overflow = true
		if remaining > 0 {
			b.Buffer.Write(p[:remaining])
		}
		b.cancel()
		return n, nil
	}
	return b.Buffer.Write(p)
}
func nativeCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		e := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if e == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return e
	}
	cmd.WaitDelay = time.Second
	var stdout, stderr commandBuffer
	stdout.cancel = cancel
	stderr.cancel = cancel
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if stdout.overflow || stderr.overflow {
		return nil, ErrOutputLimit
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, errors.New("native read-only command failed")
	}
	// Diagnostics can contain private values. Do not combine them with a valid
	// structured reply and never return their bytes or text to the caller.
	if stderr.Len() != 0 {
		return nil, errors.New("native command emitted diagnostics")
	}
	return stdout.Bytes(), nil
}
func ValidateConfig(c Config) error {
	if !identifier.MatchString(c.RouterID) || len(c.Peers) > 2048 {
		return errors.New("invalid local looking-glass mapping")
	}
	for _, cap := range Capabilities() {
		if cap.Platform == c.Platform {
			goto validPlatform
		}
	}
	return ErrUnsupported
validPlatform:
	seen := map[string]bool{}
	for id, selector := range c.Peers {
		if !identifier.MatchString(id) || seen[selector] {
			return errors.New("invalid or duplicate local peer mapping")
		}
		seen[selector] = true
		if c.Platform == "bird2" || c.Platform == "bird3" {
			if !protocolName.MatchString(selector) {
				return errors.New("BIRD peer mapping must be a literal protocol identifier")
			}
		} else {
			if _, err := canonicalAddress(selector); err != nil {
				return err
			}
		}
	}
	for _, path := range []string{c.Birdc, c.Vtysh, c.Bgpctl, c.BirdSocket, c.VtySocket, c.BgpSocket} {
		if path != "" && (!filepath.IsAbs(path) || filepath.Clean(path) != path) {
			return errors.New("local executables and sockets require absolute clean paths")
		}
	}
	return nil
}
