package provision_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/provision"
)

func rfc2317Fixture(prefix string) provision.RFC2317Input {
	p, _ := netip.ParsePrefix(prefix)
	b := p.Addr().As4()
	first := uint16(b[3])
	last := first + uint16((1<<uint(32-p.Bits()))-1)
	serial := uint32(41)
	parent := "2.0.192.in-addr.arpa."
	child := fmt.Sprintf("%d-%d.%s", first, last, parent)
	sum := sha256.Sum256([]byte("synthetic operator-reviewed parent snapshot; not a live parent"))
	in := provision.RFC2317Input{Version: 1, Parent: provision.RFC2317ParentReview{Zone: parent, SnapshotSHA256: hex.EncodeToString(sum[:]), ObservedSerial: &serial, TTL: 3600, Delegation: provision.RFC2317DelegationClaim{Owner: child, State: "absent"}, Owners: []provision.RFC2317OwnerClaim{}}, ChildZone: child, First: &first, Last: &last, NewChildZone: true, DNS: provision.DNSOptions{Prefix: prefix, Serial: 17, PrimaryNS: "NS1.Example.Test.", ResponsibleMailbox: "Hostmaster.Example.Test.", NameServers: []string{"ns2.example.test.", "NS1.Example.Test."}}, PTRs: []provision.RFC2317PTR{{Address: p.Addr().String(), Name: "Host-A.Customer.Example.Test."}}}
	for n := first; n <= last; n++ {
		in.Parent.Owners = append(in.Parent.Owners, provision.RFC2317OwnerClaim{Owner: fmt.Sprintf("%d.%s", n, parent), State: "absent"})
	}
	return in
}

func TestRFC2317ExactOwnershipAndSeparateArtifacts(t *testing.T) {
	in := rfc2317Fixture("192.0.2.128/30")
	before, _ := json.Marshal(in)
	bundle, err := provision.RenderRFC2317(in)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(in)
	if string(before) != string(after) {
		t.Fatal("normalization mutated source")
	}
	if len(bundle.Files) != 2 || !bundle.Files[0].Complete || bundle.Files[1].Complete || bundle.ParentIntegrated || bundle.ParentSnapshotVerified || bundle.Approved || bundle.Activated {
		t.Fatal("fragment or operator claims became approved authority", bundle)
	}
	child, parent := bundle.Files[0].Content, bundle.Files[1].Content
	if !strings.Contains(child, "128.128-131.2.0.192.in-addr.arpa. IN PTR host-a.customer.example.test.") || strings.Contains(child, "128.2.0.192.in-addr.arpa. IN PTR") {
		t.Fatal("child PTR owner is outside child")
	}
	if strings.Count(parent, " IN CNAME ") != 4 || strings.Count(parent, " IN NS ") != 2 || strings.Contains(parent, " IN SOA ") || strings.Contains(parent, "$ORIGIN") || !strings.Contains(parent, "NOT a complete zone") {
		t.Fatal("parent fragment scope/authority changed", parent)
	}
	for n := 128; n <= 131; n++ {
		if !strings.Contains(parent, fmt.Sprintf("%d.2.0.192.in-addr.arpa. 3600 IN CNAME %d.128-131.2.0.192.in-addr.arpa.", n, n)) {
			t.Fatal("missing exact CNAME owner/target", n)
		}
	}
	raw, err := provision.EncodeRFC2317(bundle)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := provision.DecodeRFC2317(raw)
	if err != nil || !reflect.DeepEqual(decoded, bundle) {
		t.Fatal("canonical bundle roundtrip", err)
	}
	for _, file := range bundle.Files {
		if err := provision.ValidateArtifact(provision.Artifact{Kind: "dns-arpa", Zone: file.Zone, Content: file.Content, Filename: file.Path, Complete: true, ValidationLevel: "native"}); err == nil {
			t.Fatal("RFC2317 artifact entered old authoritative-zone approval path")
		}
	}
	in.Parent.Delegation.State = "ns"
	in.Parent.Delegation.NameServers = []string{"NS1.Example.Test.", "ns2.example.test."}
	for i := range in.Parent.Owners {
		in.Parent.Owners[i].State = "cname"
		in.Parent.Owners[i].Target = strings.TrimSuffix(in.Parent.Owners[i].Owner, in.Parent.Zone) + in.ChildZone
	}
	if _, err := provision.RenderRFC2317(in); err != nil {
		t.Fatal("matching existing ownership not accepted", err)
	}
}

