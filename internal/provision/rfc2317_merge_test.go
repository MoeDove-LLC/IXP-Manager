package provision_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/provision"
)

const completeRFC2317Parent = `; Parent fixture with explicit authority and unrelated records.
$ORIGIN 2.0.192.in-addr.arpa.
$TTL 1h
@ IN SOA ns.parent.example.test. hostmaster.parent.example.test. (
  00041 ; source serial
  1h 10m 1w 5m )
  IN NS ns.parent.example.test.
10 220 IN PTR unrelated.customer.example.test.
15 IN TXT "literal ; $INCLUDE /path-to-nowhere"
0-31 IN NS ns.unrelated.example.test.
`

func rfc2317ParentReview(t *testing.T, raw string, observed uint32, changes func(*provision.RFC2317Input)) provision.RFC2317Bundle {
	t.Helper()
	in := rfc2317Fixture("192.0.2.128/30")
	in.Parent.SnapshotSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
	in.Parent.ObservedSerial = &observed
	if changes != nil {
		changes(&in)
	}
	bundle, err := provision.RenderRFC2317(in)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestRFC2317MergePreservesSourceAndMatchingOwners(t *testing.T) {
	parent := completeRFC2317Parent + "128 1799 IN CNAME 128.128-131\n"
	parent = strings.ReplaceAll(parent, "\n", "\r\n")
	review := rfc2317ParentReview(t, parent, 41, func(in *provision.RFC2317Input) {
		in.Parent.Owners[0].State = "cname"
		in.Parent.Owners[0].Target = "128." + in.ChildZone
	})
	bundle, err := provision.PrepareRFC2317Merge(review, []byte(parent), 42)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.ParentSnapshot != parent || !bundle.ParentSnapshotVerified || bundle.LiveParentVerified || bundle.Approved || bundle.Activated || bundle.AddedRecords != 5 || bundle.ParentSerial != 42 || bundle.PreviousParentSerial != 41 || bundle.ChildSerial != 17 {
		t.Fatal("local verification or serial bounds were lost")
	}
	newParent := bundle.Files[1].Content
	want := strings.Replace(parent, "00041 ; source serial", "42 ; source serial", 1)
	if !strings.HasPrefix(newParent, want) || strings.Count(newParent, " IN CNAME ") != 4 || strings.Contains(strings.ReplaceAll(newParent, "\r\n", ""), "\n") {
		t.Fatal("parent bytes, alias set or newline style changed unexpectedly")
	}
	if bundle.Files[0].Content != review.Files[0].Content || bundle.Files[0].SHA256 != review.Files[0].SHA256 || !bundle.Files[0].Complete || !bundle.Files[1].Complete {
		t.Fatal("complete files lost their identity or child was changed")
	}
	raw, err := provision.EncodeRFC2317Merged(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provision.DecodeRFC2317Merged(raw); err != nil {
		t.Fatal(err)
	}
	if _, err = provision.DecodeRFC2317Merged(append(bytes.Clone(raw), ' ')); err == nil {
		t.Fatal("noncanonical merged bundle accepted")
	}
	bundle.Files[1].Content += "132 IN CNAME outside.example.test.\n"
	bundle.Files[1].SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(bundle.Files[1].Content)))
	if _, err = provision.EncodeRFC2317Merged(bundle); err == nil {
		t.Fatal("unreviewed parent delta accepted after recomputing a file hash")
	}
}

