// ixp-netconf stages reviewed Junos artifacts with a persistent rollback timer.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/netconf"
	"github.com/moedove/ixp-manager/internal/routing"
)

type receipt struct {
	Version                  int                `json:"version"`
	Target                   netconf.SSHOptions `json:"target"`
	Checksum                 string             `json:"checksum"`
	Token                    string             `json:"persistToken"`
	CreatedAt                time.Time          `json:"createdAt"`
	Timeout                  uint32             `json:"timeout"`
	State                    string             `json:"state"`
	KnownHostsSHA256         string             `json:"knownHostsSha256"`
	AllowGlobalRoutingChange bool               `json:"allowGlobalRoutingChange"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 || (args[0] != "stage" && args[0] != "confirm" && args[0] != "cancel") {
		return errors.New("usage: ixp-netconf stage --file approved.xml --checksum SHA256 --receipt receipt.json --host HOST --user USER --known-hosts /path --identity /path [--port 830] [--timeout 300]; ixp-netconf confirm|cancel --receipt receipt.json")
	}
	mode := args[0]
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	path := fs.String("receipt", "", "private receipt path (required)")
	file := fs.String("file", "", "approved Junos XML")
	sum := fs.String("checksum", "", "file SHA-256 from the authorized config download (not the approval identifier)")
	host := fs.String("host", "", "router host")
	user := fs.String("user", "", "NETCONF user")
	port := fs.Int("port", 830, "SSH NETCONF port")
	known := fs.String("known-hosts", "", "pinned known_hosts file")
	identity := fs.String("identity", "", "private key path")
	timeout := fs.Uint("timeout", 300, "confirmed commit timeout seconds (60..3600)")
	allowGlobal := fs.Bool("allow-global-routing-change", false, "explicitly permit replacing existing global router-id or ASN")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if mode != "stage" {
		var unexpected string
		fs.Visit(func(f *flag.Flag) {
			if f.Name != "receipt" {
				unexpected = f.Name
			}
		})
		if unexpected != "" {
			return fmt.Errorf("%s only accepts --receipt; unexpected --%s", mode, unexpected)
		}
	}
	if fs.NArg() != 0 || *path == "" {
		return errors.New("--receipt is required and positional arguments are not supported")
	}
	var r receipt
	var config string
	if mode == "stage" {
		if *file == "" || *sum == "" || *timeout < 60 || *timeout > 3600 {
			return errors.New("stage requires --file, --checksum and timeout 60..3600")
		}
		f, err := os.Open(*file)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
		f.Close()
		if err != nil {
			return err
		}
		config = string(b)
		digest := sha256.Sum256(b)
		if hex.EncodeToString(digest[:]) != *sum {
			return errors.New("configuration checksum mismatch")
		}
		if err = routing.ValidateJunosXML(config); err != nil {
			return err
		}
		token, err := netconf.Token()
		if err != nil {
			return err
		}
		r = receipt{Version: 1, Target: netconf.SSHOptions{Host: *host, User: *user, Port: *port, KnownHosts: *known, Identity: *identity}, Checksum: *sum, Token: token, CreatedAt: time.Now().UTC(), Timeout: uint32(*timeout), State: "prepared", AllowGlobalRoutingChange: *allowGlobal}
		if _, err = netconf.SSHArgs(r.Target); err != nil {
			return err
		}
		knownData, err := os.ReadFile(r.Target.KnownHosts)
		if err != nil {
			return err
		}
		knownHash := sha256.Sum256(knownData)
		r.KnownHostsSHA256 = hex.EncodeToString(knownHash[:])
		if err = saveReceipt(*path, r, true); err != nil {
			return err
		}

	} else {
		if *host != "" || *user != "" || *known != "" || *identity != "" || *file != "" || *sum != "" {
			return errors.New("confirm/cancel uses only the target bound in --receipt")
		}
		st, err := os.Lstat(*path)
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 16384 {
			return errors.New("receipt must be a private regular file (0600) under 16 KiB")
		}
		b, err := os.ReadFile(*path)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(b, &r); err != nil {
			return err
		}
		decodedToken, tokenErr := hex.DecodeString(r.Token)
		if tokenErr != nil || len(decodedToken) != 32 || len(r.Checksum) != 64 {
			return errors.New("invalid receipt token or configuration binding")
		}
		if r.Version != 1 {
			return errors.New("unsupported receipt version")
		}
		if r.State == "confirmed" || r.State == "cancelled" {
			return errors.New("receipt has already been finalized")
		}
	}
	knownData, err := os.ReadFile(r.Target.KnownHosts)
	if err != nil {
		return err
	}
	knownHash := sha256.Sum256(knownData)
	if hex.EncodeToString(knownHash[:]) != r.KnownHostsSHA256 {
		return errors.New("pinned known_hosts changed since receipt creation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c, err := netconf.DialSSH(ctx, r.Target)
	if err != nil {
		return fmt.Errorf("connect failed; keep private receipt for explicit recovery: %w", err)
	}
	defer c.Close()
	switch mode {
	case "stage":
		err = c.StageWithOptions(config, r.Checksum, r.Token, r.Timeout, netconf.StageOptions{AllowGlobalRoutingChange: r.AllowGlobalRoutingChange})
	case "confirm":
		err = c.Confirm(r.Token)
	case "cancel":
		err = c.Cancel(r.Token)
	}
	if err != nil {
		return fmt.Errorf("%s failed; commit outcome may be uncertain, preserve receipt and inspect/cancel the pending commit: %w", mode, errors.New(strings.ReplaceAll(err.Error(), r.Token, "[redacted]")))
	}
	switch mode {
	case "stage":
		r.State = "staged"
	case "confirm":
		r.State = "confirmed"
	case "cancel":
		r.State = "cancelled"
	}
	if err = saveReceipt(*path, r, false); err != nil {
		return err
	}
	if mode == "stage" {
		fmt.Printf("Junos candidate validated and staged; automatic rollback in %d seconds unless explicitly confirmed. Receipt: %s\n", r.Timeout, *path)
	} else {
		fmt.Printf("NETCONF %s completed. Receipt: %s\n", mode, *path)
	}
	return nil
}

func saveReceipt(path string, r receipt, exclusive bool) error {
	var f *os.File
	var err error
	if exclusive {
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	} else {
		f, err = os.CreateTemp(filepath.Dir(path), ".netconf-receipt-*")
	}
	if err != nil {
		return err
	}
	if !exclusive {
		defer os.Remove(f.Name())
	}
	defer f.Close()
	if err = json.NewEncoder(f).Encode(r); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if !exclusive {
		if err = os.Rename(f.Name(), path); err != nil {
			return err
		}
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
