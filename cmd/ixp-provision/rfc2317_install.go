package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/moedove/ixp-manager/internal/provision"
)

const (
	rfc2317ChildPending  = "child-pending"
	rfc2317ParentPending = "parent-pending"
	rfc2317Installed     = "installed"
)

type rfc2317InstallDirectory struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type rfc2317InstallBinding struct {
	ParentFile      string                  `json:"parentFile"`
	ChildFile       string                  `json:"childFile"`
	ParentDirectory rfc2317InstallDirectory `json:"parentDirectory"`
	ChildDirectory  rfc2317InstallDirectory `json:"childDirectory"`
	RNDCBinary      string                  `json:"rndcBinary"`
	ControlKey      string                  `json:"controlKey"`
	ControlEndpoint string                  `json:"controlEndpoint"`
	DNSEndpoint     string                  `json:"dnsEndpoint"`
	NamedCheckzone  string                  `json:"namedCheckzone"`
}

type rfc2317InstallState struct {
	Version      int                             `json:"version"`
	BundleSHA256 string                          `json:"bundleSHA256"`
	Binding      rfc2317InstallBinding           `json:"binding"`
	Evidence     provision.RFC2317TargetEvidence `json:"evidence"`
	Phase        string                          `json:"phase"`
}

type rfc2317InstallAuthority interface {
	Status(context.Context, string, string) (rfc2317ZoneStatus, error)
	Reload(context.Context, string, string) error
	SOA(context.Context, string) (uint32, error)
}

func runRFC2317Install(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("rfc2317-install", flag.ContinueOnError)
	fs.SetOutput(output)
	f := addRFC2317ArtifactFlags(fs)
	parent := fs.String("parent-file", "", "absolute target static parent master file")
	child := fs.String("child-file", "", "absolute target static child master file")
	rndc := fs.String("rndc", "/usr/sbin/rndc", "absolute local rndc binary")
	key := fs.String("rndc-key", "", "absolute private BIND control key file")
	control := fs.String("control-endpoint", "", "numeric loopback BIND control IP:port")
	dns := fs.String("dns-endpoint", "", "numeric loopback authoritative DNS TCP IP:port")
	exclusive := fs.Bool("exclusive-maintenance", false, "confirm this transaction exclusively controls both static zone files and BIND zone loads")
	resume := fs.Bool("resume", false, "continue the existing interrupted install with this same approved pair and target binding")
	done, err := parse(fs, args)
	if err != nil || done {
		return err
	}
	if !*exclusive || *f.path == "" || !validChecksum(*f.checksum) || *f.parent == "" || *f.child == "" || *parent == "" || *child == "" ||
		*key == "" || *control == "" || *dns == "" || *rndc == "" {
		return errors.New("approved pair, both explicit zones and files, local BIND endpoints and key, and --exclusive-maintenance are required")
	}
	bundlePath, err := filepath.Abs(*f.path)
	if err != nil || filepath.Clean(*f.path) != *f.path || bundlePath == *parent || bundlePath == *child || *parent == *child ||
		*key == *parent || *key == *child || bundlePath == *key {
		return errors.New("approved bundle, control key and both zone files must be distinct clean paths")
	}
	release, err := readRFC2317LocalFile(bundlePath, provision.MaxRFC2317MergeBytes, false)
	if err != nil || !release.present || checksum(string(release.bytes)) != *f.checksum {
		return errors.New("RFC 2317 bundle is unavailable or does not match the approved checksum")
	}
	bundle, err := provision.DecodeRFC2317Merged(release.bytes)
	if err != nil {
		return err
	}
	if err = f.verifyZones(bundle.Review.Input); err != nil {
		return err
	}
	authority, err := openRFC2317BIND(rfc2317BINDOptions{RNDCBinary: *rndc, ControlKey: *key, ControlEndpoint: *control, DNSEndpoint: *dns})
	if err != nil {
		return err
	}
	defer authority.Close()
	files, err := lockRFC2317Files(*parent, *child)
	if err != nil {
		return err
	}
	defer files.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	binding := rfc2317InstallBinding{ParentFile: *parent, ChildFile: *child, RNDCBinary: *rndc, ControlKey: *key, ControlEndpoint: *control, DNSEndpoint: *dns, NamedCheckzone: *f.binary}
	state, err := installRFC2317(ctx, bundle, *f.checksum, binding, files, authority, *resume)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Kind                string `json:"kind"`
		BundleSHA256        string `json:"bundleSHA256"`
		Activated           bool   `json:"activated"`
		TargetFilesVerified bool   `json:"targetFilesVerified"`
		provision.RFC2317TargetEvidence
	}{Kind: "dns-rfc2317-install", BundleSHA256: state.BundleSHA256, Activated: true, TargetFilesVerified: true, RFC2317TargetEvidence: state.Evidence})
}

