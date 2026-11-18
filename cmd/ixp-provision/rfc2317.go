package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"

	"github.com/moedove/ixp-manager/internal/provision"
)

func runRFC2317Render(args []string, input io.Reader, output io.Writer) error {
	fs := flag.NewFlagSet("rfc2317-render", flag.ContinueOnError)
	fs.SetOutput(output)
	path := fs.String("input", "", "typed RFC2317 review input file, or - for stdin")
	destination := fs.String("output", "", "new private review bundle file; never installs DNS")
	done, err := parse(fs, args)
	if err != nil || done {
		return err
	}
	if *path == "" || *destination == "" {
		return errors.New("--input and --output are required")
	}
	var raw []byte
	if *path == "-" {
		raw, err = io.ReadAll(io.LimitReader(input, provision.MaxRFC2317Bytes+1))
	} else {
		raw, err = readRegular(*path, provision.MaxRFC2317Bytes, false)
	}
	if err != nil || len(raw) > provision.MaxRFC2317Bytes {
		return errors.New("RFC2317 input must be a bounded regular JSON file")
	}
	in, err := provision.DecodeRFC2317Input(raw)
	if err != nil {
		return err
	}
	bundle, err := provision.RenderRFC2317(in)
	if err != nil {
		return err
	}
	raw, err = provision.EncodeRFC2317(bundle)
	if err != nil {
		return err
	}
	if err = writeNew(*destination, raw); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]any{"kind": bundle.Kind, "file": *destination, "sha256": checksum(string(raw)), "parentZone": in.Parent.Zone, "childZone": in.ChildZone, "childComplete": true, "parentComplete": false, "validated": false, "approved": false, "activated": false, "parentIntegrated": false, "parentSnapshotVerified": false})
}

func runRFC2317Artifact(args []string, output io.Writer, extract bool) error {
	name := "rfc2317-check"
	if extract {
		name = "rfc2317-extract"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(output)
	path := fs.String("file", "", "exact canonical RFC2317 bundle")
	want := fs.String("checksum", "", "reviewed bundle SHA256")
	parent := fs.String("parent-zone", "", "explicit expected /24 parent zone")
	child := fs.String("child-zone", "", "explicit expected first-last child zone")
	var destination, binary *string
	if extract {
		destination = fs.String("output", "", "new private review directory; not a service installation")
	} else {
		binary = fs.String("named-checkzone", "named-checkzone", "local BIND offline checker")
	}
	done, err := parse(fs, args)
	if err != nil || done {
		return err
	}
	if *path == "" || !validChecksum(*want) || *parent == "" || *child == "" || extract && *destination == "" {
		return errors.New("--file, reviewed --checksum, --parent-zone and --child-zone are required; extraction also needs a new --output directory")
	}
	raw, err := readRegular(*path, provision.MaxRFC2317Bytes, false)
	if err != nil || checksum(string(raw)) != *want {
		return errors.New("RFC2317 bundle bytes do not match the reviewed checksum")
	}
	bundle, err := provision.DecodeRFC2317(raw)
	if err != nil {
		return err
	}
	p, pe := provision.SafeFQDN(*parent)
	c, ce := provision.SafeFQDN(*child)
	if pe != nil || ce != nil || p != bundle.Input.Parent.Zone || c != bundle.Input.ChildZone {
		return errors.New("RFC2317 bundle does not match the explicitly expected parent and child zones")
	}
	if extract {
		if err = writeRFC2317Directory(*destination, bundle, raw); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(map[string]any{"directory": *destination, "sha256": *want, "childZone": c, "parentZone": p, "parentComplete": false, "approved": false, "activated": false, "parentIntegrated": false, "parentSnapshotVerified": false})
	}
	result, err := provision.ValidateRFC2317(context.Background(), bundle, provision.ValidatorOptions{NamedCheckzone: *binary})
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]any{"sha256": *want, "validation": result, "parentComplete": false, "approved": false, "activated": false})
}

func writeRFC2317Directory(path string, bundle provision.RFC2317Bundle, raw []byte) error {
	if err := provision.ValidateRFC2317Bundle(bundle); err != nil {
		return err
	}
	return writeRFC2317Files(path, []rfc2317ReviewFile{
		{"child.zone", []byte(bundle.Files[0].Content)},
		{"parent.fragment", []byte(bundle.Files[1].Content)},
		{"review.json", raw},
	})
}

type rfc2317ReviewFile struct {
	name string
	data []byte
}

func writeRFC2317Files(path string, files []rfc2317ReviewFile) error {
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return errors.New("review output parent directory unavailable")
	}
	defer parent.Close()
	name := filepath.Base(path)
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return errors.New("invalid review directory name")
	}
	if err = parent.Mkdir(name, 0700); err != nil {
		return errors.New("review output directory must be new; existing paths are never replaced")
	}
	dir, err := parent.OpenRoot(name)
	if err != nil {
		return errors.New("new review directory could not be opened; inspect partial output")
	}
	defer dir.Close()
	for _, file := range files {
		f, err := dir.OpenFile(file.name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errors.New("could not create private review file; inspect partial directory")
		}
		_, err = f.Write(file.data)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			return errors.New("review file persistence failed; inspect partial directory")
		}
	}
	for _, root := range []*os.Root{dir, parent} {
		f, err := root.Open(".")
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
