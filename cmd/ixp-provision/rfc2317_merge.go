package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strconv"

	"github.com/moedove/ixp-manager/internal/provision"
)

type rfc2317ArtifactFlags struct {
	path, checksum, parent, child, binary *string
}

func addRFC2317ArtifactFlags(fs *flag.FlagSet) rfc2317ArtifactFlags {
	return rfc2317ArtifactFlags{
		path:     fs.String("file", "", "exact reviewed RFC2317 bundle"),
		checksum: fs.String("checksum", "", "reviewed bundle SHA256"),
		parent:   fs.String("parent-zone", "", "explicit expected /24 parent zone"),
		child:    fs.String("child-zone", "", "explicit expected classless child zone"),
		binary:   fs.String("named-checkzone", "named-checkzone", "local BIND offline checker"),
	}
}

func (f rfc2317ArtifactFlags) read(limit int64) ([]byte, error) {
	if *f.path == "" || !validChecksum(*f.checksum) || *f.parent == "" || *f.child == "" {
		return nil, errors.New("--file, reviewed --checksum, --parent-zone and --child-zone are required")
	}
	raw, err := readRegular(*f.path, limit, false)
	if err != nil || checksum(string(raw)) != *f.checksum {
		return nil, errors.New("RFC2317 bundle bytes do not match the reviewed checksum")
	}
	return raw, nil
}

func (f rfc2317ArtifactFlags) verifyZones(in provision.RFC2317Input) error {
	parent, pe := provision.SafeFQDN(*f.parent)
	child, ce := provision.SafeFQDN(*f.child)
	if pe != nil || ce != nil || parent != in.Parent.Zone || child != in.ChildZone {
		return errors.New("RFC2317 bundle does not match the explicitly expected parent and child zones")
	}
	return nil
}

func runRFC2317Merge(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("rfc2317-merge", flag.ContinueOnError)
	fs.SetOutput(output)
	f := addRFC2317ArtifactFlags(fs)
	parentPath := fs.String("parent-snapshot", "", "complete local parent master file matching the reviewed SHA256 and serial")
	serialText := fs.String("parent-serial", "", "independently advancing new parent SOA serial as a decimal integer")
	dest := fs.String("output", "", "new private merged review bundle file")
	done, err := parse(fs, args)
	if err != nil || done {
		return err
	}
	serial, err := strconv.ParseUint(*serialText, 10, 32)
	if *parentPath == "" || *dest == "" || err != nil || serial == 0 {
		return errors.New("--parent-snapshot, decimal --parent-serial and new --output file are required")
	}
	raw, err := f.read(provision.MaxRFC2317Bytes)
	if err != nil {
		return err
	}
	review, err := provision.DecodeRFC2317(raw)
	if err != nil {
		return err
	}
	if err = f.verifyZones(review.Input); err != nil {
		return err
	}
	parent, err := readRegular(*parentPath, provision.MaxRFC2317ParentBytes, false)
	if err != nil {
		return err
	}
	merged, err := provision.PrepareRFC2317Merge(review, parent, uint32(serial))
	if err != nil {
		return err
	}
	validation, err := provision.ValidateRFC2317Merge(context.Background(), merged, provision.ValidatorOptions{NamedCheckzone: *f.binary})
	if err != nil {
		return err
	}
	raw, err = provision.EncodeRFC2317Merged(merged)
	if err != nil {
		return err
	}
	if err = writeNew(*dest, raw); err != nil {
		return err
	}
	metadata := rfc2317MergeMetadata(merged, validation, checksum(string(raw)))
	metadata["file"] = *dest
	return json.NewEncoder(output).Encode(metadata)
}

func runRFC2317MergedArtifact(args []string, output io.Writer, extract bool) error {
	name := "rfc2317-merge-check"
	if extract {
		name = "rfc2317-merge-extract"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(output)
	f := addRFC2317ArtifactFlags(fs)
	var dest *string
	if extract {
		dest = fs.String("output", "", "new private directory for both checked zone files")
	}
	done, err := parse(fs, args)
	if err != nil || done {
		return err
	}
	if extract && *dest == "" {
		return errors.New("extraction requires a new --output directory")
	}
	raw, err := f.read(provision.MaxRFC2317MergeBytes)
	if err != nil {
		return err
	}
	merged, err := provision.DecodeRFC2317Merged(raw)
	if err != nil {
		return err
	}
	if err = f.verifyZones(merged.Review.Input); err != nil {
		return err
	}
	validation, err := provision.ValidateRFC2317Merge(context.Background(), merged, provision.ValidatorOptions{NamedCheckzone: *f.binary})
	if err != nil {
		return err
	}
	metadata := rfc2317MergeMetadata(merged, validation, *f.checksum)
	if extract {
		files := []rfc2317ReviewFile{
			{"child.zone", []byte(merged.Files[0].Content)},
			{"parent.zone", []byte(merged.Files[1].Content)},
			{"merge.json", raw},
		}
		if err = writeRFC2317Files(*dest, files); err != nil {
			return err
		}
		metadata["directory"] = *dest
	}
	return json.NewEncoder(output).Encode(metadata)
}

func rfc2317MergeMetadata(bundle provision.RFC2317MergedBundle, validation provision.RFC2317MergeValidation, sum string) map[string]any {
	return map[string]any{
		"kind": bundle.Kind, "sha256": sum, "reviewSHA256": bundle.ReviewSHA256,
		"parentZone": bundle.Review.Input.Parent.Zone, "childZone": bundle.Review.Input.ChildZone,
		"parentSnapshotSHA256": bundle.ParentSnapshotSHA256, "previousParentSerial": bundle.PreviousParentSerial,
		"parentSerial": bundle.ParentSerial, "childSerial": bundle.ChildSerial, "addedRecords": bundle.AddedRecords,
		"validation": validation, "parentComplete": true, "childComplete": true, "approved": false,
		"activated": false, "liveParentVerified": false,
	}
}
