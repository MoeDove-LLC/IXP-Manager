package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/provision"
	"golang.org/x/net/dns/dnsmessage"
)

const (
	rfc2317NativeCLI     = "/scratch/bin/ixp-provision"
	rfc2317NativeNamed   = "/native-named/named"
	rfc2317NativeRNDC    = "/native/usr/sbin/rndc"
	rfc2317NativeChecker = "/native/usr/bin/named-checkzone"
	rfc2317NativeControl = "127.0.0.1:19530"
	rfc2317NativeDNS     = "127.0.0.1:15353"
)

func rfc2317NativeFixtureGuard(t *testing.T) {
	t.Helper()
	if os.Getenv("RFC2317_BIND_NATIVE") != "1" {
		t.Skip("requires the isolated stock BIND install fixture")
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) != 1 || interfaces[0].Name != "lo" {
		t.Fatal("native BIND fixture requires a loopback-only network namespace", err)
	}
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil || !strings.Contains(string(raw), "NoNewPrivs:\t1") {
		t.Fatal("native BIND fixture requires NoNewPrivs", err)
	}
	for _, k := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		if !strings.Contains(string(raw), k+":\t0000000000000000\n") {
			t.Fatal("native BIND fixture requires all-zero capabilities")
		}
	}
}

func rfc2317NativeDNSQuery(ctx context.Context, fqdn string, qtype dnsmessage.Type) (dnsmessage.Message, error) {
	name, err := dnsmessage.NewName(fqdn)
	if err != nil {
		return dnsmessage.Message{}, err
	}
	const id = 0x2317
	question := dnsmessage.Question{Name: name, Type: qtype, Class: dnsmessage.ClassINET}
	query := dnsmessage.Message{Header: dnsmessage.Header{ID: id}, Questions: []dnsmessage.Question{question}}
	wire, err := query.Pack()
	if err != nil {
		return dnsmessage.Message{}, err
	}
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", rfc2317NativeDNS)
	if err != nil {
		return dnsmessage.Message{}, err
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		return dnsmessage.Message{}, err
	}
	frame := binary.BigEndian.AppendUint16(nil, uint16(len(wire)))
	if _, err = io.Copy(conn, bytes.NewReader(append(frame, wire...))); err != nil {
		return dnsmessage.Message{}, err
	}
	var prefix [2]byte
	if _, err = io.ReadFull(conn, prefix[:]); err != nil {
		return dnsmessage.Message{}, err
	}
	size := int(binary.BigEndian.Uint16(prefix[:]))
	if size < 12 || size > 8192 {
		return dnsmessage.Message{}, fmt.Errorf("unexpected bounded DNS reply size %d", size)
	}
	reply := make([]byte, size)
	if _, err = io.ReadFull(conn, reply); err != nil {
		return dnsmessage.Message{}, err
	}
	var msg dnsmessage.Message
	if err = msg.Unpack(reply); err != nil {
		return msg, err
	}
	if !msg.Response || msg.ID != id || msg.Truncated || len(msg.Questions) != 1 || msg.Questions[0] != question {
		return msg, fmt.Errorf("DNS response did not match the exact TCP question")
	}
	return msg, nil
}

func rfc2317NativeSOAMatches(reply dnsmessage.Message, zone string, serial uint32) bool {
	if !reply.Authoritative || reply.RCode != dnsmessage.RCodeSuccess || len(reply.Answers) != 1 || reply.Answers[0].Header.Name.String() != zone {
		return false
	}
	answer, ok := reply.Answers[0].Body.(*dnsmessage.SOAResource)
	return ok && answer.Serial == serial
}

