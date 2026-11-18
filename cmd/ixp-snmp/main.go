package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	devicesnmp "github.com/moedove/ixp-manager/internal/snmp"
)

const usage = `Usage:
 ixp-snmp discover --config /etc/moedove/snmp.json --token-file /etc/moedove/device.token
 ixp-snmp discover --config /etc/moedove/snmp.json --output discovery.json
 ixp-snmp observe --config /etc/moedove/snmp.json --token-file /etc/moedove/device.token [--once] [--interval 30s]

SNMP credentials are read only from a private0600 local JSON file. v3authPriv
is required unless v2c is explicitly opted in. No SET or arbitrary OID requests.
Discovery upload is a proposal only; an administrator must review inventory mappings.`

var collectDevice = devicesnmp.Collect
var fetchBindings = devicesnmp.FetchBindings
var uploadSnapshot = devicesnmp.Upload

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	if args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(output, usage)
		return nil
	}
	mode := args[0]
	if mode != "discover" && mode != "observe" {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "", "private local SNMP credential/config JSON")
	tokenFile := fs.String("token-file", "", "private scoped device token file")
	tokenStdin := fs.Bool("token-stdin", false, "read scoped device token from stdin")
	target := fs.String("output", "", "private offline discovery snapshot path")
	once := fs.Bool("once", false, "observe once and exit")
	interval := fs.Duration("interval", 30*time.Second, "observation interval5..60seconds")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(output, usage)
			return nil
		}
		return errors.New("invalid flags; secrets must not appear in command arguments")
	}
	if fs.NArg() != 0 || *configPath == "" || (*tokenFile != "" && *tokenStdin) || (mode == "observe" && *target != "") || *interval < 5*time.Second || *interval > time.Minute {
		return errors.New("invalid collector configuration flags")
	}
	upload := *tokenFile != "" || *tokenStdin
	if !upload && (mode != "discover" || *target == "") {
		return errors.New("upload requires a scoped token; offline discovery requires --output")
	}
	config, err := devicesnmp.ReadPrivateConfig(*configPath)
	if err != nil {
		return err
	}
	if upload && config.Endpoint == "" {
		return errors.New("collector endpoint is required for upload")
	}
	if mode == "observe" && len(config.Mappings) == 0 {
		return errors.New("observation mode requires explicit reviewed port/ifIndex mappings")
	}
	token := ""
	if *tokenFile != "" {
		token, err = devicesnmp.ReadToken(*tokenFile)
	} else if *tokenStdin {
		var data []byte
		data, err = io.ReadAll(io.LimitReader(input, 257))
		if err == nil {
			token, err = devicesnmp.TokenFromBytes(data)
		}
	}
	if err != nil {
		return errors.New("collector token could not be loaded")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	client := &http.Client{Timeout: 20 * time.Second}
	for {
		pollConfig := config
		var collectErr error
		if mode == "observe" {
			pollConfig.Mappings, collectErr = fetchBindings(ctx, client, config.Endpoint, token, config.SwitchID, config.Mappings)
		}
		var snapshot devicesnmp.Snapshot
		if collectErr == nil {
			snapshot, collectErr = collectDevice(ctx, pollConfig)
		}
		if collectErr != nil {
			if mode == "observe" && ctx.Err() == nil {
				_ = uploadSnapshot(ctx, client, config.Endpoint, token, "observations", devicesnmp.Failure(config, collectErr))
			}
			if mode == "discover" || *once {
				return errors.New("SNMP collection failed; no fabricated measurements or credentials were emitted")
			}
			fmt.Fprintln(output, "SNMP collection failed; last successful data will become stale. Check local credentials, mappings and device reachability.")
		} else {
			if *target != "" {
				if err = devicesnmp.WriteSnapshot(*target, snapshot); err != nil {
					return errors.New("private snapshot could not be saved")
				}
			}
			if upload {
				operation := "observations"
				if mode == "discover" {
					operation = "discoveries"
				}
				err = uploadSnapshot(ctx, client, config.Endpoint, token, operation, snapshot)
			}
			if err != nil {
				if mode == "discover" || *once {
					return err
				}
				fmt.Fprintln(output, "Snapshot upload failed; verify token scope and reviewed mappings.")
			} else {
				fmt.Fprintf(output, "Collected %d interfaces at %s. %s\n", len(snapshot.Interfaces), snapshot.ObservedAt.Format(time.RFC3339), map[bool]string{true: "Discovery requires administrator review; inventory unchanged.", false: "Read-only observations recorded; no device configuration changed."}[mode == "discover"])
			}
		}
		if mode == "discover" || *once {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*interval):
		}
	}
}
