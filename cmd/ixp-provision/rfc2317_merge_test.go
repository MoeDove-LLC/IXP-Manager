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

func TestRFC2317MergeCLIExactNativeOfflineArtifacts(t *testing.T) {
	dir := t.TempDir()
	parent := "$ORIGIN 2.0.192.in-addr.arpa.\n$TTL 3600\n@ IN SOA ns.parent.example.test. hostmaster.parent.example.test. (1 3600 600 604800 300)\n@ IN NS ns.parent.example.test.\n10 IN PTR retained.example.test.\n"
	in := rfcCLIInput()
	in.Parent.SnapshotSHA256 = checksum(parent)
	input, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	parentPath, reviewPath, mergePath := filepath.Join(dir, "original.zone"), filepath.Join(dir, "review.json"), filepath.Join(dir, "merged.json")
	if err = os.WriteFile(parentPath, []byte(parent), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := run([]string{"rfc2317-render", "--input", "-", "--output", reviewPath}, bytes.NewReader(input), io.Discard); err != nil {
		t.Fatal(err)
	}
	review, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"rfc2317-merge", "--file", reviewPath, "--checksum", checksum(string(review)), "--parent-zone", in.Parent.Zone, "--child-zone", in.ChildZone, "--parent-snapshot", parentPath, "--parent-serial", "2", "--output", mergePath}
	bad := append(append([]string{}, args...), "--named-checkzone", filepath.Join(dir, "absent"))
	if _, err = run(bad, nil, io.Discard); err == nil {
		t.Fatal("merge accepted without native checker")
	}
	if _, err = os.Stat(mergePath); !os.IsNotExist(err) {
		t.Fatal("unvalidated merge candidate saved")
	}
	binary := os.Getenv("NAMED_CHECKZONE_BIN")
	if binary == "" {
		t.Skip("set NAMED_CHECKZONE_BIN for the actual offline validator")
	}
	args = append(args, "--named-checkzone", binary)
	var output bytes.Buffer
	if _, err = run(args, nil, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"nativeRecordDeltaVerified":true`) || !strings.Contains(output.String(), `"activated":false`) || !strings.Contains(output.String(), `"approved":false`) || !strings.Contains(output.String(), `"liveParentVerified":false`) {
		t.Fatal("incorrect native or publication receipt", output.String())
	}
	original, err := os.ReadFile(parentPath)
	if err != nil || string(original) != parent {
		t.Fatal("parent snapshot changed after merge", err)
	}
	data, err := os.ReadFile(mergePath)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := provision.DecodeRFC2317Merged(data)
	if err != nil || bundle.PreviousParentSerial != 1 || bundle.ParentSerial != 2 || bundle.ChildSerial != 1 || bundle.AddedRecords != 3 || bundle.ReviewSHA256 != checksum(string(review)) {
		t.Fatal("output does not bind exact review and both serials", err)
	}
	if _, err = run(args, nil, io.Discard); err == nil {
		t.Fatal("existing merged bundle overwritten")
	}
	artifactArgs := []string{"--file", mergePath, "--checksum", checksum(string(data)), "--parent-zone", in.Parent.Zone, "--child-zone", in.ChildZone, "--named-checkzone", binary}
	if _, err = run(append([]string{"rfc2317-merge-check"}, artifactArgs...), nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "checked-zones")
	extract := append(append([]string{"rfc2317-merge-extract"}, artifactArgs...), "--output", destination)
	if _, err = run(extract, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, file := range bundle.Files {
		path := filepath.Join(destination, file.Path)
		actual, err := os.ReadFile(path)
		if err != nil || checksum(string(actual)) != file.SHA256 || string(actual) != file.Content {
			t.Fatal("emitted file differs from checked bundle", file.Path, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("emitted zone file is not private", file.Path, err)
		}
	}
	emittedBundle, err := os.ReadFile(filepath.Join(destination, "merge.json"))
	if err != nil || !bytes.Equal(emittedBundle, data) {
		t.Fatal("exact checked merge bundle not retained", err)
	}
	if _, err = run(extract, nil, io.Discard); err == nil {
		t.Fatal("existing directory overwritten")
	}
	for index, value := range map[int]string{3: strings.Repeat("0", 64), 5: "3.0.192.in-addr.arpa.", 7: "130-131.2.0.192.in-addr.arpa."} {
		bad := append([]string{}, artifactArgs...)
		bad[index] = value
		if _, err = run(append([]string{"rfc2317-merge-check"}, bad...), nil, io.Discard); err == nil {
			t.Fatal("changed checksum or zone accepted", index)
		}
	}
}
