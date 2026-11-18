package routing

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var quaggaVersionLine = regexp.MustCompile(`(?m)^[A-Za-z0-9._-]+ version 1\.2\.4\r?$`)

func quaggaVersionSupported(output string) bool {
	return quaggaVersionLine.MatchString(output) && strings.Contains(output, "Copyright 1996-2005 Kunihiro Ishiguro, et al.") && !strings.Contains(strings.ToLower(output), "frrouting")
}

// Stock Quagga's -C can create its BGP listening socket while parsing a router
// declaration. Unlike FRR, -p 0 means port 179. Every invocation therefore gets
// a mandatory private user/network namespace and an empty allowlisted env.
func runQuagga(ctx context.Context, binary, dir string, args ...string) (string, error) {
	return runQuaggaCommand(ctx, exec.CommandContext(ctx, binary, args...), dir, true)
}

func runQuaggaCommand(ctx context.Context, cmd *exec.Cmd, dir string, isolated bool) (string, error) {
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir, "LANG=C", "LC_ALL=C"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if isolated {
		cmd.SysProcAttr.Cloneflags = syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET
		cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: os.Geteuid(), HostID: os.Geteuid(), Size: 1}}
		cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getegid(), HostID: os.Getegid(), Size: 1}}
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
		return "", errors.New("Quagga native output exceeded 64 KiB")
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return output.String(), err
}

func ValidateQuagga(ctx context.Context, binary, config string) error {
	if binary == "" {
		binary = "quagga-bgpd"
	}
	return validateQuaggaConfig(ctx, config, func(ctx context.Context, dir string, args ...string) (string, error) {
		return runQuagga(ctx, binary, dir, args...)
	})
}

func validateQuaggaConfig(ctx context.Context, config string, run func(context.Context, string, ...string) (string, error)) error {
	if err := ValidateManagedQuagga(config); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "moedove-quagga-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	version, err := run(ctx, dir, "--version")
	if err != nil {
		return nativeFailure("Quagga native validation executable unavailable", err)
	}
	if !quaggaVersionSupported(version) {
		return errors.New("validation requires actual stock-compatible Quagga bgpd 1.2.4; FRR and other versions are not substitutes")
	}
	for _, probe := range []struct {
		name, content string
		reject        bool
	}{{"reject.conf", "this-is-not-a-quagga-command\n", true}, {"candidate.conf", config, false}} {
		file := filepath.Join(dir, probe.name)
		if err := os.WriteFile(file, []byte(probe.content), 0600); err != nil {
			return err
		}
		output, parseErr := run(ctx, dir, "--dryrun", "--skip_runas", "--no_kernel", "--bgp_port", "65535", "--listenon", "127.0.0.1", "--vty_port", "0", "--socket", filepath.Join(dir, "unused-zebra.sock"), "--pid_file", filepath.Join(dir, "unused.pid"), "--config_file", file)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if probe.reject {
			if parseErr == nil && strings.TrimSpace(output) == "" {
				return errors.New("Quagga checker accepted an invalid command; native syntax validation unavailable")
			}
			continue
		}
		if parseErr != nil {
			return nativeFailure("Quagga 1.2.4 native dry-run rejected managed configuration", parseErr)
		}
		// Quagga can report a rejected command and still complete a dry run.
		// A generated profile must parse without diagnostics; never return raw
		// output, which can echo a private neighbor password or configuration.
		if strings.TrimSpace(output) != "" {
			return errors.New("Quagga native dry-run emitted diagnostics; raw configuration withheld")
		}
	}
	return nil
}