func TestRFC2317RejectsUnreviewedOrConflictingClaims(t *testing.T) {
	cases := map[string]func(*provision.RFC2317Input){
		"version":                 func(v *provision.RFC2317Input) { v.Version = 2 },
		"missing-range":           func(v *provision.RFC2317Input) { v.First = nil },
		"wrong-range":             func(v *provision.RFC2317Input) { n := uint16(132); v.Last = &n },
		"parent-mismatch":         func(v *provision.RFC2317Input) { v.Parent.Zone = "3.0.192.in-addr.arpa." },
		"child-mismatch":          func(v *provision.RFC2317Input) { v.ChildZone = "0-3.2.0.192.in-addr.arpa." },
		"slash-name":              func(v *provision.RFC2317Input) { v.ChildZone = "128/30.2.0.192.in-addr.arpa." },
		"octet-zone":              func(v *provision.RFC2317Input) { v.DNS.Prefix = "192.0.2.0/24" },
		"noncanonical-prefix":     func(v *provision.RFC2317Input) { v.DNS.Prefix = "192.0.2.129/30" },
		"IPv6":                    func(v *provision.RFC2317Input) { v.DNS.Prefix = "2001:db8::/64" },
		"missing-parent-serial":   func(v *provision.RFC2317Input) { v.Parent.ObservedSerial = nil },
		"invalid-parent-checksum": func(v *provision.RFC2317Input) { v.Parent.SnapshotSHA256 = "unknown" },
		"missing-parent-ttl":      func(v *provision.RFC2317Input) { v.Parent.TTL = 0 },
		"missing-owner":           func(v *provision.RFC2317Input) { v.Parent.Owners = v.Parent.Owners[:3] },
		"duplicate-owner":         func(v *provision.RFC2317Input) { v.Parent.Owners[1] = v.Parent.Owners[0] },
		"outside-owner":           func(v *provision.RFC2317Input) { v.Parent.Owners[0].Owner = "127.2.0.192.in-addr.arpa." },
		"existing-PTR":            func(v *provision.RFC2317Input) { v.Parent.Owners[0].State = "ptr" },
		"foreign-CNAME": func(v *provision.RFC2317Input) {
			v.Parent.Owners[0].State = "cname"
			v.Parent.Owners[0].Target = "128.other.example.test."
		},
		"unknown-delegation": func(v *provision.RFC2317Input) { v.Parent.Delegation.State = "unknown" },
		"changed-delegation": func(v *provision.RFC2317Input) {
			v.Parent.Delegation.State = "ns"
			v.Parent.Delegation.NameServers = []string{"ns.other.test."}
		},
		"glue-required":  func(v *provision.RFC2317Input) { v.DNS.NameServers = []string{"ns." + v.ChildZone} },
		"injection":      func(v *provision.RFC2317Input) { v.DNS.PrimaryNS = "ns.example.test.\n$INCLUDE /private" },
		"outside-PTR":    func(v *provision.RFC2317Input) { v.PTRs[0].Address = "192.0.2.127" },
		"duplicate-PTR":  func(v *provision.RFC2317Input) { v.PTRs = append(v.PTRs, v.PTRs[0]) },
		"unnamed-PTR":    func(v *provision.RFC2317Input) { v.PTRs[0].Name = "" },
		"implicit-names": func(v *provision.RFC2317Input) { v.DNS.NameSuffix = "fallback.example.test." },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			v := rfc2317Fixture("192.0.2.128/30")
			change(&v)
			if _, err := provision.RenderRFC2317(v); err == nil {
				t.Fatal("unsafe typed scope accepted")
			}
		})
	}
}

