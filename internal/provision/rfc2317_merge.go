package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"time"
)

const RFC2317MergeKind = "dns-rfc2317-parent-merge"
const MaxRFC2317MergeBytes = 16 << 20

type RFC2317CompleteFile struct {
	Path     string `json:"path"`
	Zone     string `json:"zone"`
	Serial   uint32 `json:"serial"`
	Complete bool   `json:"complete"`
	SHA256   string `json:"sha256"`
	Content  string `json:"content"`
}
type RFC2317MergedBundle struct {
	Version                int                   `json:"version"`
	Kind                   string                `json:"kind"`
	Review                 RFC2317Bundle         `json:"review"`
	ReviewSHA256           string                `json:"reviewSHA256"`
	ParentSnapshot         string                `json:"parentSnapshot"`
	ParentSnapshotSHA256   string                `json:"parentSnapshotSHA256"`
	PreviousParentSerial   uint32                `json:"previousParentSerial"`
	ParentSerial           uint32                `json:"parentSerial"`
	ChildSerial            uint32                `json:"childSerial"`
	AddedRecords           int                   `json:"addedRecords"`
	Files                  []RFC2317CompleteFile `json:"files"`
	ParentSnapshotVerified bool                  `json:"parentSnapshotVerified"`
	Approved               bool                  `json:"approved"`
	Activated              bool                  `json:"activated"`
	LiveParentVerified     bool                  `json:"liveParentVerified"`
}
type RFC2317MergeValidation struct {
	ParentSnapshot            Validation `json:"parentSnapshot"`
	ParentCandidate           Validation `json:"parentCandidate"`
	Child                     Validation `json:"child"`
	ParentSnapshotVerified    bool       `json:"parentSnapshotVerified"`
	NativeRecordDeltaVerified bool       `json:"nativeRecordDeltaVerified"`
	Approved                  bool       `json:"approved"`
	Activated                 bool       `json:"activated"`
	LiveParentVerified        bool       `json:"liveParentVerified"`
}

// PrepareRFC2317Merge reads no files or network state. It binds the complete
// supplied snapshot bytes to the reviewed digest and declared owner facts.
// Only the SOA serial token is replaced; all other original bytes are retained.
func PrepareRFC2317Merge(review RFC2317Bundle, parent []byte, newSerial uint32) (RFC2317MergedBundle, error) {
	var result RFC2317MergedBundle
	if err := ValidateRFC2317Bundle(review); err != nil {
		return result, err
	}
	if len(parent) == 0 || len(parent) > MaxRFC2317ParentBytes || rfc2317Hash(parent) != review.Input.Parent.SnapshotSHA256 {
		return result, errors.New("complete parent snapshot bytes do not match the reviewed SHA256/bound")
	}
	model, err := parseRFC2317Parent(string(parent), review.Input.Parent.Zone, MaxRFC2317ParentBytes)
	if err != nil {
		return result, err
	}
	additions, err := checkParentSnapshot(review.Input, model)
	if err != nil {
		return result, err
	}
	order, err := CompareSerial(newSerial, model.serial)
	if newSerial == 0 || err != nil || order <= 0 {
		return result, errors.New("new parent SOA serial must independently advance the observed baseline under RFC1982")
	}
	reviewBytes, err := EncodeRFC2317(review)
	if err != nil {
		return result, err
	}
	// Freeze source slices and pointer fields in the returned independent review.
	copy, err := DecodeRFC2317(reviewBytes)
	if err != nil {
		return result, err
	}
	newline := "\n"
	if bytes.Contains(parent, []byte("\r\n")) {
		newline = "\r\n"
	}
	merged := string(parent[:model.serialStart]) + strconv.FormatUint(uint64(newSerial), 10) + string(parent[model.serialEnd:]) + parentAdditionsText(additions, newline)
	if len(merged) > MaxRFC2317ParentBytes {
		return result, errors.New("merged parent exceeds 1 MiB bound")
	}
	result = RFC2317MergedBundle{Version: 1, Kind: RFC2317MergeKind, Review: copy, ReviewSHA256: rfc2317Hash(reviewBytes), ParentSnapshot: string(parent), ParentSnapshotSHA256: rfc2317Hash(parent), PreviousParentSerial: model.serial, ParentSerial: newSerial, ChildSerial: review.Input.DNS.Serial, AddedRecords: len(additions), ParentSnapshotVerified: true, Files: []RFC2317CompleteFile{
		{Path: "child.zone", Zone: review.Input.ChildZone, Serial: review.Input.DNS.Serial, Complete: true, Content: review.Files[0].Content},
		{Path: "parent.zone", Zone: review.Input.Parent.Zone, Serial: newSerial, Complete: true, Content: merged},
	}}
	for i := range result.Files {
		result.Files[i].SHA256 = rfc2317Hash([]byte(result.Files[i].Content))
	}
	return result, nil
}
func CheckRFC2317MergedBundle(bundle RFC2317MergedBundle) error {
	expected, err := PrepareRFC2317Merge(bundle.Review, []byte(bundle.ParentSnapshot), bundle.ParentSerial)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(bundle, expected) {
		return errors.New("merged RFC2317 bundle changed snapshot, serial, records, files or authority flags")
	}
	return nil
}
func EncodeRFC2317Merged(bundle RFC2317MergedBundle) ([]byte, error) {
	if err := CheckRFC2317MergedBundle(bundle); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil || len(raw)+1 > MaxRFC2317MergeBytes {
		return nil, errors.New("merged RFC2317 bundle exceeds bound")
	}
	return append(raw, '\n'), nil
}
func DecodeRFC2317Merged(raw []byte) (RFC2317MergedBundle, error) {
	var bundle RFC2317MergedBundle
	if err := decodeRFC2317JSONLimit(raw, &bundle, MaxRFC2317MergeBytes); err != nil {
		return bundle, err
	}
	canonical, err := EncodeRFC2317Merged(bundle)
	if err != nil {
		return bundle, err
	}
	if !bytes.Equal(raw, canonical) {
		return bundle, errors.New("merged RFC2317 bundle must retain canonical reviewed bytes")
	}
	return bundle, nil
}

