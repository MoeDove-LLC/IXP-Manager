package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/provision"
	"golang.org/x/net/dns/dnsmessage"
)

type rfc2317BINDOptions struct {
	RNDCBinary      string
	ControlKey      string
	ControlEndpoint string
	DNSEndpoint     string
}

type rfc2317ZoneStatus struct {
	Zone   string
	File   string
	Serial *uint32 // nil only for a proven configured, unloaded static primary
}

type rfc2317BIND struct {
	opts           rfc2317BINDOptions
	control, dns   netip.AddrPort
	program, key   rfc2317LocalFile
	binary, config *os.File
}

var rfc2317RNDCKey = regexp.MustCompile(`^\s*key\s+"([A-Za-z0-9_.-]{1,128})"\s*\{\s*algorithm\s+([A-Za-z0-9_-]{1,63})\s*;\s*secret\s+"([A-Za-z0-9+/]+={0,2})"\s*;\s*\}\s*;\s*$`)

func rfc2317Loopback(raw string) (netip.AddrPort, error) {
	endpoint, err := netip.ParseAddrPort(raw)
	if err != nil || !endpoint.Addr().IsLoopback() || endpoint.Addr().Is4In6() || endpoint.Addr().Zone() != "" || endpoint.Port() == 0 {
		return netip.AddrPort{}, errors.New("BIND endpoint must be an explicit numeric loopback IP and port")
	}
	return endpoint, nil
}

func openRFC2317BIND(opts rfc2317BINDOptions) (*rfc2317BIND, error) {
	control, err := rfc2317Loopback(opts.ControlEndpoint)
	if err != nil {
		return nil, err
	}
	dns, err := rfc2317Loopback(opts.DNSEndpoint)
	if err != nil || dns == control {
		return nil, errors.New("distinct numeric loopback control and authoritative DNS endpoints required")
	}
	b := &rfc2317BIND{opts: opts, control: control, dns: dns}
	// The operator pins an explicit local executable. Check its file identity
	// before each command and execute the opened descriptor, not mutable PATH.
	b.program, err = readRFC2317LocalFile(opts.RNDCBinary, 16<<20, false)
	if err != nil || !b.program.present || !bytes.HasPrefix(b.program.bytes, []byte{0x7f, 'E', 'L', 'F'}) || b.program.file.Mode&0022 != 0 ||
		b.program.file.Mode&0111 == 0 || b.program.file.Mode&(syscall.S_ISUID|syscall.S_ISGID) != 0 || !rfc2317TrustedOwner(b.program.file) {
		return nil, errors.New("RNDC requires an explicit trusted Linux executable without symlinks")
	}
	b.key, err = readRFC2317LocalFile(opts.ControlKey, 4096, false)
	if err != nil || !b.key.present || b.key.file.Mode&0027 != 0 || b.key.file.Nlink != 1 || !rfc2317TrustedOwner(b.key.file) {
		return nil, errors.New("BIND control key must be an owned private regular key file without symlinks")
	}
	clauses := rfc2317RNDCKey.FindSubmatch(b.key.bytes)
	if len(clauses) != 4 {
		return nil, errors.New("BIND control key must contain exactly one direct quoted key without includes")
	}
	secret, err := base64.StdEncoding.Strict().DecodeString(string(clauses[3]))
	clear(secret)
	if err != nil || len(secret) == 0 {
		return nil, errors.New("BIND control key secret is invalid")
	}
	b.binary, err = os.OpenFile(opts.RNDCBinary, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("cannot pin the RNDC binary descriptor")
	}
	ready := false
	defer func() {
		if !ready {
			b.Close()
		}
	}()
	var inspected syscall.Stat_t
	if syscall.Fstat(int(b.binary.Fd()), &inspected) != nil || !sameRFC2317File(b.program.file, inspected) {
		return nil, errors.New("RNDC binary changed before pinning")
	}
	b.config, err = os.CreateTemp("", "moedove-rfc2317-rndc-")
	if err != nil {
		return nil, errors.New("cannot prepare private RNDC configuration")
	}
	if err = os.Remove(b.config.Name()); err != nil {
		return nil, errors.New("cannot privatize the RNDC configuration")
	}
	// RNDC's -k is an ambient fallback if no config is selected. Generate a
	// single scoped config and always pass -c using the private descriptor.
	if _, err = b.config.Write(b.key.bytes); err != nil {
		return nil, errors.New("cannot populate the private RNDC configuration")
	}
	if _, err = io.WriteString(b.config, "\noptions { default-key \""+string(clauses[1])+"\"; };\n"); err != nil {
		return nil, errors.New("cannot populate the private RNDC configuration")
	}
	ready = true
	return b, nil
}

func rfc2317TrustedOwner(st syscall.Stat_t) bool {
	return st.Uid == 0 || st.Uid == uint32(os.Geteuid())
}

