// ixp-provision renders/checks/downloads provisioning artifacts and reads
// observed health for Nagios. It never installs or reloads a service.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/moedove/ixp-manager/internal/provision"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
var bearerToken = regexp.MustCompile(`^mdt\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

func main() {
	code, err := run(os.Args[1:], os.Stdin, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func run(args []string, input io.Reader, output io.Writer) (int, error) {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Fprintln(output, `Usage: ixp-provision <command> [flags]
render: typed input JSON to a new private configuration file
check: verify checksum and constrained artifact; DNS also requires native offline checkers
fetch: download an exact approved revision over HTTPS, without activation
probe: read fresh observed BGP/switch/router status for Nagios (0/2/3); no active network probe
as112-extract: extract an exact AS112 bundle into a new private directory
as112-probe: explicitly query only the bundle's locally configured fixed AS112 DNS addresses over UDP/TCP
rfc2317-render: create a review bundle with complete child and incomplete parent fragment
rfc2317-check: check child and synthetic parent wrapper; not actual parent integration or approval
rfc2317-extract: save exact reviewed files into a new private directory
rfc2317-merge: verify a complete local parent snapshot and create an offline candidate bundle
rfc2317-merge-check: verify an exact merged bundle with native offline BIND checks
rfc2317-merge-extract: verify and save both complete reviewed zone files into a new private directory
rfc2317-preflight: verify exact approved paired bytes against explicit local target zone file baselines
rfc2317-install: explicitly publish both static zones to a bound local BIND instance, child first
Use <command> --help for flags.`)
		return 0, nil
	}
	switch args[0] {
	case "rfc2317-render":
		return 0, runRFC2317Render(args[1:], input, output)
	case "rfc2317-check":
		return 0, runRFC2317Artifact(args[1:], output, false)
	case "rfc2317-extract":
		return 0, runRFC2317Artifact(args[1:], output, true)
	case "rfc2317-merge":
		return 0, runRFC2317Merge(args[1:], output)
	case "rfc2317-merge-check":
		return 0, runRFC2317MergedArtifact(args[1:], output, false)
	case "rfc2317-merge-extract":
		return 0, runRFC2317MergedArtifact(args[1:], output, true)
	case "rfc2317-preflight":
		return 0, runRFC2317Preflight(args[1:], output)
	case "rfc2317-install":
		return 0, runRFC2317Install(args[1:], output)
	case "render":
		return 0, runRender(args[1:], input, output)
	case "check":
		return 0, runCheck(args[1:], output)
	case "fetch":
		return 0, runFetch(args[1:], input, output)
	case "probe":
		return runProbe(args[1:], input, output)
	case "as112-extract":
		return runAS112(args[1:], output, false)
	case "as112-probe":
		return runAS112(args[1:], output, true)
	default:
		return 1, errors.New("unknown command; use ixp-provision --help")
	}
}

func parse(fs *flag.FlagSet, args []string) (bool, error) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, nil
		}
		return false, errors.New("invalid flags; use --help (secrets are accepted only from stdin or private files)")
	}
	if fs.NArg() != 0 {
		return false, errors.New("unexpected positional arguments")
	}
	return false, nil
}

func runRender(args []string, input io.Reader, output io.Writer) error {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	fs.SetOutput(output)
	path := fs.String("input", "", "typed Input JSON file, or - for stdin (8 MiB maximum)")
	destination := fs.String("output", "", "new private output file; never overwrites an existing path")
	done, err := parse(fs, args)
	if err != nil || done {
		return err
	}
	if *path == "" || *destination == "" {
		return errors.New("--input and --output are required")
	}
	var raw []byte
	if *path == "-" {
		raw, err = io.ReadAll(io.LimitReader(input, (8<<20)+1))
	} else {
		raw, err = readRegular(*path, 8<<20, false)
	}
	if err != nil || len(raw) > 8<<20 {
		return errors.New("input must be a readable regular JSON file of at most 8 MiB")
	}
	var in provision.Input
	if err = decodeStrict(raw, &in); err != nil {
		return errors.New("invalid typed provisioning input JSON")
	}
	a, err := provision.Render(in)
	if err != nil {
		return err
	}
	if err = provision.ValidateArtifact(a); err != nil {
		return err
	}
	if err = writeNew(*destination, []byte(a.Content)); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Kind            string             `json:"kind"`
		File            string             `json:"file"`
		SHA256          string             `json:"sha256"`
		Zone            string             `json:"zone,omitempty"`
		ValidationLevel string             `json:"validationLevel"`
		Validated       bool               `json:"validated"`
		Activated       bool               `json:"activated"`
		Notices         []provision.Notice `json:"notices"`
	}{a.Kind, *destination, checksum(a.Content), a.Zone, a.ValidationLevel, false, false, a.Notices})
}

func runCheck(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(output)
	path := fs.String("file", "", "reviewed configuration file")
	kind := fs.String("kind", "", "dns-arpa, as112-dns, nagios, smokeping or switch-core")
	zone := fs.String("zone", "", "expected reverse zone for DNS, including final dot")
	want := fs.String("checksum", "", "actual file SHA-256 from download header or render output, not a review token")
	binary := fs.String("named-checkzone", "named-checkzone", "local BIND offline checker executable")
	pdns := fs.String("pdns-server", "pdns_server", "PowerDNS Authoritative offline settings checker for AS112")
	done, err := parse(fs, args)
	if err != nil || done {
		return err
	}
	if *path == "" || !validChecksum(*want) {
		return errors.New("--file, --kind and actual --checksum are required")
	}
	raw, err := readRegular(*path, provision.MaxArtifactBytes, false)
	if err != nil {
		return err
	}
	if checksum(string(raw)) != *want {
		return errors.New("file checksum does not match the reviewed bytes")
	}
	a := artifact(*kind, *zone, filepath.Base(*path), raw)
	v, err := provision.Validate(context.Background(), a, provision.ValidatorOptions{NamedCheckzone: *binary, PDNSServer: *pdns})
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Validation provision.Validation `json:"validation"`
		SHA256     string               `json:"sha256"`
		Activated  bool                 `json:"activated"`
	}{v, *want, false})
}

func artifact(kind, zone, name string, raw []byte) provision.Artifact {
	level := "structural"
	if kind == "dns-arpa" || kind == "as112-dns" {
		level = "native"
	}
	return provision.Artifact{Kind: kind, Content: string(raw), Filename: name, Zone: zone, Complete: kind == "dns-arpa" || kind == "as112-dns", ValidationLevel: level}
}
func checksum(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
func validChecksum(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && strings.ToLower(value) == value
}
func decodeStrict(raw []byte, value any) error {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}
func readRegular(path string, limit int64, private bool) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("input file unavailable")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > limit || (private && st.Mode().Perm()&0077 != 0) {
		return nil, errors.New("input must be a bounded regular file; token files must be private (0600)")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("input file read failed or exceeded size limit")
	}
	return raw, nil
}
func readToken(path string, stdin bool, input io.Reader) (string, error) {
	if (path == "") == !stdin {
		return "", errors.New("exactly one of --token-file or --token-stdin is required")
	}
	var raw []byte
	var err error
	if stdin {
		raw, err = io.ReadAll(io.LimitReader(input, 257))
	} else {
		raw, err = readRegular(path, 256, true)
	}
	if err != nil || len(raw) > 256 {
		return "", errors.New("could not read bounded private token")
	}
	token := strings.TrimSpace(string(raw))
	if !bearerToken.MatchString(token) {
		return "", errors.New("invalid API token")
	}
	return token, nil
}

// Publish a fully fsynced file without overwriting any existing destination.
// This saves an artifact; it is intentionally not a service install operation.
func writeNew(path string, raw []byte) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return errors.New("output directory unavailable")
	}
	defer root.Close()
	name := filepath.Base(path)
	if name == "." || name == ".." {
		return errors.New("invalid output filename")
	}
	if _, err = root.Lstat(name); !os.IsNotExist(err) {
		return errors.New("output already exists or cannot be inspected")
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return errors.New("could not create private output")
	}
	temp := ".moedove-provision-" + hex.EncodeToString(nonce[:])
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("could not create private output")
	}
	defer f.Close()
	defer root.Remove(temp)
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Close()
	}
	if err != nil {
		return errors.New("could not persist output")
	}
	if err = root.Link(temp, name); err != nil {
		return errors.New("could not publish output; destination may already exist")
	}
	if err = root.Remove(temp); err != nil {
		return errors.New("output saved but temporary cleanup failed; inspect before retrying")
	}
	dir, err := root.Open(".")
	if err == nil {
		err = dir.Sync()
		dir.Close()
	}
	if err != nil {
		return errors.New("output saved but directory sync failed; inspect before retrying")
	}
	return nil
}
