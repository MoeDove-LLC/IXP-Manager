// ixp-openbgpd provides an explicitly supervised local OpenBGPD transaction.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/routing"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, output io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Fprintln(output, `Usage: ixp-openbgpd check|serve|stage|status|confirm|cancel [flags]
check verifies exact bytes and the real OpenBGPD offline parser.
serve requires a dedicated Linux bgpd parent PID, managed bgpd.conf, private
control socket, exclusive-instance acknowledgment and an external restart supervisor.
stage reloads but does not confirm. confirm follows operator health review.
cancel/timeout/restart recover the durable prior file and request a reload.
This is local supervised recovery, not OpenBGPD-native confirmed commit.
Use <command> --help for flags.`)
		return nil
	}
	mode := args[0]
	if mode != "check" && mode != "serve" && mode != "stage" && mode != "status" && mode != "confirm" && mode != "cancel" {
		return errors.New("unsupported command")
	}
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(output)
	socket := fs.String("socket", "/run/moedove-openbgpd-agent/agent.sock", "private watchdog Unix socket")
	target := fs.String("target", "/etc/moedove-openbgpd/bgpd.conf", "dedicated native configuration path")
	stateDir := fs.String("state-dir", "/var/lib/moedove-openbgpd", "private durable recovery directory")
	validator := fs.String("bgpd", "/usr/sbin/bgpd", "OpenBGPD 8.8+ executable, also pinned daemon identity")
	control := fs.String("bgpctl", "/usr/sbin/bgpctl", "OpenBGPD control executable")
	pid := fs.Int("daemon-pid", 0, "optional expected root parent PID; default derives it from private socket credentials then verifies binary and -f target")
	exclusive := fs.Bool("exclusive-instance", false, "acknowledge that this agent exclusively manages the instance; no out-of-band config/network/fib commands")
	file := fs.String("file", "", "private reviewed configuration file")
	checksum := fs.String("checksum", "", "actual approved file SHA-256, not a review token")
	base := fs.String("base-checksum", "", "expected current disk SHA-256")
	id := fs.String("transaction", "", "exact pending transaction ID")
	timeout := fs.Int("timeout", 120, "confirmation deadline in seconds (60..3600)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid flags; use command --help")
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if mode == "serve" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return routing.ServeOpenBGPDWatchdog(ctx, routing.OpenBGPDOptions{Target: *target, StateDir: *stateDir, Validator: *validator, BGPCTL: *control, DaemonPID: *pid, ExclusiveInstance: *exclusive}, *socket)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	var payload any
	if mode == "check" || mode == "stage" {
		if *file == "" || len(*checksum) != 64 {
			return errors.New("--file and actual --checksum are required")
		}
		config, err := readArtifact(*file)
		if err != nil {
			return err
		}
		if routing.Checksum(config) != *checksum {
			return errors.New("file checksum mismatch")
		}
		if mode == "check" {
			if err = routing.ValidateOpenBGPD(ctx, *validator, config); err != nil {
				return err
			}
			fmt.Fprintln(output, "Native OpenBGPD syntax check passed. No daemon was contacted or configuration applied.")
			return nil
		}
		if len(*base) != 64 {
			return errors.New("--base-checksum is required")
		}
		payload = routing.OpenBGPDStageRequest{Config: config, Checksum: *checksum, BaseChecksum: *base, TimeoutSeconds: *timeout}
	} else if mode == "confirm" || mode == "cancel" {
		if *id == "" || len(*checksum) != 64 {
			return errors.New("--transaction and --checksum are required")
		}
		payload = routing.OpenBGPDDecision{ID: *id, Checksum: *checksum}
	}
	state, err := routing.RequestOpenBGPD(ctx, *socket, mode, payload)
	if state.Version != 0 {
		if e := json.NewEncoder(output).Encode(state); e != nil {
			return errors.Join(err, e)
		}
	}
	return err
}
func readArtifact(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", errors.New("artifact unavailable")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 16<<20 {
		return "", errors.New("artifact must be a private regular file of at most16 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil || len(raw) > 16<<20 {
		return "", errors.New("artifact read failed or exceeded limit")
	}
	return string(raw), nil
}