func TestRFC2317SerialAndCanonicalReviewGuard(t *testing.T) {
	for _, pair := range []struct {
		previous, next uint32
		valid          bool
	}{{0, 1, true}, {4294967295, 1, true}, {17, 17, false}, {17, 16, false}, {1, 2147483649, false}} {
		v := rfc2317Fixture("192.0.2.128/30")
		v.NewChildZone = false
		v.PreviousChildSerial = &pair.previous
		v.DNS.Serial = pair.next
		_, err := provision.RenderRFC2317(v)
		if (err == nil) != pair.valid {
			t.Fatal("RFC1982 boundary", pair, err)
		}
	}
	v := rfc2317Fixture("192.0.2.128/30")
	baseline := uint32(0)
	v.PreviousChildSerial = &baseline
	if _, err := provision.RenderRFC2317(v); err == nil {
		t.Fatal("existing serial silently reset as new child")
	}
	v = rfc2317Fixture("192.0.2.128/30")
	bundle, err := provision.RenderRFC2317(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, tamper := range []func(*provision.RFC2317Bundle){func(b *provision.RFC2317Bundle) { b.ParentIntegrated = true }, func(b *provision.RFC2317Bundle) { b.ParentSnapshotVerified = true }, func(b *provision.RFC2317Bundle) { b.Approved = true }, func(b *provision.RFC2317Bundle) { b.Files[1].Complete = true }, func(b *provision.RFC2317Bundle) {
		b.Files[1].Content += "127.2.0.192.in-addr.arpa. IN CNAME stolen.example.test.\n"
	}, func(b *provision.RFC2317Bundle) { b.Input.DNS.Serial++ }} {
		raw, _ := json.Marshal(bundle)
		var copy provision.RFC2317Bundle
		json.Unmarshal(raw, &copy)
		tamper(&copy)
		if err := provision.ValidateRFC2317Bundle(copy); err == nil {
			t.Fatal("tampered scope/authority accepted")
		}
	}
	raw, _ := json.Marshal(v)
	duplicate := strings.Replace(string(raw), `"version":1`, `"version":1,"Version":1`, 1)
	if _, err := provision.DecodeRFC2317Input([]byte(duplicate)); err == nil {
		t.Fatal("case-alias duplicate fields accepted")
	}
	encoded, _ := provision.EncodeRFC2317(bundle)
	if _, err := provision.DecodeRFC2317(append(encoded, ' ')); err == nil {
		t.Fatal("changed canonical bytes accepted")
	}
	result, err := provision.ValidateRFC2317(context.Background(), bundle, provision.ValidatorOptions{NamedCheckzone: filepath.Join(t.TempDir(), "absent")})
	if err == nil || result.Child.Level == "native" {
		t.Fatal("missing checker claimed native validation")
	}
}

func TestRFC2317NativeChildAndExplicitSyntheticParent(t *testing.T) {
	binary := os.Getenv("NAMED_CHECKZONE_BIN")
	if binary == "" {
		t.Skip("set NAMED_CHECKZONE_BIN for the actual offline validator")
	}
	for _, prefix := range []string{"192.0.2.0/25", "192.0.2.128/26", "192.0.2.254/31", "192.0.2.255/32"} {
		t.Run(prefix, func(t *testing.T) {
			b, err := provision.RenderRFC2317(rfc2317Fixture(prefix))
			if err != nil {
				t.Fatal(err)
			}
			v, err := provision.ValidateRFC2317(context.Background(), b, provision.ValidatorOptions{NamedCheckzone: binary})
			if err != nil || v.Child.Level != "native" || v.ParentFragment.Level != "native-synthetic-wrapper" || v.ParentSnapshotVerified || v.ParentIntegrated || v.Approved || v.Activated {
				t.Fatal(v, err)
			}
			fragment := filepath.Join(t.TempDir(), "parent.fragment")
			if err := os.WriteFile(fragment, []byte(b.Files[1].Content), 0600); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command(binary, "-i", "local", b.Input.Parent.Zone, fragment).CombinedOutput(); err == nil {
				t.Fatal("parent fragment was accepted as a complete authority", string(output))
			}
		})
	}
	b, _ := provision.RenderRFC2317(rfc2317Fixture("192.0.2.128/30"))
	conflict := "$ORIGIN 2.0.192.in-addr.arpa.\n$TTL 300\n@ IN SOA ns.parent.invalid. hostmaster.parent.invalid. ( 41 3600 600 604800 300 )\n@ IN NS ns.parent.invalid.\n128 IN PTR old.customer.example.test.\n" + b.Files[1].Content
	file := filepath.Join(t.TempDir(), "conflicting-parent.zone")
	os.WriteFile(file, []byte(conflict), 0600)
	if output, err := exec.Command(binary, "-i", "local", b.Input.Parent.Zone, file).CombinedOutput(); err == nil {
		t.Fatal("native checker accepted parent PTR/CNAME ownership collision", string(output))
	}
}
