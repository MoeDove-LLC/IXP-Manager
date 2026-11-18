package routing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

const maxConfigSize = 16 << 20

type boundedOutput struct {
	bytes.Buffer
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len()+n > 65536 {
		b.truncated = true
	}
	if b.Len() < 65536 {
		remaining := 65536 - b.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}
func run(ctx context.Context, binary string, args ...string) (string, error) {
	return runNative(ctx, binary, args, false)
}
func runNative(ctx context.Context, binary string, args []string, isolatedNetwork bool) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	// Reload helpers spawn vtysh. Kill the whole process group on cancellation,
	// otherwise a surviving child could keep applying commands during rollback.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if isolatedNetwork {
		cmd.SysProcAttr.Cloneflags = syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET
		cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: os.Geteuid(), HostID: os.Geteuid(), Size: 1}}
		cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getegid(), HostID: os.Getegid(), Size: 1}}
		cmd.SysProcAttr.GidMappingsEnableSetgroups = false
	}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second

	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if output.truncated {
		return output.String(), errors.New("native command output exceeded 64 KiB")
	}
	if ctx.Err() != nil {
		return output.String(), ctx.Err()
	}
	return output.String(), err
}

// Validate requires the actual target major version. Missing tools fail closed;
// the caller must never label an unparsed candidate as validated.
func Validate(ctx context.Context, binary, config string) error {
	return validateBIRDMajor(ctx, binary, config, 2)
}

var birdVersionLine = regexp.MustCompile(`(?m)^BIRD(?: version)? ([0-9]+)\.[0-9]+(?:\.[0-9]+)?[^\r\n]*$`)

func validateBIRDMajor(ctx context.Context, binary, config string, major int) error {
	major, err := birdMajor(major)
	if err != nil {
		return err
	}
	if err := rejectRedactedArtifact(config); err != nil {
		return err
	}
	if err := validateBIRDArtifactMajor(config, major); err != nil {
		return err
	}
	if len(config) == 0 || len(config) > maxConfigSize {
		return errors.New("configuration is empty or exceeds 16 MiB")
	}
	if binary == "" {
		binary = "bird"
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	version, err := run(ctx, binary, "--version")
	if err != nil {
		return nativeFailure(fmt.Sprintf("BIRD %d executable unavailable", major), err)
	}
	match := birdVersionLine.FindStringSubmatch(version)
	if match == nil || match[1] != strconv.Itoa(major) {
		return fmt.Errorf("validation requires BIRD %d; installed major version does not match", major)
	}
	f, err := os.CreateTemp("", "moedove-bird-*.conf")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err = io.WriteString(f, config); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	_, err = run(ctx, binary, "-p", "-c", path)
	if err != nil {
		return nativeFailure("BIRD parse rejected configuration", err)
	}
	return nil
}