func readRFC2317InstallState(file rfc2317LocalFile) (rfc2317InstallState, error) {
	var state rfc2317InstallState
	d := json.NewDecoder(bytes.NewReader(file.bytes))
	d.DisallowUnknownFields()
	if d.Decode(&state) != nil || d.Decode(new(any)) != io.EOF || state.Version != 1 || !validChecksum(state.BundleSHA256) ||
		(state.Phase != rfc2317ChildPending && state.Phase != rfc2317ParentPending && state.Phase != rfc2317Installed) {
		return state, errors.New("invalid RFC 2317 install journal")
	}
	raw, err := json.Marshal(state)
	if err != nil || !bytes.Equal(append(raw, '\n'), file.bytes) {
		return state, errors.New("RFC 2317 install journal is no longer canonical")
	}
	return state, nil
}

func rfc2317SameSerial(first, second *uint32) bool {
	return first == nil && second == nil || first != nil && second != nil && *first == *second
}

func rfc2317FileMatches(file rfc2317LocalFile, want provision.RFC2317PreflightFile) bool {
	return file.present == want.Present && (!file.present || checksum(string(file.bytes)) == want.SHA256)
}

func validateRFC2317InstallEvidence(state rfc2317InstallState, bundle provision.RFC2317MergedBundle) bool {
	p, c := state.Evidence.Parent, state.Evidence.Child
	return state.Evidence.NativeCandidatesVerified && p.Zone == bundle.Files[1].Zone && c.Zone == bundle.Files[0].Zone &&
		p.Current.Present && p.Current.SHA256 == bundle.ParentSnapshotSHA256 && rfc2317SameSerial(p.Current.Serial, &bundle.PreviousParentSerial) &&
		c.Current.Present == !bundle.Review.Input.NewChildZone && rfc2317SameSerial(c.Current.Serial, bundle.Review.Input.PreviousChildSerial) &&
		(c.Current.Present && validChecksum(c.Current.SHA256) || !c.Current.Present && c.Current.SHA256 == "") &&
		p.Candidate.Present && p.Candidate.SHA256 == bundle.Files[1].SHA256 && rfc2317SameSerial(p.Candidate.Serial, &bundle.ParentSerial) &&
		c.Candidate.Present && c.Candidate.SHA256 == bundle.Files[0].SHA256 && rfc2317SameSerial(c.Candidate.Serial, &bundle.ChildSerial)
}

func writeRFC2317InstallState(files *rfc2317InstallFiles, before rfc2317LocalFile, state rfc2317InstallState) (rfc2317LocalFile, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return before, err
	}
	raw = append(raw, '\n')
	if err = files.WriteState(before, raw); err != nil {
		return before, fmt.Errorf("install journal write failed; inspect journal before retrying: %w", err)
	}
	after, err := files.ReadState()
	if err != nil || !after.present || !bytes.Equal(after.bytes, raw) {
		return before, errors.New("install journal bytes could not be verified")
	}
	return after, nil
}

