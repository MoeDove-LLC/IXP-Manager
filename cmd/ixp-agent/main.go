// ixp-agent runs on the router host; no root privileges are given to the web app.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/moedove/ixp-manager/internal/routing"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New(agentUsage)
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		fmt.Println(agentUsage)
		return nil
	}
	if args[0] == "fetch" {
		return runFetch(args[1:], os.Stdin, os.Stdout)
	}
	valid := map[string]bool{"check": true, "apply": true, "recover": true, "serve": true, "stage": true, "status": true, "confirm": true, "cancel": true, "bootstrap": true}
	if !valid[args[0]] {
		return errors.New(agentUsage)
	}
	mode := args[0]
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	platform := fs.String("platform", "", "explicit target platform: bird1, bird2, bird3, frr, quagga, junos or openbgpd")
	validator := fs.String("validator", "", "native validator executable for selected platform")
	file := fs.String("file", "", "downloaded configuration artifact")
	checksum := fs.String("checksum", "", "file SHA-256 from the authorized config download (not the approval identifier)")
	target := fs.String("target", "", "existing config: bird2/bird3=/etc/bird/bird.conf, frr=/etc/frr/bgpd.conf")
	bird := fs.String("bird", "bird", "BIRD executable matching the explicit platform major")
	birdc := fs.String("birdc", "birdc", "BIRD control executable")
	socket := fs.String("socket", "", "BIRD control socket or FRR/Quagga watchdog Unix socket")
	stateDir := fs.String("state-dir", "/var/lib/moedove-frr", "private watchdog journal directory; explicitly supplied for Quagga")
	reload := fs.String("reload", "/usr/lib/frr/frr-reload.py", "FRR reload helper; fixed by watchdog administrator")
	vtysh := fs.String("vtysh", "/usr/bin/vtysh", "vtysh executable; fixed by watchdog administrator")
	vtySocket := fs.String("vty-socket", "/run/frr", "dedicated bgpd VTY socket directory")
	base := fs.String("base-checksum", "", "reviewed current configuration SHA-256 for BIRD1 apply/recover, FRR stage/bootstrap or Quagga stage")
	exclusive := fs.Bool("exclusive-instance-acknowledged", false, "BIRD1/Quagga: acknowledge continuous exclusive control; Quagga replacements interrupt BGP sessions")
	transaction := fs.String("transaction", "", "pending FRR or Quagga transaction ID to confirm/cancel")
	timeout := fs.Int("timeout", 120, "confirmation deadline: FRR/Quagga 60..3600 seconds; BIRD1 native undo 2..300 seconds")
	binaryChecksum := fs.String("binary-checksum", "", "Quagga serve: exact SHA-256 of the native bgpd executable")
	bgpPort := fs.Int("bgp-port", 0, "Quagga serve: explicit BGP listening port (1..65534)")
	bootstrapFile := fs.String("bootstrap-file", "", "Quagga initial serve: reviewed private bootstrap file")
	bootstrapChecksum := fs.String("bootstrap-checksum", "", "Quagga initial serve: actual SHA-256 of the bootstrap file")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *platform == "" {
		return fmt.Errorf("--platform is required; never infer the target from a filename")
	}
	if *platform == "quagga" && mode != "check" {
		return runQuagga(mode, fs, quaggaCLIOptions{
			Socket: *socket, File: *file, Checksum: *checksum, BaseChecksum: *base, Transaction: *transaction, Timeout: *timeout,
			Watchdog: routing.QuaggaOptions{StateDir: *stateDir, Binary: *validator, BinaryChecksum: *binaryChecksum,
				BGPPort: *bgpPort, BootstrapFile: *bootstrapFile, BootstrapChecksum: *bootstrapChecksum, ExclusiveInstance: *exclusive},
		}, os.Stdout)
	}
	if quaggaStartupFlagsUsed(fs) {
		return errors.New("Quagga startup settings require serve --platform quagga")
	}
	if *platform == "bird1" && mode != "check" {
		native := *validator
		if native == "" {
			native = *bird
		}
		return runBIRD1(mode, *file, routing.BIRD1ApplyOptions{BirdBinary: native, Target: *target, Socket: *socket, Checksum: *checksum, BaseChecksum: *base, ExclusiveInstanceAcknowledged: *exclusive, TimeoutSeconds: *timeout})
	}
	if *target == "" {
		if *platform == "frr" {
			*target = "/etc/frr/bgpd.conf"
		} else {
			*target = "/etc/bird/bird.conf"
		}
	}
	if *platform == "frr" && mode != "check" {
		return runFRR(mode, frrCLIOptions{Target: *target, Socket: *socket, StateDir: *stateDir, Validator: *validator, Reload: *reload, Vtysh: *vtysh, VtySocket: *vtySocket, File: *file, Checksum: *checksum, BaseChecksum: *base, Transaction: *transaction, Timeout: *timeout})
	}
	if mode != "check" && mode != "apply" && mode != "recover" {
		return errors.New("serve/stage/status/confirm/cancel require --platform frr or quagga; bootstrap requires frr")
	}
	if mode != "check" && *platform != "bird2" && *platform != "bird3" {
		return fmt.Errorf("ixp-agent apply/recover only supports bird2/bird3; Junos requires ixp-netconf candidate transaction, other platforms have no automatic apply adapter")
	}
	major := 2
	if *platform == "bird3" {
		major = 3
	}
	birdValidator := *bird
	if *validator != "" {
		birdValidator = *validator
	}
	if mode == "recover" {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := routing.Recover(ctx, routing.ApplyOptions{BirdMajor: major, BirdBinary: birdValidator, BirdcBinary: *birdc, Socket: *socket, Target: *target}); err != nil {
			return err
		}
		fmt.Println("Interrupted deployment recovered; previous configuration restored.")
		return nil
	}
	if *file == "" || *checksum == "" {
		return fmt.Errorf("--file and --checksum are required")
	}
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer f.Close()
	config, err := io.ReadAll(io.LimitReader(f, 16<<20+1))
	if err != nil {
		return err
	}
	if len(config) > 16<<20 {
		return fmt.Errorf("configuration exceeds 16 MiB")
	}
	if routing.Checksum(string(config)) != *checksum {
		return fmt.Errorf("configuration checksum mismatch")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if mode == "check" {
		native := *validator
		if native == "" && (*platform == "bird2" || *platform == "bird3") {
			native = *bird
		}
		err = routing.ValidateFor(ctx, *platform, native, string(config))
	} else {
		err = routing.Apply(ctx, string(config), routing.ApplyOptions{BirdMajor: major, BirdBinary: birdValidator, BirdcBinary: *birdc, Socket: *socket, Target: *target, Checksum: *checksum})
	}
	if err != nil {
		return err
	}
	if mode == "check" {
		fmt.Printf("Checksum and %s validation passed for %s; no configuration changed.\n", routing.ValidationLevel(*platform), *platform)
	} else {
		fmt.Println("Configuration installed and BIRD reconfigured; previous file retained as .moedove-previous.")
	}
	return nil
}
