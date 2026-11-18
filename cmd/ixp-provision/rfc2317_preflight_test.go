package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/moedove/ixp-manager/internal/provision"
)

type targetFixture struct {
	dir, bundlePath, parent, child string
	bundle                         provision.RFC2317MergedBundle
	data, parentBytes, childBytes  []byte
}

func newRFC2317TargetFixture(t *testing.T, existing bool) targetFixture {
	t.Helper()
	dir := t.TempDir()
	f := targetFixture{dir: dir, parent: filepath.Join(dir, "parent.zone"), child: filepath.Join(dir, "child.zone"), bundlePath: filepath.Join(dir, "download.json")}
	in := rfcCLIInput()
	f.parentBytes = []byte("$ORIGIN " + in.Parent.Zone + "\n$TTL 3600\n@ IN SOA ns.parent.example.test. hostmaster.parent.example.test. (1 3600 600 604800 300)\n@ IN NS ns.parent.example.test.\n10 IN PTR parent-fixture.example.test.\n")
	in.Parent.SnapshotSHA256 = checksum(string(f.parentBytes))
	if existing {
		zero := uint32(0)
		in.NewChildZone, in.PreviousChildSerial = false, &zero
		f.childBytes = []byte("$ORIGIN " + in.ChildZone + "\n$TTL 300\n@ IN SOA ns.child.example.test. hostmaster.child.example.test. (0 3600 600 604800 300)\n@ IN NS ns.child.example.test.\n128 IN PTR previous-child-fixture.example.test.\n")
		writeRFC2317TestFile(t, f.child, f.childBytes)
	}
	review, err := provision.RenderRFC2317(in)
	if err != nil {
		t.Fatal(err)
	}
	f.bundle, err = provision.PrepareRFC2317Merge(review, f.parentBytes, 2)
	if err != nil {
		t.Fatal(err)
	}
	f.data, err = provision.EncodeRFC2317Merged(f.bundle)
	if err != nil {
		t.Fatal(err)
	}
	writeRFC2317TestFile(t, f.parent, f.parentBytes)
	writeRFC2317TestFile(t, f.bundlePath, f.data)
	return f
}

func writeRFC2317TestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func (f targetFixture) command(checker string) []string {
	in := f.bundle.Review.Input
	return []string{
		"rfc2317-preflight", "--file", f.bundlePath, "--checksum", checksum(string(f.data)),
		"--parent-file", f.parent, "--child-file", f.child,
		"--parent-zone", in.Parent.Zone, "--child-zone", in.ChildZone,
		"--named-checkzone", checker,
	}
}

