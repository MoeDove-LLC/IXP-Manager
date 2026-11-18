package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/lookingglass"
	"github.com/moedove/ixp-manager/internal/telemetry"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, input io.Reader, output io.Writer) error {
	fs := flag.NewFlagSet("ixp-collector", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "collector JSON configuration")
	tokenFile := fs.String("token-file", "", "private collector token file")
	tokenStdin := fs.Bool("token-stdin", false, "read collector token from stdin")
	once := fs.Bool("once", false, "collect/upload one sample or process one looking-glass lease")
	lg := fs.Bool("looking-glass", false, "explicit opt-in to bounded looking-glass lease polling")
	lgOnly := fs.Bool("looking-glass-only", false, "only process looking-glass leases; requires looking-glass:run token")
	interval := fs.Duration("interval", 30*time.Second, "collection interval (5..60 seconds)")
	if err := fs.Parse(args); err != nil {
		return errors.New("invalid collector flags (tokens are accepted only via stdin or file)")
	}
	if fs.NArg() != 0 || *path == "" || (*tokenFile == "") == (!*tokenStdin) {
		return errors.New("--config and exactly one of --token-file / --token-stdin are required")
	}
	if *interval < 5*time.Second || *interval > time.Minute {
		return errors.New("interval must be 5..60 seconds")
	}
	f, err := os.Open(*path)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil || info.Size() > 1<<20 {
		f.Close()
		return errors.New("collector config exceeds 1 MiB or cannot be read")
	}
	var config telemetry.CollectorConfig
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&config)
	if err == nil && decoder.Decode(new(any)) != io.EOF {
		err = errors.New("multiple JSON values")
	}
	f.Close()
	if err != nil {
		return errors.New("invalid collector JSON configuration")
	}
	lgConfig := lookingglass.Config{RouterID: config.RouterID, Platform: config.Platform, Peers: config.Peers, Birdc: config.Birdc, BirdSocket: config.BirdSocket, Vtysh: config.Vtysh, VtySocket: config.VtySocket, Bgpctl: config.Bgpctl, BgpSocket: config.BgpSocket, AllowTable: config.LookingGlass.AllowTable}
	if !*lgOnly {
		if err = telemetry.ValidateCollector(config); err != nil {
			return err
		}
	}
	if *lg || *lgOnly {
		if err = lookingglass.ValidateConfig(lgConfig); err != nil {
			return err
		}
	}
	var raw []byte
	if *tokenFile != "" {
		handle, e := os.OpenFile(*tokenFile, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			return errors.New("collector token file unavailable")
		}
		defer handle.Close()
		info, e := handle.Stat()
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 256 {
			return errors.New("token file must be a private regular file (0600), at most 256 bytes")
		}
		raw, err = io.ReadAll(io.LimitReader(handle, 257))
		if err != nil {
			return errors.New("could not read collector token")
		}

	} else {
		raw, err = io.ReadAll(io.LimitReader(input, 257))
		if err != nil {
			return errors.New("could not read collector token")
		}
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || len(token) > 256 || strings.ContainsAny(token, "\r\n") {
		return errors.New("invalid collector token")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	client := &http.Client{Timeout: 20 * time.Second}
	for {
		var collectionErr, errorLG error
		if !*lgOnly {
			fetchBindings := telemetry.FetchBindings
			if config.Platform == "quagga" && len(config.Peers) > 0 {
				fetchBindings = telemetry.FetchQuaggaBindings
			}
			bindings, e := fetchBindings(ctx, client, config.Endpoint, token, config.RouterID)
			var batch telemetry.Batch
			if e == nil {
				batch, e = telemetry.CollectWithBindings(ctx, config, bindings)
			}
			if e == nil {
				e = telemetry.StampBindings(&batch, bindings)
			}
			if e == nil {
				e = telemetry.Upload(ctx, client, config.Endpoint, token, batch)
			}
			collectionErr = e
			if e != nil {
				fmt.Fprintln(output, "Collection failed; no fabricated sample was uploaded. Check mappings, local status access and endpoint availability.")
			} else {
				fmt.Fprintf(output, "Uploaded %d port counters and %d session observations at %s\n", len(batch.Ports), len(batch.Sessions), batch.ObservedAt.Format(time.RFC3339))
			}
		}
		if *lg || *lgOnly {
			processed, e := lookingglass.RunOnce(ctx, client, config.Endpoint, token, lgConfig)
			errorLG = e
			if e != nil {
				fmt.Fprintln(output, "Looking-glass request failed; raw routing output was not uploaded.")
			} else if processed {
				fmt.Fprintln(output, "Completed one bounded looking-glass lease.")
			} else if *once {
				fmt.Fprintln(output, "No pending looking-glass lease.")
			}
		}
		if *once {
			return errors.Join(collectionErr, errorLG)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*interval):
		}
	}
}