func installRFC2317(ctx context.Context, bundle provision.RFC2317MergedBundle, bundleSHA256 string, binding rfc2317InstallBinding, files *rfc2317InstallFiles, authority rfc2317InstallAuthority, resume bool) (rfc2317InstallState, error) {
	journal, err := files.ReadState()
	if err != nil {
		return rfc2317InstallState{}, err
	}
	parent, err := files.Read(binding.ParentFile)
	if err != nil {
		return rfc2317InstallState{}, err
	}
	child, err := files.Read(binding.ChildFile)
	if err != nil {
		return rfc2317InstallState{}, err
	}
	binding.ParentDirectory = rfc2317InstallDirectory{uint64(parent.parent.Dev), uint64(parent.parent.Ino)}
	binding.ChildDirectory = rfc2317InstallDirectory{uint64(child.parent.Dev), uint64(child.parent.Ino)}
	var state rfc2317InstallState
	if journal.present {
		state, err = readRFC2317InstallState(journal)
		if err != nil {
			return state, err
		}
		if state.Binding.ParentFile != binding.ParentFile || state.Binding.ParentDirectory != binding.ParentDirectory ||
			(resume || state.Phase != rfc2317Installed) && state.Binding != binding {
			return state, errors.New("target binding differs from the parent install journal")
		}
	}
	validator := provision.ValidatorOptions{NamedCheckzone: binding.NamedCheckzone}
	if resume {
		if !journal.present || state.BundleSHA256 != bundleSHA256 || !validateRFC2317InstallEvidence(state, bundle) {
			return state, errors.New("resume requires the exact approved bundle and its verified install journal")
		}
		validation, err := provision.ValidateRFC2317Merge(ctx, bundle, validator)
		if err != nil || !validation.NativeRecordDeltaVerified || !validation.ParentSnapshotVerified {
			return state, errors.New("native validation of the recovery candidates failed")
		}
	} else {
		if journal.present && (state.Phase != rfc2317Installed || state.BundleSHA256 == bundleSHA256) {
			return state, errors.New("an install journal already owns this pair; use --resume with the same approved bundle")
		}
		evidence, err := provision.PreflightRFC2317(ctx, bundle, parent.bytes, child.bytes, child.present, validator)
		if err != nil {
			return state, err
		}
		if err = proveRFC2317Baseline(ctx, files, authority, parent, child, binding, evidence); err != nil {
			return state, err
		}
		state = rfc2317InstallState{Version: 1, BundleSHA256: bundleSHA256, Binding: binding, Evidence: evidence, Phase: rfc2317ChildPending}
		journal, err = writeRFC2317InstallState(files, journal, state)
		if err != nil {
			return state, err
		}
	}
	if state.Phase == rfc2317ChildPending {
		err = proveRFC2317FileAndAuthority(ctx, files, authority, binding.ParentFile, state.Evidence.Parent.Zone, state.Evidence.Parent.Current)
		if err == nil {
			err = publishRFC2317Zone(ctx, files, authority, binding.ChildFile, bundle.Files[0], state.Evidence.Child)
		}
		if err != nil {
			return state, fmt.Errorf("child phase incomplete; resume the same approved pair: %w", err)
		}
		state.Phase = rfc2317ParentPending
		journal, err = writeRFC2317InstallState(files, journal, state)
		if err != nil {
			return state, err
		}
	}
	if err = proveRFC2317FileAndAuthority(ctx, files, authority, binding.ChildFile, state.Evidence.Child.Zone, state.Evidence.Child.Candidate); err != nil {
		return state, fmt.Errorf("parent phase requires the already loaded child: %w", err)
	}
	if state.Phase == rfc2317ParentPending {
		if err = publishRFC2317Zone(ctx, files, authority, binding.ParentFile, bundle.Files[1], state.Evidence.Parent); err != nil {
			return state, fmt.Errorf("parent phase incomplete; resume the same approved pair: %w", err)
		}
		if err = proveRFC2317FileAndAuthority(ctx, files, authority, binding.ChildFile, state.Evidence.Child.Zone, state.Evidence.Child.Candidate); err != nil {
			return state, fmt.Errorf("pair verification lost child evidence after parent load: %w", err)
		}
		state.Phase = rfc2317Installed
		if _, err = writeRFC2317InstallState(files, journal, state); err != nil {
			return state, err
		}
	}
	if err = proveRFC2317FileAndAuthority(ctx, files, authority, binding.ParentFile, state.Evidence.Parent.Zone, state.Evidence.Parent.Candidate); err != nil {
		return state, fmt.Errorf("installed parent evidence changed: %w", err)
	}
	return state, nil
}

