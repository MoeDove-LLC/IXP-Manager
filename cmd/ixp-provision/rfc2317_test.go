package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/provision"
)

func rfcCLIInput() provision.RFC2317Input {
	first, last := uint16(128), uint16(129)
	serial := uint32(1)
	return provision.RFC2317Input{Version: 1, Parent: provision.RFC2317ParentReview{Zone: "2.0.192.in-addr.arpa.", SnapshotSHA256: strings.Repeat("a", 64), ObservedSerial: &serial, TTL: 3600, Delegation: provision.RFC2317DelegationClaim{Owner: "128-129.2.0.192.in-addr.arpa.", State: "absent"}, Owners: []provision.RFC2317OwnerClaim{{Owner: "128.2.0.192.in-addr.arpa.", State: "absent"}, {Owner: "129.2.0.192.in-addr.arpa.", State: "absent"}}}, ChildZone: "128-129.2.0.192.in-addr.arpa.", First: &first, Last: &last, NewChildZone: true, DNS: provision.DNSOptions{Prefix: "192.0.2.128/31", Serial: 1, PrimaryNS: "ns.example.test.", ResponsibleMailbox: "hostmaster.example.test.", NameServers: []string{"ns.example.test."}}, PTRs: []provision.RFC2317PTR{{Address: "192.0.2.128", Name: "host.example.test."}}}
}
func TestRFC2317CLIPinsSeparateFilesAndNeverApproves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review.json")
	raw, _ := json.Marshal(rfcCLIInput())
	var output bytes.Buffer
	if _, err := run([]string{"rfc2317-render", "--input", "-", "--output", path}, bytes.NewReader(raw), &output); err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	json.Unmarshal(output.Bytes(), &metadata)
	if metadata["validated"] != false || metadata["approved"] != false || metadata["parentComplete"] != false || metadata["activated"] != false {
		t.Fatal(metadata)
	}
	data, _ := os.ReadFile(path)
	sum := checksum(string(data))
	if metadata["sha256"] != sum {
		t.Fatal("review checksum mismatch")
	}
	if _, err := run([]string{"rfc2317-render", "--input", "-", "--output", path}, bytes.NewReader(raw), io.Discard); err == nil {
		t.Fatal("review bundle overwritten")
	}
	args := []string{"--file", path, "--checksum", sum, "--parent-zone", "2.0.192.in-addr.arpa.", "--child-zone", "128-129.2.0.192.in-addr.arpa."}
	destination := filepath.Join(dir, "review-files")
	if _, err := run(append([]string{"rfc2317-extract"}, append(args, "--output", destination)...), nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"child.zone", "parent.fragment", "review.json"} {
		info, err := os.Stat(filepath.Join(destination, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("private extracted file missing", name, err)
		}
	}
	info, _ := os.Stat(destination)
	if info.Mode().Perm() != 0700 {
		t.Fatal("review directory not private")
	}
	if _, err := run(append([]string{"rfc2317-extract"}, append(args, "--output", destination)...), nil, io.Discard); err == nil {
		t.Fatal("existing directory overwritten")
	}
	bad := append([]string{}, args...)
	bad[3] = strings.Repeat("0", 64)
	if _, err := run(append([]string{"rfc2317-extract"}, append(bad, "--output", filepath.Join(dir, "bad"))...), nil, io.Discard); err == nil {
		t.Fatal("unreviewed bytes extracted")
	}
	bad = append([]string{}, args...)
	bad[7] = "0-1.2.0.192.in-addr.arpa."
	if _, err := run(append([]string{"rfc2317-extract"}, append(bad, "--output", filepath.Join(dir, "wrong-target"))...), nil, io.Discard); err == nil {
		t.Fatal("wrong child identity extracted")
	}
	binary := os.Getenv("NAMED_CHECKZONE_BIN")
	if binary != "" {
		output.Reset()
		if _, err := run(append([]string{"rfc2317-check"}, append(args, "--named-checkzone", binary)...), nil, &output); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), `"parentIntegrated":false`) || !strings.Contains(output.String(), `"approved":false`) {
			t.Fatal(output.String())
		}
	}
}