func (b *rfc2317BIND) Close() error {
	clear(b.key.bytes)
	var err error
	if b.binary != nil {
		err = b.binary.Close()
		b.binary = nil
	}
	if b.config != nil {
		err = errors.Join(err, b.config.Close())
		b.config = nil
	}
	return err
}

type rfc2317RNDCOutput struct {
	bytes.Buffer
	exceeded bool
}

func (b *rfc2317RNDCOutput) Write(data []byte) (int, error) {
	n := len(data)
	if n > 16<<10-b.Len() {
		b.exceeded = true
		return n, nil
	}
	return b.Buffer.Write(data)
}

func (b *rfc2317BIND) checkBindings() error {
	if b.binary == nil || b.config == nil {
		return errors.New("local BIND control adapter is closed")
	}
	program, err := readRFC2317LocalFile(b.opts.RNDCBinary, 16<<20, false)
	if err != nil || !b.program.matches(program) {
		return errors.New("pinned local RNDC executable changed")
	}
	current, err := readRFC2317LocalFile(b.opts.ControlKey, 4096, false)
	defer clear(current.bytes)
	if err != nil || !b.key.matches(current) {
		return errors.New("pinned local BIND control key changed")
	}
	return nil
}

func (b *rfc2317BIND) command(ctx context.Context, verb, zone string) (string, string, error) {
	if verb != "zonestatus" && verb != "showzone" && verb != "reload" {
		return "", "", errors.New("unsupported BIND operation")
	}
	if err := b.checkBindings(); err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/proc/self/fd/3", "-c", "/proc/self/fd/4", "-s", b.control.Addr().String(), "-p", strconv.Itoa(int(b.control.Port())), verb, zone, "IN")
	cmd.Args[0] = b.opts.RNDCBinary
	cmd.ExtraFiles = []*os.File{b.binary, b.config}
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LC_ALL=C", "LANG=C"}
	if libraries := os.Getenv("LD_LIBRARY_PATH"); libraries != "" {
		// The operator's library installation can be separate from the
		// executable. No ambient RNDC configuration or preload is inherited.
		cmd.Env = append(cmd.Env, "LD_LIBRARY_PATH="+libraries)
	}
	cmd.WaitDelay = 500 * time.Millisecond
	var stdout, stderr rfc2317RNDCOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil || stdout.exceeded || stderr.exceeded {
		return "", "", errors.New("RNDC response exceeded its time or size bound")
	}
	return stdout.String(), stderr.String(), err
}

func rfc2317BINDZone(raw string) (string, error) {
	zone, err := provision.SafeFQDN(raw)
	if err != nil || !strings.HasSuffix(zone, ".in-addr.arpa.") {
		return "", errors.New("explicit IPv4 reverse zone required for BIND control")
	}
	return zone, nil
}

func rfc2317BINDPath(path string) bool {
	// RNDC's `files:` report can concatenate includes without quoting. Match
	// one unambiguous absolute path, rather than a potential list of names.
	return filepath.IsAbs(path) && path != "/" && filepath.Clean(path) == path && !strings.ContainsAny(path, " \t\r\n\x00,'\"\\;")
}

func parseRFC2317LoadedZone(raw, zone, path string) (rfc2317ZoneStatus, error) {
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		name, value, ok := strings.Cut(line, ": ")
		if !ok || fields[name] != "" || value == "" {
			return rfc2317ZoneStatus{}, errors.New("RNDC returned an ambiguous zone status")
		}
		switch name {
		case "name", "type", "files", "serial", "nodes", "last loaded", "secure", "dynamic", "reconfigurable via modzone", "inline signing", "key maintenance":
			fields[name] = value
		default:
			return rfc2317ZoneStatus{}, errors.New("RNDC reported an unsupported zone status field")
		}
	}
	serial, err := strconv.ParseUint(fields["serial"], 10, 32)
	if err != nil || fields["serial"] != strconv.FormatUint(serial, 10) || fields["name"] != zone || fields["files"] != path || fields["type"] != "primary" ||
		fields["last loaded"] == "" || fields["dynamic"] != "no" || fields["secure"] != "no" || fields["reconfigurable via modzone"] != "no" ||
		(fields["inline signing"] != "" && fields["inline signing"] != "no") || (fields["key maintenance"] != "" && fields["key maintenance"] != "none") {
		return rfc2317ZoneStatus{}, errors.New("RNDC did not prove the expected loaded unsigned static primary and master file")
	}
	value := uint32(serial)
	return rfc2317ZoneStatus{Zone: zone, File: path, Serial: &value}, nil
}