func rfc2317NativeStartBIND(t *testing.T, ctx context.Context, f targetFixture) (string, string) {
	t.Helper()
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(f.dir, "rndc.key")
	writeRFC2317TestFile(t, key, fmt.Appendf(nil, "key \"rndc-key\" { algorithm hmac-sha256; secret \"%s\"; };\n", base64.StdEncoding.EncodeToString(secret[:])))
	parent, child := f.bundle.Files[1].Zone, f.bundle.Files[0].Zone
	config := fmt.Sprintf(`options {
 directory "%s";
 pid-file "%s";
 session-keyfile none;
 listen-on port 15353 { 127.0.0.1; };
 listen-on-v6 { none; };
 recursion no;
 dnssec-validation no;
 empty-zones-enable no;
 automatic-interface-scan no;
 notify no;
 allow-new-zones yes;
};
include "%s";
controls { inet 127.0.0.1 port 19530 allow {127.0.0.1;} keys {"rndc-key";}; };
zone "%s" IN { type primary; file "%s"; allow-update { none; }; dnssec-policy none; inline-signing no; };
zone "%s" IN { type primary; file "%s"; allow-update { none; }; dnssec-policy none; inline-signing no; };
`, f.dir, filepath.Join(f.dir, "named.pid"), key, parent, f.parent, child, f.child)
	conf := filepath.Join(f.dir, "named.conf")
	writeRFC2317TestFile(t, conf, []byte(config))
	log := filepath.Join(f.dir, "named.log")
	output, err := os.OpenFile(log, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	named := exec.Command(rfc2317NativeNamed, "-g", "-n", "1", "-c", conf)
	named.Stdout, named.Stderr = output, output
	err = named.Start()
	output.Close()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- named.Wait() }()
	joined := false
	t.Cleanup(func() {
		if joined {
			return
		}
		_ = named.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			if err != nil && !t.Failed() {
				t.Error("native named did not stop cleanly", err)
			}
		case <-time.After(5 * time.Second):
			_ = named.Process.Kill()
			<-done
			t.Error("native named did not acknowledge shutdown")
		}
	})
	for until := time.Now().Add(10 * time.Second); time.Now().Before(until); {
		if msg, err := rfc2317NativeDNSQuery(ctx, parent, dnsmessage.TypeSOA); err == nil && rfc2317NativeSOAMatches(msg, parent, f.bundle.PreviousParentSerial) {
			return key, log
		}
		select {
		case err := <-done:
			joined = true
			t.Fatal("native named exited during startup", err)
		case <-ctx.Done():
			t.Fatal("native named startup context ended", ctx.Err())
		case <-time.After(40 * time.Millisecond):
		}
	}
	t.Fatal("native named never served the expected parent baseline")
	return "", ""
}

func rfc2317NativeReloads(t *testing.T, log string) []string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if err != nil || len(raw) > 1<<20 {
		t.Fatal("could not inspect the bounded native BIND control log", err)
	}
	var reloads []string
	for _, line := range strings.Split(string(raw), "\n") {
		_, rest, ok := strings.Cut(line, "received control channel command 'reload ")
		if !ok {
			continue
		}
		cmd, _, ok := strings.Cut(rest, "'")
		fields := strings.Fields(cmd)
		if !ok || len(fields) == 0 {
			t.Fatal("native BIND emitted an incomplete reload record")
		}
		reloads = append(reloads, strings.Trim(fields[0], `"`))
	}
	return reloads
}

type rfc2317LostNativeChildReply struct {
	*rfc2317BIND
	zone string
	lost bool
}

func (a *rfc2317LostNativeChildReply) Reload(ctx context.Context, zone, path string) error {
	if err := a.rfc2317BIND.Reload(ctx, zone, path); err != nil {
		return err
	}
	if zone == a.zone && !a.lost {
		a.lost = true
		return errors.New("fixture lost the response after actual BIND child reload")
	}
	return nil
}

func rfc2317NativeInterruptChildLoad(t *testing.T, ctx context.Context, f targetFixture, key, log string) {
	t.Helper()
	parent, child := f.bundle.Files[1].Zone, f.bundle.Files[0].Zone
	opts := rfc2317BINDOptions{RNDCBinary: rfc2317NativeRNDC, ControlKey: key, ControlEndpoint: rfc2317NativeControl, DNSEndpoint: rfc2317NativeDNS}
	authority, err := openRFC2317BIND(opts)
	if err != nil {
		t.Fatal(err)
	}
	files, err := lockRFC2317Files(f.parent, f.child)
	if err != nil {
		_ = authority.Close()
		t.Fatal(err)
	}
	binding := rfc2317InstallBinding{ParentFile: f.parent, ChildFile: f.child, RNDCBinary: opts.RNDCBinary, ControlKey: opts.ControlKey,
		ControlEndpoint: opts.ControlEndpoint, DNSEndpoint: opts.DNSEndpoint, NamedCheckzone: rfc2317NativeChecker}
	interrupted := &rfc2317LostNativeChildReply{rfc2317BIND: authority, zone: child}
	state, err := installRFC2317(ctx, f.bundle, checksum(string(f.data)), binding, files, interrupted, false)
	files.Close()
	if closeErr := authority.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "child phase incomplete") || !interrupted.lost || state.Phase != rfc2317ChildPending {
		t.Fatal("the native lost child response was not preserved as pending", state.Phase, err)
	}
	old, err := os.ReadFile(f.parent)
	if err != nil || !bytes.Equal(old, f.parentBytes) {
		t.Fatal("interrupted native child installation rewrote the parent file", err)
	}
	if reply, err := rfc2317NativeDNSQuery(ctx, parent, dnsmessage.TypeSOA); err != nil || !rfc2317NativeSOAMatches(reply, parent, f.bundle.PreviousParentSerial) {
		t.Fatal("interrupted child installation advanced authoritative parent SOA", err)
	}
	if got := rfc2317NativeReloads(t, log); !reflect.DeepEqual(got, []string{child}) {
		t.Fatal("native interruption occurred without exactly one child reload", got)
	}
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if reply, err := rfc2317NativeDNSQuery(ctx, child, dnsmessage.TypeSOA); err == nil && rfc2317NativeSOAMatches(reply, child, f.bundle.ChildSerial) {
			t.Log("rfc2317_native_child_reply_lost=true child_already_served_tcp=true parent_baseline_still_served=true")
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("BIND did not load the child candidate before external CLI resume")
}