// RFC2317ParentChanges shows the additive records without exposing the original
// parent file or its private provenance comments. The caller has already bound
// the decoded bundle to its immutable revision.
func RFC2317ParentChanges(bundle RFC2317MergedBundle) (string, error) {
	input := bundle.Review.Input
	model, err := parseRFC2317Parent(bundle.ParentSnapshot, input.Parent.Zone, MaxRFC2317ParentBytes)
	if err != nil {
		return "", err
	}
	additions, err := checkParentSnapshot(input, model)
	if err != nil || len(additions) != bundle.AddedRecords {
		return "", errors.New("reviewed parent additions do not match the source")
	}
	return fmt.Sprintf("; Parent SOA %d -> %d\n", bundle.PreviousParentSerial, bundle.ParentSerial) + parentAdditionsText(additions, "\n"), nil
}

func nativeRFC2317ZoneDump(ctx context.Context, binary, zone, file, dump string) (parentZone, error) {
	if _, err := command(ctx, binary, "-i", "local", "-D", "-s", "full", "-o", dump, zone, file); err != nil {
		return parentZone{}, errors.New("named-checkzone unavailable or rejected complete zone")
	}
	f, err := os.Open(dump)
	if err != nil {
		return parentZone{}, errors.New("native canonical dump unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxRFC2317DumpBytes+1))
	if err != nil || len(raw) > maxRFC2317DumpBytes {
		return parentZone{}, errors.New("native canonical dump exceeds bound")
	}
	return parseRFC2317Parent(string(raw), zone, maxRFC2317DumpBytes)
}

// Native dumps independently verify the actual parent RRsets/serial and that
// the candidate has no semantic changes except the declared serial and additions.
// -i local performs only local integrity checks; no journal/include is loaded.
func ValidateRFC2317Merge(ctx context.Context, bundle RFC2317MergedBundle, options ValidatorOptions) (RFC2317MergeValidation, error) {
	var validation RFC2317MergeValidation
	if err := CheckRFC2317MergedBundle(bundle); err != nil {
		return validation, err
	}
	binary := options.NamedCheckzone
	if binary == "" {
		binary = "named-checkzone"
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "moedove-rfc2317-merge-")
	if err != nil {
		return validation, err
	}
	defer os.RemoveAll(dir)
	for _, item := range []struct{ name, content string }{{"source.zone", bundle.ParentSnapshot}, {"parent.zone", bundle.Files[1].Content}, {"child.zone", bundle.Files[0].Content}} {
		if err := os.WriteFile(filepath.Join(dir, item.name), []byte(item.content), 0600); err != nil {
			return validation, err
		}
	}
	before, err := nativeRFC2317ZoneDump(ctx, binary, bundle.Review.Input.Parent.Zone, filepath.Join(dir, "source.zone"), filepath.Join(dir, "source.dump"))
	if err != nil {
		return validation, err
	}
	additions, err := checkParentSnapshot(bundle.Review.Input, before)
	if err != nil {
		return validation, fmt.Errorf("native parent ownership differs: %w", err)
	}
	after, err := nativeRFC2317ZoneDump(ctx, binary, bundle.Review.Input.Parent.Zone, filepath.Join(dir, "parent.zone"), filepath.Join(dir, "parent.dump"))
	if err != nil {
		return validation, err
	}
	if before.serial != bundle.PreviousParentSerial || after.serial != bundle.ParentSerial || len(additions) != bundle.AddedRecords {
		return validation, errors.New("native parent SOA/record count differs from reviewed merge")
	}
	expected, actual := map[string]int{}, map[string]int{}
	for _, rr := range before.records {
		expected[parentRRKey(rr, &bundle.ParentSerial)]++
	}
	for _, rr := range additions {
		expected[parentRRKey(rr, nil)]++
	}
	for _, rr := range after.records {
		actual[parentRRKey(rr, nil)]++
	}
	if !reflect.DeepEqual(expected, actual) {
		return validation, errors.New("native merged RRsets do not preserve the parent with only reviewed additions and SOA advancement")
	}
	if _, err := command(ctx, binary, "-i", "local", bundle.Review.Input.ChildZone, filepath.Join(dir, "child.zone")); err != nil {
		return validation, errors.New("named-checkzone unavailable or rejected complete child")
	}
	return RFC2317MergeValidation{ParentSnapshot: Validation{"native", "Exact local snapshot digest, apex SOA and complete native RRsets matched reviewed owner claims"}, ParentCandidate: Validation{"native", "Complete merged parent parsed; native RRset delta is only declared additions and independent SOA advance"}, Child: Validation{"native", "Existing reviewed complete child parsed without changing its serial or bytes"}, ParentSnapshotVerified: true, NativeRecordDeltaVerified: true}, nil
}