func proveRFC2317Baseline(ctx context.Context, files *rfc2317InstallFiles, authority rfc2317InstallAuthority, parent, child rfc2317LocalFile, binding rfc2317InstallBinding, evidence provision.RFC2317TargetEvidence) error {
	for _, item := range []struct {
		path string
		old  rfc2317LocalFile
		zone provision.RFC2317PreflightZone
	}{{binding.ParentFile, parent, evidence.Parent}, {binding.ChildFile, child, evidence.Child}} {
		current, err := files.Read(item.path)
		if err != nil || !item.old.matches(current) {
			return errors.New("zone file changed during native preflight")
		}
		if err := proveRFC2317Authority(ctx, authority, item.zone.Zone, item.path, item.zone.Current.Serial); err != nil {
			return err
		}
	}
	return nil
}

func proveRFC2317Authority(ctx context.Context, authority rfc2317InstallAuthority, zone, path string, serial *uint32) error {
	status, err := authority.Status(ctx, zone, path)
	if err != nil || status.Zone != zone || status.File != path || !rfc2317SameSerial(status.Serial, serial) {
		return fmt.Errorf("BIND state for %s does not match the expected static zone and serial", zone)
	}
	if serial != nil {
		actual, err := authority.SOA(ctx, zone)
		if err != nil || actual != *serial {
			return fmt.Errorf("authoritative TCP SOA for %s does not match the expected serial", zone)
		}
	}
	return nil
}

func proveRFC2317FileAndAuthority(ctx context.Context, files *rfc2317InstallFiles, authority rfc2317InstallAuthority, path, zone string, want provision.RFC2317PreflightFile) error {
	before, err := files.Read(path)
	if err != nil || !rfc2317FileMatches(before, want) {
		return fmt.Errorf("master file for %s differs from the expected release", zone)
	}
	if err = proveRFC2317Authority(ctx, authority, zone, path, want.Serial); err != nil {
		return err
	}
	if after, err := files.Read(path); err != nil || !before.matches(after) {
		return fmt.Errorf("master file for %s changed during BIND observation", zone)
	}
	return nil
}

func publishRFC2317Zone(ctx context.Context, files *rfc2317InstallFiles, authority rfc2317InstallAuthority, path string, candidate provision.RFC2317CompleteFile, zone provision.RFC2317PreflightZone) error {
	current, err := files.Read(path)
	if err != nil {
		return err
	}
	if rfc2317FileMatches(current, zone.Candidate) {
		if proveRFC2317FileAndAuthority(ctx, files, authority, path, zone.Zone, zone.Candidate) == nil {
			return nil // The exact candidate is already loaded after an interruption.
		}
	} else if !rfc2317FileMatches(current, zone.Current) {
		return fmt.Errorf("master file for %s matches neither its verified baseline nor candidate", zone.Zone)
	}
	if err := proveRFC2317Authority(ctx, authority, zone.Zone, path, zone.Current.Serial); err != nil {
		return err
	}
	if !rfc2317FileMatches(current, zone.Candidate) {
		if err := files.Install(path, current, []byte(candidate.Content)); err != nil {
			return err
		}
	}
	installed, err := files.Read(path)
	if err != nil || !rfc2317FileMatches(installed, zone.Candidate) {
		return fmt.Errorf("master file for %s changed before reload", zone.Zone)
	}
	if err := authority.Reload(ctx, zone.Zone, path); err != nil {
		return fmt.Errorf("BIND reload for %s was not confirmed: %w", zone.Zone, err)
	}
	verification, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	for {
		if err := proveRFC2317FileAndAuthority(verification, files, authority, path, zone.Zone, zone.Candidate); err == nil {
			return nil
		}
		select {
		case <-verification.Done():
			return fmt.Errorf("candidate for %s did not pass BIND and TCP SOA readback", zone.Zone)
		case <-time.After(250 * time.Millisecond):
		}
	}
}