func TestRFC2317InstallStockBINDCLI(t *testing.T) {
	rfc2317NativeFixtureGuard(t)
	for _, tc := range []struct {
		name             string
		existing         bool
		loseChildReceipt bool
	}{{"unloaded-new-child", false, false}, {"existing-child-serial-zero", true, false}, {"resume-after-native-child-reply-loss", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRFC2317TargetFixture(t, tc.existing)
			ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
			defer cancel()
			key, log := rfc2317NativeStartBIND(t, ctx, f)
			parent, child := f.bundle.Files[1].Zone, f.bundle.Files[0].Zone
			if tc.existing {
				msg, err := rfc2317NativeDNSQuery(ctx, child, dnsmessage.TypeSOA)
				if err != nil || !rfc2317NativeSOAMatches(msg, child, 0) {
					t.Fatal("native child did not begin at an actual loaded serial zero", err)
				}
			} else if _, err := os.Lstat(f.child); !os.IsNotExist(err) {
				t.Fatal("new native child master file was already present", err)
			}
			first := f.bundle.Review.Input.Parent.Owners[0].Owner
			beforeAliases, err := rfc2317NativeDNSQuery(ctx, first, dnsmessage.TypeCNAME)
			if err != nil || len(beforeAliases.Answers) != 0 {
				t.Fatal("native parent alias unexpectedly appeared before installation", err)
			}
			args := f.command(rfc2317NativeChecker)
			args[0] = "rfc2317-install"
			args = append(args, "--rndc", rfc2317NativeRNDC, "--rndc-key", key, "--control-endpoint", rfc2317NativeControl, "--dns-endpoint", rfc2317NativeDNS)
			run := func(args []string) ([]byte, error) {
				return exec.CommandContext(ctx, rfc2317NativeCLI, args...).CombinedOutput()
			}
			if out, err := run(args); err == nil || !bytes.Contains(out, []byte("--exclusive-maintenance are required")) {
				t.Fatal("CLI accepted a release without explicit exclusive maintenance", err)
			}
			args = append(args, "--exclusive-maintenance")
			for _, guard := range []struct {
				flag, value, diagnostic string
			}{
				{"--checksum", strings.Repeat("0", 64), "approved checksum"},
				{"--child-zone", "129-130.2.0.192.in-addr.arpa.", "explicitly expected parent and child zones"},
				{"--parent-file", filepath.Join(f.dir, "other-parent.zone"), "does not match the expected static zone"},
			} {
				bad := append([]string{}, args...)
				if guard.flag == "--parent-file" {
					writeRFC2317TestFile(t, guard.value, f.parentBytes)
				}
				for i := 1; i+1 < len(bad); i++ {
					if bad[i] == guard.flag {
						bad[i+1] = guard.value
						break
					}
				}
				if out, err := run(bad); err == nil || !bytes.Contains(out, []byte(guard.diagnostic)) {
					t.Fatalf("native install guard %s did not fail as expected: %v\n%s", guard.flag, err, out)
				}
			}
			if got := rfc2317NativeReloads(t, log); len(got) != 0 {
				t.Fatal("rejected install attempted a native reload", got)
			}
			activate := args
			if tc.loseChildReceipt {
				rfc2317NativeInterruptChildLoad(t, ctx, f, key, log)
				activate = append(append([]string{}, args...), "--resume")
			}
			out, err := run(activate)
			if err != nil {
				t.Fatalf("stock BIND install failed: %v\n%s", err, out)
			}
			var result struct {
				Activated           bool   `json:"activated"`
				TargetFilesVerified bool   `json:"targetFilesVerified"`
				BundleSHA256        string `json:"bundleSHA256"`
				provision.RFC2317TargetEvidence
			}
			if json.Unmarshal(out, &result) != nil || !result.Activated || !result.TargetFilesVerified || !result.NativeCandidatesVerified || result.BundleSHA256 != checksum(string(f.data)) || result.Parent.Candidate.SHA256 != f.bundle.Files[1].SHA256 || result.Child.Candidate.SHA256 != f.bundle.Files[0].SHA256 {
				t.Fatal("actual CLI lost the bound pair identity or activation receipt", string(out))
			}
			if got := rfc2317NativeReloads(t, log); !reflect.DeepEqual(got, []string{child, parent}) {
				t.Fatal("real BIND did not load child before parent exactly once", got)
			}
			for _, item := range f.bundle.Files {
				soa, err := rfc2317NativeDNSQuery(ctx, item.Zone, dnsmessage.TypeSOA)
				if err != nil || !rfc2317NativeSOAMatches(soa, item.Zone, item.Serial) {
					t.Fatal("TCP authoritative SOA differs from the exact approved zone", item.Zone, err)
				}
			}
			cname, err := rfc2317NativeDNSQuery(ctx, first, dnsmessage.TypeCNAME)
			var target string
			if err == nil && cname.Authoritative && cname.RCode == dnsmessage.RCodeSuccess && len(cname.Answers) == 1 && cname.Answers[0].Header.Name.String() == first {
				if answer, ok := cname.Answers[0].Body.(*dnsmessage.CNAMEResource); ok {
					target = answer.CNAME.String()
				}
			}
			if target != "128."+child {
				t.Fatal("native parent did not serve the approved CNAME", target, err)
			}
			ptr, err := rfc2317NativeDNSQuery(ctx, target, dnsmessage.TypePTR)
			if err != nil || !ptr.Authoritative || ptr.RCode != dnsmessage.RCodeSuccess || len(ptr.Answers) != 1 || ptr.Answers[0].Header.Name.String() != target {
				t.Fatal("native child did not serve the approved pointer", err)
			}
			answer, ok := ptr.Answers[0].Body.(*dnsmessage.PTRResource)
			if !ok || answer.PTR.String() != f.bundle.Review.Input.PTRs[0].Name {
				t.Fatal("native PTR answer did not match the reviewed child")
			}
			journal := filepath.Join(f.dir, ".moedove-rfc2317-"+checksum(filepath.Base(f.parent))[:32]+".json")
			record := func(path string) rfc2317LocalFile {
				t.Helper()
				local, err := readRFC2317LocalFile(path, 1<<20, false)
				if err != nil || !local.present {
					t.Fatal("installed private file could not be read", path, err)
				}
				return local
			}
			paths := []string{f.child, f.parent, journal}
			before := []rfc2317LocalFile{record(paths[0]), record(paths[1]), record(paths[2])}
			for i := range f.bundle.Files {
				if checksum(string(before[i].bytes)) != f.bundle.Files[i].SHA256 || string(before[i].bytes) != f.bundle.Files[i].Content {
					t.Fatal("published master bytes diverged from the exact approved files", paths[i])
				}
			}
			wrongResume := append(append([]string{}, args...), "--resume", "--dns-endpoint", "127.0.0.1:15354")
			if raw, err := run(wrongResume); err == nil || !bytes.Contains(raw, []byte("target binding differs")) {
				t.Fatalf("resume did not reject a different bound endpoint: %v\n%s", err, raw)
			}
			if retry, err := run(append(append([]string{}, args...), "--resume")); err != nil || !bytes.Equal(out, retry) {
				t.Fatalf("observed-only resume failed: %v\n%s", err, retry)
			}
			for i, path := range paths {
				if after := record(path); !before[i].matches(after) {
					t.Fatal("observed-only resume rewrote a master file or the approved journal", path)
				}
			}
			if got := rfc2317NativeReloads(t, log); !reflect.DeepEqual(got, []string{child, parent}) {
				t.Fatal("observed-only resume replayed a real BIND reload", got)
			}
			t.Log("rfc2317_bind_cli_install=true child_before_parent=true parent_cname_tcp=true child_ptr_tcp=true both_soa_tcp=true approved_exact_bytes=true wrong_binding_rejected=true resume_observed_only=true")
		})
	}
}
