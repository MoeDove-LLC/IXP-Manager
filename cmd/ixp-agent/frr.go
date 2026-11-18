package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/routing"
)

const agentUsage = `Usage:
  ixp-agent fetch --server HTTPS_ORIGIN --deployment ID --router ID --platform bird1|bird2|bird3|frr|junos|openbgpd|quagga --output CONFIG --token-file PRIVATE_FILE
  ixp-agent check --platform bird1|bird2|bird3|frr|junos|openbgpd|quagga --file CONFIG --checksum SHA256
  ixp-agent apply|recover --platform bird2|bird3 [--target /etc/bird/bird.conf] ...
  ixp-agent apply|recover --platform bird1 --validator ABS_BINARY --target PRIVATE_CONF --socket PRIVATE_SOCKET --base-checksum BASE_SHA256 --checksum NEXT_SHA256 --exclusive-instance-acknowledged ...
  ixp-agent serve --platform frr --target /etc/frr/bgpd.conf [--socket /run/moedove-frr/agent.sock]
  ixp-agent stage --platform frr --file CONFIG --checksum SHA256 --base-checksum OLD_SHA256 [--timeout 120]
  ixp-agent status --platform frr
  ixp-agent confirm|cancel --platform frr --transaction ID --checksum SHA256
  ixp-agent bootstrap --platform frr --file CONFIG --checksum SHA256 --base-checksum OLD_SHA256
  ixp-agent serve --platform quagga --validator ABS_BGPD --binary-checksum SHA256 --state-dir ABS_DIR --socket ABS_SOCKET --bgp-port PORT --exclusive-instance-acknowledged [--bootstrap-file PRIVATE_FILE --bootstrap-checksum SHA256]
  ixp-agent status --platform quagga --socket ABS_SOCKET
  ixp-agent stage --platform quagga --socket ABS_SOCKET --file PRIVATE_FILE --checksum SHA256 --base-checksum OLD_SHA256 [--timeout 120]
  ixp-agent confirm|cancel --platform quagga --socket ABS_SOCKET --transaction ID --checksum SHA256

BIRD 1 apply/recover use a separate dedicated v2 transaction. Supply explicit
--validator, --target, --socket, --base-checksum, --checksum and
--exclusive-instance-acknowledged; apply also requires --file. No default target
or cross-family/native-major fallback is allowed. Historical v1 remains check only.
Use <command> --help for flags. FRR requires a supervised local watchdog and a
managed dedicated bgpd instance. Stage is not confirmation. Bootstrap writes disk
only and never reloads a daemon. Quagga uses dedicated supervised process replacement.
Stage and rollback interrupt BGP sessions. Junos uses ixp-netconf; OpenBGPD uses the separate ixp-openbgpd watchdog.`

type frrCLIOptions struct {
	Target, Socket, StateDir, Validator, Reload, Vtysh, VtySocket, File, Checksum, BaseChecksum, Transaction string
	Timeout                                                                                                  int
}

func runFRR(mode string, o frrCLIOptions) error {
	if o.Socket == "" {
		o.Socket = "/run/moedove-frr/agent.sock"
	}
	if o.Validator == "" {
		o.Validator = "/usr/lib/frr/bgpd"
	}
	if mode == "serve" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return routing.ServeFRRWatchdog(ctx, routing.FRROptions{Target: o.Target, StateDir: o.StateDir, Validator: o.Validator, Reload: o.Reload, Vtysh: o.Vtysh, VtySocket: o.VtySocket}, o.Socket)
	}
	if mode == "apply" || mode == "recover" {
		return errors.New("FRR uses supervised stage/confirm/cancel; start serve under a restart supervisor, not one-shot apply")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	var payload any
	switch mode {
	case "stage", "bootstrap":
		if o.File == "" || len(o.Checksum) != 64 || len(o.BaseChecksum) != 64 {
			return errors.New("--file, --checksum and --base-checksum are required")
		}
		config, err := readAgentArtifact(o.File)
		if err != nil {
			return err
		}
		if mode == "bootstrap" {
			if err = routing.BootstrapFRR(ctx, o.Target, o.Validator, config, o.Checksum, o.BaseChecksum); err != nil {
				return err
			}
			fmt.Println("Reviewed managed bgpd.conf installed on disk; original preserved. No running daemon changed. Start a dedicated bgpd instance on this file before serving transactions.")
			return nil
		}
		payload = routing.FRRStageRequest{Config: config, Checksum: o.Checksum, BaseChecksum: o.BaseChecksum, TimeoutSeconds: o.Timeout}
	case "confirm", "cancel":
		if o.Transaction == "" || len(o.Checksum) != 64 {
			return errors.New("--transaction and --checksum are required")
		}
		payload = routing.FRRDecision{ID: o.Transaction, Checksum: o.Checksum}
	case "status":
	default:
		return errors.New("unsupported FRR command")
	}
	state, err := routing.RequestFRR(ctx, o.Socket, mode, payload)
	if state.Version != 0 {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if outErr := enc.Encode(state); outErr != nil {
			return errors.Join(err, outErr)
		}
	}
	return err
}
func readAgentArtifact(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil {
		return "", err
	}
	if len(b) > 16<<20 {
		return "", errors.New("configuration exceeds 16 MiB")
	}
	return string(b), nil
}