func TestRFC2317PreflightRejectsConflictsBeforeValidators(t *testing.T) {
	for _, test := range []struct {
		name     string
		existing bool
		tweak    func(*testing.T, targetFixture, []string)
	}{
		{"approved checksum mismatch", false, func(t *testing.T, f targetFixture, args []string) { args[4] = strings.Repeat("0", 64) }},
		{"expected zone mismatch", false, func(t *testing.T, f targetFixture, args []string) { args[12] = "129-130.2.0.192.in-addr.arpa." }},
		{"changed parent bytes with same SOA", false, func(t *testing.T, f targetFixture, args []string) {
			writeRFC2317TestFile(t, f.parent, append(f.parentBytes, []byte("; drift\n")...))
		}},
		{"expected parent missing", false, func(t *testing.T, f targetFixture, args []string) {
			args[6] = filepath.Join(f.dir, "missing-parent.zone")
		}},
		{"new child path occupied", false, func(t *testing.T, f targetFixture, args []string) {
			writeRFC2317TestFile(t, f.child, []byte(f.bundle.Files[0].Content))
		}},
		{"no parent directory is not absent child", false, func(t *testing.T, f targetFixture, args []string) {
			args[8] = filepath.Join(f.dir, "missing", "child.zone")
		}},
		{"dangling target symlink is not absence", false, func(t *testing.T, f targetFixture, args []string) {
			if err := os.Symlink(filepath.Join(f.dir, "no-such-file"), f.child); err != nil {
				t.Fatal(err)
			}
		}},
		{"parent through symlinked ancestor", false, func(t *testing.T, f targetFixture, args []string) {
			alias := filepath.Join(f.dir, "alias")
			if err := os.Symlink(f.dir, alias); err != nil {
				t.Fatal(err)
			}
			args[6] = filepath.Join(alias, filepath.Base(f.parent))
		}},
		{"same target inode", false, func(t *testing.T, f targetFixture, args []string) {
			if err := os.Link(f.parent, f.child); err != nil {
				t.Fatal(err)
			}
		}},
		{"dynamic sidecar without a child leaf", false, func(t *testing.T, f targetFixture, args []string) {
			writeRFC2317TestFile(t, f.child+".jnl", []byte("journal"))
		}},
		{"special file", false, func(t *testing.T, f targetFixture, args []string) {
			if err := syscall.Mkfifo(f.child, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"expected existing child missing", true, func(t *testing.T, f targetFixture, args []string) {
			args[8] = filepath.Join(f.dir, "missing-child.zone")
		}},
		{"existing child unknown SOA", true, func(t *testing.T, f targetFixture, args []string) {
			writeRFC2317TestFile(t, f.child, []byte(strings.Replace(string(f.childBytes), "(0 3600", "(9 3600", 1)))
		}},
		{"existing child other zone", true, func(t *testing.T, f targetFixture, args []string) {
			writeRFC2317TestFile(t, f.child, []byte(strings.Replace(string(f.childBytes), f.bundle.Review.Input.ChildZone, "128-131.3.0.192.in-addr.arpa.", 1)))
		}},
		{"existing child include", true, func(t *testing.T, f targetFixture, args []string) {
			writeRFC2317TestFile(t, f.child, append(f.childBytes, []byte("  $iNcLuDe bogus.master\n")...))
		}},
		{"existing child generator", true, func(t *testing.T, f targetFixture, args []string) {
			writeRFC2317TestFile(t, f.child, append(f.childBytes, []byte("\t$GENERATE 128-131 $ PTR host$.example.test.\n")...))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newRFC2317TargetFixture(t, test.existing)
			marker := filepath.Join(f.dir, "validator-started")
			t.Setenv("RFC2317_PRECHECK_MARKER", marker)
			checker := filepath.Join(f.dir, "marker-checker")
			if err := os.WriteFile(checker, []byte("#!/bin/sh\n: > \"$RFC2317_PRECHECK_MARKER\"\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			args := f.command(checker)
			test.tweak(t, f, args)
			var out bytes.Buffer
			if _, err := run(args, nil, &out); err == nil || out.Len() != 0 {
				t.Fatal("conflicting target emitted preflight evidence", err, out.String())
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("unsafe or mismatched input reached a native validator")
			}
		})
	}
}

func TestRFC2317NativePreflightChecksBothPathsAndRedacts(t *testing.T) {
	binary := os.Getenv("NAMED_CHECKZONE_BIN")
	if binary == "" {
		t.Skip("set NAMED_CHECKZONE_BIN for actual BIND preflight")
	}
	for _, existing := range []bool{false, true} {
		name := "new_child"
		if existing {
			name = "existing_serial_zero"
		}
		t.Run(name, func(t *testing.T) {
			f := newRFC2317TargetFixture(t, existing)
			var output bytes.Buffer
			if _, err := run(f.command(binary), nil, &output); err != nil {
				t.Fatal(err)
			}
			var got struct {
				Kind                string `json:"kind"`
				BundleSHA256        string `json:"bundleSHA256"`
				TargetFilesVerified bool   `json:"targetFilesVerified"`
				Activated           bool   `json:"activated"`
				provision.RFC2317TargetEvidence
			}
			if err := json.Unmarshal(output.Bytes(), &got); err != nil || got.Kind != "dns-rfc2317-target-preflight" || got.BundleSHA256 != checksum(string(f.data)) || !got.TargetFilesVerified || !got.NativeCandidatesVerified || got.Activated {
				t.Fatal("incorrect preflight evidence", err, output.String())
			}
			if got.Parent.Zone != f.bundle.Review.Input.Parent.Zone || !got.Parent.Current.Present || got.Parent.Current.SHA256 != checksum(string(f.parentBytes)) || got.Parent.Current.Serial == nil || *got.Parent.Current.Serial != 1 || got.Parent.Candidate.SHA256 != f.bundle.Files[1].SHA256 || got.Parent.Candidate.Serial == nil || *got.Parent.Candidate.Serial != 2 {
				t.Fatal("parent snapshot/candidate not bound by evidence", got.Parent)
			}
			if got.Child.Zone != f.bundle.Review.Input.ChildZone || got.Child.Current.Present != existing || got.Child.Candidate.SHA256 != f.bundle.Files[0].SHA256 || got.Child.Candidate.Serial == nil || *got.Child.Candidate.Serial != 1 {
				t.Fatal("child target/candidate not bound by evidence", got.Child)
			}
			if existing && (got.Child.Current.Serial == nil || *got.Child.Current.Serial != 0 || got.Child.Current.SHA256 != checksum(string(f.childBytes))) || !existing && (got.Child.Current.Serial != nil || got.Child.Current.SHA256 != "") {
				t.Fatal("existing zero and verified absent leaf were conflated", got.Child.Current)
			}
			if text := output.String(); strings.Contains(text, "fixture.example.test") || strings.Contains(text, f.dir) || strings.Contains(text, "$ORIGIN") {
				t.Fatal("preflight evidence exposed a file path or raw master-file data")
			}
			for path, want := range map[string][]byte{f.bundlePath: f.data, f.parent: f.parentBytes, f.child: f.childBytes} {
				actual, err := os.ReadFile(path)
				if want == nil && !os.IsNotExist(err) || want != nil && (err != nil || !bytes.Equal(actual, want)) {
					t.Fatal("preflight changed a target file", err)
				}
			}
		})
	}
}

func TestRFC2317PreflightRechecksFilesAfterNativeValidation(t *testing.T) {
	binary := os.Getenv("NAMED_CHECKZONE_BIN")
	if binary == "" {
		t.Skip("set NAMED_CHECKZONE_BIN for actual BIND preflight")
	}
	f := newRFC2317TargetFixture(t, false)
	t.Setenv("RFC2317_PRECHECK_BIND", binary)
	t.Setenv("RFC2317_PRECHECK_PARENT", f.parent)
	checker := filepath.Join(f.dir, "concurrent-write-fixture")
	script := "#!/bin/sh\n\"$RFC2317_PRECHECK_BIND\" \"$@\" || exit 1\nprintf '\\n; concurrent writer fixture\\n' >> \"$RFC2317_PRECHECK_PARENT\"\n"
	if err := os.WriteFile(checker, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if _, err := run(f.command(checker), nil, &output); err == nil || !strings.Contains(err.Error(), "target parent changed during preflight") || output.Len() != 0 {
		t.Fatal("concurrently changed target produced evidence", err, output.String())
	}
}