func matchRFC2317UnloadedZone(raw, zone, path string) bool {
	// ISC's showzone renders one normalized stanza. Only a primary with an
	// explicit static update ACL and both signing switches off proves the
	// unloaded state. Global signing defaults cannot fill a missing field.
	text := strings.Join(strings.Fields(raw), " ")
	body, ok := strings.CutPrefix(text, "zone \""+zone+"\" IN { ")
	if !ok {
		return false
	}
	body, ok = strings.CutSuffix(body, " };")
	if !ok {
		return false
	}
	settings := []string{"type primary;", "file \"" + path + "\";", "inline-signing no;", `allow-update { "none"; };`, `dnssec-policy "none";`}
	seen := [5]bool{}
	for body != "" {
		found := false
		for i, setting := range settings {
			if remaining, ok := strings.CutPrefix(body, setting); ok && !seen[i] {
				body = strings.TrimPrefix(remaining, " ")
				seen[i], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return seen[0] && seen[1] && seen[2] && seen[3] && seen[4]
}

func (b *rfc2317BIND) Status(ctx context.Context, zone, path string) (rfc2317ZoneStatus, error) {
	zone, err := rfc2317BINDZone(zone)
	if err != nil || !rfc2317BINDPath(path) {
		return rfc2317ZoneStatus{}, errors.New("expected exact reverse zone and an unambiguous absolute master path")
	}
	stdout, stderr, err := b.command(ctx, "zonestatus", zone)
	if err == nil && stderr == "" {
		return parseRFC2317LoadedZone(stdout, zone, path)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || stdout != "" || strings.TrimSuffix(stderr, "\n") != "rndc: 'zonestatus' failed: zone not loaded" {
		return rfc2317ZoneStatus{}, errors.New("cannot authenticate the expected local BIND zone or read its status")
	}
	// A configured but unloaded primary has no path in zonestatus. Query
	// only its exact authenticated configuration, never infer it from ENOENT.
	stdout, stderr, err = b.command(ctx, "showzone", zone)
	if err != nil || stderr != "" || !matchRFC2317UnloadedZone(stdout, zone, path) {
		return rfc2317ZoneStatus{}, errors.New("unloaded child requires showzone evidence for its exact unsigned static primary and master file")
	}
	return rfc2317ZoneStatus{Zone: zone, File: path}, nil
}

func (b *rfc2317BIND) Reload(ctx context.Context, zone, path string) error {
	status, err := b.Status(ctx, zone, path)
	if err != nil {
		return err
	}
	_, stderr, err := b.command(ctx, "reload", status.Zone)
	if err != nil || stderr != "" {
		return errors.New("BIND did not acknowledge the exact zone reload")
	}
	return nil
}

func (b *rfc2317BIND) SOA(ctx context.Context, zone string) (uint32, error) {
	if b.binary == nil || b.config == nil {
		return 0, errors.New("local BIND control adapter is closed")
	}
	zone, err := rfc2317BINDZone(zone)
	if err != nil {
		return 0, err
	}
	name, err := dnsmessage.NewName(zone)
	if err != nil {
		return 0, errors.New("invalid local authoritative SOA name")
	}
	var id [2]byte
	if _, err = rand.Read(id[:]); err != nil {
		return 0, errors.New("cannot create local DNS query ID")
	}
	question := dnsmessage.Question{Name: name, Type: dnsmessage.TypeSOA, Class: dnsmessage.ClassINET}
	query := dnsmessage.Message{Header: dnsmessage.Header{ID: binary.BigEndian.Uint16(id[:])}, Questions: []dnsmessage.Question{question}}
	raw, err := query.AppendPack([]byte{0, 0})
	if err != nil {
		return 0, errors.New("cannot pack local SOA query")
	}
	binary.BigEndian.PutUint16(raw[:2], uint16(len(raw)-2))
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", b.dns.String())
	if err != nil {
		return 0, errors.New("local authoritative DNS TCP endpoint unavailable")
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return 0, errors.New("cannot bound local DNS TCP request")
	}
	if n, err := conn.Write(raw); err != nil || n != len(raw) {
		return 0, errors.New("cannot send exact local SOA query")
	}
	var length [2]byte
	if _, err := io.ReadFull(conn, length[:]); err != nil {
		return 0, errors.New("cannot read local DNS TCP response length")
	}
	size := int(binary.BigEndian.Uint16(length[:]))
	if size < 12 || size > 4096 {
		return 0, errors.New("local DNS TCP response outside length bound")
	}
	response := make([]byte, size)
	if _, err := io.ReadFull(conn, response); err != nil {
		return 0, errors.New("cannot read complete local DNS TCP response")
	}
	reply := dnsmessage.Message{}
	if reply.Unpack(response) != nil || reply.ID != query.ID || !reply.Response || reply.OpCode != 0 || reply.Truncated || !reply.Authoritative ||
		reply.RCode != dnsmessage.RCodeSuccess || len(reply.Questions) != 1 || reply.Questions[0] != question || len(reply.Answers) != 1 {
		return 0, errors.New("local DNS reply is not the exact authoritative SOA answer")
	}
	answer := reply.Answers[0]
	soa, ok := answer.Body.(*dnsmessage.SOAResource)
	if !ok || answer.Header.Class != dnsmessage.ClassINET || answer.Header.Type != dnsmessage.TypeSOA || !strings.EqualFold(answer.Header.Name.String(), zone) {
		return 0, errors.New("local authoritative SOA has a different owner or class")
	}
	return soa.Serial, nil
}