func TestRFC2317MergeRejectsSnapshotDriftAndConflicts(t *testing.T) {
	cases := []struct{ name, parent, want string }{
		{"PTR collision", completeRFC2317Parent + "128 IN PTR occupied.example.test.\n", "declared absent"},
		{"unclaimed CNAME", completeRFC2317Parent + "128 IN CNAME wrong.example.test.\n", "declared absent"},
		{"unclaimed delegation", completeRFC2317Parent + "128-131 IN NS wrong.example.test.\n", "delegation declared absent"},
		{"deeper child data", completeRFC2317Parent + "128.128-131 IN TXT \"exists\"\n", "below the child delegation"},
		{"deeper address data", completeRFC2317Parent + "nested.131 IN PTR occupied.example.test.\n", "below a claimed CNAME owner"},
		{"overlapping range", completeRFC2317Parent + "128-159 IN NS wrong.example.test.\n", "overlaps"},
		{"overlapping slash", completeRFC2317Parent + "128/25 IN NS wrong.example.test.\n", "overlaps"},
		{"wildcard", completeRFC2317Parent + "* IN PTR fallback.example.test.\n", "wildcard"},
		{"DNAME", completeRFC2317Parent + "128 IN DNAME fallback.example.test.\n", "unsupported parent RR"},
		{"hidden include", completeRFC2317Parent + "  \t$iNcLuDe /path-to-nowhere\n", "before native parsing"},
		{"generate", completeRFC2317Parent + "$GENERATE 1-10 $ PTR name$.example.test.\n", "before native parsing"},
		{"invalid control", completeRFC2317Parent + "; comment\x00\n", "unsupported control"},
		{"serial drift", strings.Replace(completeRFC2317Parent, "00041", "00040", 1), "observed serial"},
		{"zone drift", strings.Replace(completeRFC2317Parent, "$ORIGIN 2.", "$ORIGIN 3.", 1), "exact absolute origin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			review := rfc2317ParentReview(t, tc.parent, 41, nil)
			if _, err := provision.PrepareRFC2317Merge(review, []byte(tc.parent), 42); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
	review := rfc2317ParentReview(t, completeRFC2317Parent, 41, nil)
	if _, err := provision.PrepareRFC2317Merge(review, []byte(completeRFC2317Parent+"; changed bytes\n"), 42); err == nil {
		t.Fatal("changed snapshot bytes accepted")
	}
}

func TestRFC2317MergeParentSerialAndExistingDelegation(t *testing.T) {
	parent := strings.Replace(completeRFC2317Parent, "00041", "4294967295", 1) +
		"128-131 1750 IN NS ns1.example.test.\n128-131 1750 IN NS ns2.example.test.\n"
	for n := 128; n <= 131; n++ {
		parent += fmt.Sprintf("%d 1710 IN CNAME %d.128-131\n", n, n)
	}
	matching := func(in *provision.RFC2317Input) {
		in.Parent.Delegation.State = "ns"
		in.Parent.Delegation.NameServers = []string{"ns1.example.test.", "ns2.example.test."}
		for i := range in.Parent.Owners {
			in.Parent.Owners[i].State = "cname"
			in.Parent.Owners[i].Target = fmt.Sprintf("%d.%s", i+128, in.ChildZone)
		}
	}
	review := rfc2317ParentReview(t, parent, math.MaxUint32, matching)
	for _, bad := range []uint32{0, math.MaxUint32, math.MaxUint32 - 1, math.MaxInt32} {
		if _, err := provision.PrepareRFC2317Merge(review, []byte(parent), bad); err == nil {
			t.Fatal("invalid, unchanged or ambiguous parent serial accepted", bad)
		}
	}
	bundle, err := provision.PrepareRFC2317Merge(review, []byte(parent), 1)
	if err != nil || bundle.AddedRecords != 0 || bundle.Files[1].Content != strings.Replace(parent, "4294967295", "1", 1) {
		t.Fatalf("wrapped serial must preserve matching NS/CNAME RRsets: %v", err)
	}
	for _, change := range []struct{ old, replacement string }{{"ns1.example.test.\n", "other.example.test.\n"}, {"128.128-131\n", "129.128-131\n"}} {
		conflict := strings.Replace(parent, change.old, change.replacement, 1)
		review := rfc2317ParentReview(t, conflict, math.MaxUint32, matching)
		if _, err := provision.PrepareRFC2317Merge(review, []byte(conflict), 1); err == nil {
			t.Fatal("actual matching NS/CNAME ownership was not checked")
		}
	}
}

func TestRFC2317MergeNativeWholeZoneAndReadback(t *testing.T) {
	review := rfc2317ParentReview(t, completeRFC2317Parent, 41, nil)
	merged, err := provision.PrepareRFC2317Merge(review, []byte(completeRFC2317Parent), 42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provision.ValidateRFC2317Merge(context.Background(), merged, provision.ValidatorOptions{NamedCheckzone: filepath.Join(t.TempDir(), "absent")}); err == nil {
		t.Fatal("missing checker accepted")
	}
	binary := os.Getenv("NAMED_CHECKZONE_BIN")
	if binary == "" {
		t.Skip("set NAMED_CHECKZONE_BIN for the actual offline validator")
	}
	validation, err := provision.ValidateRFC2317Merge(context.Background(), merged, provision.ValidatorOptions{NamedCheckzone: binary})
	if err != nil {
		t.Fatal(err)
	}
	if validation.ParentSnapshot.Level != "native" || validation.ParentCandidate.Level != "native" || validation.Child.Level != "native" || !validation.NativeRecordDeltaVerified || !validation.ParentSnapshotVerified || validation.LiveParentVerified || validation.Approved || validation.Activated {
		t.Fatal("incorrect native or publication evidence", validation)
	}
	invalid := completeRFC2317Parent + "15 IN CNAME impossible.example.test.\n"
	review = rfc2317ParentReview(t, invalid, 41, nil)
	merged, err = provision.PrepareRFC2317Merge(review, []byte(invalid), 42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provision.ValidateRFC2317Merge(context.Background(), merged, provision.ValidatorOptions{NamedCheckzone: binary}); err == nil {
		t.Fatal("structural snapshot with unrelated invalid CNAME coexistence was accepted by native validation")
	}
}
