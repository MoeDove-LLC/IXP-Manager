package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/routing"
)

type fetchOptions struct {
	Server, Deployment, Router, Platform, Output string
}

var fetchIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
var pullTokenPattern = regexp.MustCompile(`^mdt\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

func runFetch(args []string, input io.Reader, output io.Writer) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	var opts fetchOptions
	fs.StringVar(&opts.Server, "server", "", "trusted HTTPS control plane origin")
	fs.StringVar(&opts.Deployment, "deployment", "", "exact approved deployment ID")
	fs.StringVar(&opts.Router, "router", "", "expected router ID")
	fs.StringVar(&opts.Platform, "platform", "", "expected target platform")
	fs.StringVar(&opts.Output, "output", "", "new private file; existing paths are never overwritten")
	tokenFile := fs.String("token-file", "", "private deployments:pull token file (0600)")
	tokenStdin := fs.Bool("token-stdin", false, "read deployments:pull token from stdin")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || (*tokenFile == "") == !*tokenStdin {
		return errors.New("fetch requires exactly one of --token-file or --token-stdin and no positional arguments")
	}
	var raw []byte
	var err error
	if *tokenStdin {
		raw, err = io.ReadAll(io.LimitReader(input, 257))
	} else {
		f, e := os.OpenFile(*tokenFile, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			return errors.New("deployment token file unavailable")
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 256 {
			return errors.New("deployment token must be a private regular file of at most 256 bytes")
		}
		raw, err = io.ReadAll(io.LimitReader(f, 257))
	}
	if err != nil || len(raw) > 256 {
		return errors.New("could not read a bounded deployment token")
	}
	token := strings.TrimSpace(string(raw))
	if !pullTokenPattern.MatchString(token) {
		return errors.New("invalid deployment token")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	checksum, err := fetchDeployment(ctx, http.DefaultTransport, opts, token)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Saved private configuration to %q\nFile SHA-256: %s\nNo router configuration was applied.\n", opts.Output, checksum)
	return err
}

func fetchDeployment(ctx context.Context, transport http.RoundTripper, opts fetchOptions, token string) (string, error) {
	u, err := url.Parse(opts.Server)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("--server must be a trusted HTTPS origin without credentials, path, query or fragment")
	}
	if !fetchIdentifier.MatchString(opts.Deployment) || !fetchIdentifier.MatchString(opts.Router) || opts.Output == "" {
		return "", errors.New("--deployment, --router and --output are required")
	}
	if opts.Platform != "quagga" && opts.Platform != "bird1" && opts.Platform != "bird2" && opts.Platform != "bird3" && opts.Platform != "frr" && opts.Platform != "junos" && opts.Platform != "openbgpd" {
		return "", errors.New("fetch requires an explicitly supported deployment platform")
	}
	if len(token) > 256 || !pullTokenPattern.MatchString(token) {
		return "", errors.New("invalid deployment token")
	}
	parent, err := os.OpenRoot(filepath.Dir(opts.Output))
	if err != nil {
		return "", errors.New("output directory unavailable")
	}
	defer parent.Close()
	name := filepath.Base(opts.Output)
	if _, err := parent.Lstat(name); !os.IsNotExist(err) {
		return "", errors.New("output path already exists or cannot be inspected")
	}
	u.Path, u.RawPath = "/api/deployments/"+opts.Deployment+"/config", ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", errors.New("could not construct deployment request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/plain")
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return "", errors.New("deployment download failed; check HTTPS connectivity and trusted server identity")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		// Error bodies may contain tool output or keys; never echo them.
		return "", fmt.Errorf("deployment download returned HTTP %d; verify the approved revision and router-bound pull token", res.StatusCode)
	}
	for key, expected := range map[string]string{"X-Config-Platform": opts.Platform, "X-Config-Router-ID": opts.Router, "X-Config-Revision": opts.Deployment} {
		if len(res.Header.Values(key)) != 1 || res.Header.Get(key) != expected {
			return "", errors.New("downloaded deployment does not match the expected platform, router or revision")
		}
	}
	checksum := res.Header.Get("X-Config-SHA256")
	decoded, err := hex.DecodeString(checksum)
	if len(res.Header.Values("X-Config-SHA256")) != 1 || err != nil || len(decoded) != 32 || checksum != strings.ToLower(checksum) {
		return "", errors.New("download lacks a valid file checksum")
	}
	config, err := io.ReadAll(io.LimitReader(res.Body, (16<<20)+1))
	if err != nil || len(config) == 0 || len(config) > 16<<20 || routing.Checksum(string(config)) != checksum {
		return "", errors.New("downloaded configuration failed its size or checksum check")
	}
	// A directory handle pins the destination even if its pathname is renamed.
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", errors.New("could not create private output file")
	}
	temp := ".moedove-download-" + hex.EncodeToString(nonce[:])
	f, err := parent.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", errors.New("could not create private output file")
	}
	defer parent.Remove(temp)
	defer f.Close()
	if _, err = f.Write(config); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Close()
	}
	if err != nil {
		return "", errors.New("could not persist downloaded configuration")
	}
	if err = parent.Link(temp, name); err != nil {
		return "", errors.New("could not publish configuration; output may already exist")
	}
	if err = parent.Remove(temp); err != nil {
		return "", errors.New("configuration was written but private temporary file cleanup failed; inspect the output before retrying")
	}
	dir, err := parent.Open(".")
	if err == nil {
		err = dir.Sync()
		dir.Close()
	}
	if err != nil {
		return "", errors.New("configuration was written but directory sync failed; inspect the output before retrying")
	}
	return checksum, nil
}
