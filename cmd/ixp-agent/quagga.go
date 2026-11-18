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
	"regexp"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/routing"
)

type quaggaCLIOptions struct {
	Socket, File, Checksum, BaseChecksum, Transaction string
	Timeout                                           int
	Watchdog                                          routing.QuaggaOptions
}

var quaggaCLIChecksum = regexp.MustCompile(`^[0-9a-f]{64}$`)
var quaggaCLITransaction = regexp.MustCompile(`^[0-9a-f]{48}$`)

func quaggaStartupFlagsUsed(fs *flag.FlagSet) bool {
	used := false
	fs.Visit(func(arg *flag.Flag) {
		switch arg.Name {
		case "binary-checksum", "bgp-port", "bootstrap-file", "bootstrap-checksum":
			used = true
		}
	})
	return used
}

func runQuagga(mode string, fs *flag.FlagSet, o quaggaCLIOptions, output io.Writer) error {
	allowed := map[string]bool{"platform": true, "socket": true}
	switch mode {
	case "serve":
		for _, key := range []string{"validator", "binary-checksum", "state-dir", "bgp-port", "exclusive-instance-acknowledged", "bootstrap-file", "bootstrap-checksum"} {
			allowed[key] = true
		}
	case "stage":
		for _, key := range []string{"file", "checksum", "base-checksum", "timeout"} {
			allowed[key] = true
		}
	case "confirm", "cancel":
		allowed["transaction"] = true
		allowed["checksum"] = true
	case "status":
	default:
		return errors.New("Quagga uses supervised serve, stage, status, confirm and cancel")
	}
	var unexpected string
	var explicitStateDir bool
	fs.Visit(func(arg *flag.Flag) {
		if !allowed[arg.Name] && unexpected == "" {
			unexpected = arg.Name
		}
		if arg.Name == "state-dir" {
			explicitStateDir = true
		}
	})
	if unexpected != "" {
		return fmt.Errorf("--%s is not a Quagga %s option", unexpected, mode)
	}
	if o.Socket == "" {
		return errors.New("Quagga requires an explicit --socket")
	}
	if mode == "serve" {
		if !explicitStateDir || o.Watchdog.StateDir == "" || o.Watchdog.Binary == "" || !quaggaCLIChecksum.MatchString(o.Watchdog.BinaryChecksum) || o.Watchdog.BGPPort < 1 || o.Watchdog.BGPPort >= 65535 || !o.Watchdog.ExclusiveInstance {
			return errors.New("Quagga serve requires --state-dir, --validator, --binary-checksum, --bgp-port and --exclusive-instance-acknowledged")
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return routing.ServeQuaggaWatchdog(ctx, o.Watchdog, o.Socket)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	var payload any
	switch mode {
	case "stage":
		if o.File == "" || !quaggaCLIChecksum.MatchString(o.Checksum) || !quaggaCLIChecksum.MatchString(o.BaseChecksum) || o.Checksum == o.BaseChecksum || o.Timeout < 60 || o.Timeout > 3600 {
			return errors.New("Quagga stage requires --file, distinct actual --checksum and --base-checksum, and timeout 60..3600 seconds")
		}
		config, err := routing.ReadQuaggaArtifact(o.File)
		if err != nil {
			return err
		}
		if config == "" || routing.Checksum(config) != o.Checksum {
			return errors.New("Quagga candidate file checksum mismatch")
		}
		payload = routing.QuaggaStageRequest{Config: config, Checksum: o.Checksum, BaseChecksum: o.BaseChecksum, TimeoutSeconds: o.Timeout}
	case "confirm", "cancel":
		if !quaggaCLITransaction.MatchString(o.Transaction) || !quaggaCLIChecksum.MatchString(o.Checksum) {
			return errors.New("Quagga confirm/cancel requires the exact --transaction and --checksum")
		}
		payload = routing.QuaggaDecision{ID: o.Transaction, Checksum: o.Checksum}
	}
	state, err := routing.RequestQuagga(ctx, o.Socket, mode, payload)
	if state.Version == 1 {
		if outErr := json.NewEncoder(output).Encode(state); outErr != nil {
			return errors.Join(err, outErr)
		}
	}
	return err
}
