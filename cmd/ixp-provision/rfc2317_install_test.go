package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"testing"
)

type installTestAuthority struct {
	paths       map[string]string
	serials     map[string]*uint32
	candidates  map[string]uint32
	reloads     []string
	loseReplyOn string
	failLoadOn  string
	before      func(string)
}

func (a *installTestAuthority) Status(_ context.Context, zone, path string) (rfc2317ZoneStatus, error) {
	if a.paths[zone] != path {
		return rfc2317ZoneStatus{}, errors.New("wrong zone binding")
	}
	return rfc2317ZoneStatus{Zone: zone, File: path, Serial: a.serials[zone]}, nil
}

func (a *installTestAuthority) SOA(_ context.Context, zone string) (uint32, error) {
	if a.serials[zone] == nil {
		return 0, errors.New("zone unloaded")
	}
	return *a.serials[zone], nil
}

func (a *installTestAuthority) Reload(_ context.Context, zone, path string) error {
	if a.paths[zone] != path {
		return errors.New("wrong zone binding")
	}
	if a.before != nil {
		a.before(zone)
	}
	a.reloads = append(a.reloads, zone)
	if a.failLoadOn == zone {
		return errors.New("BIND rejected the reload before applying it")
	}
	serial := a.candidates[zone]
	a.serials[zone] = &serial
	if a.loseReplyOn == zone {
		return errors.New("lost reload reply after server applied the serial")
	}
	return nil
}

func TestRFC2317InstallFinishesInterruptedPairInOrder(t *testing.T) {
	checker := os.Getenv("NAMED_CHECKZONE_BIN")
	if checker == "" {
		t.Skip("NAMED_CHECKZONE_BIN is required for exact native validation")
	}
	for _, lost := range []string{"child-reply", "parent-reply", "parent-rejected"} {
		t.Run(lost, func(t *testing.T) {
			// Exercise both a predeclared unloaded new child and an existing
			// child whose valid old serial is zero.
			f := newRFC2317TargetFixture(t, lost != "child-reply")
			parent, child := f.bundle.Files[1].Zone, f.bundle.Files[0].Zone
			binding := rfc2317InstallBinding{ParentFile: f.parent, ChildFile: f.child, NamedCheckzone: checker}
			a := &installTestAuthority{
				paths:      map[string]string{parent: f.parent, child: f.child},
				serials:    map[string]*uint32{parent: &f.bundle.PreviousParentSerial, child: f.bundle.Review.Input.PreviousChildSerial},
				candidates: map[string]uint32{parent: f.bundle.ParentSerial, child: f.bundle.ChildSerial},
				before: func(zone string) {
					if zone == child {
						old, err := os.ReadFile(f.parent)
						if err != nil || !bytes.Equal(old, f.parentBytes) {
							t.Fatal("parent advanced before the child was loaded", err)
						}
					}
				},
			}
			switch lost {
			case "child-reply":
				a.loseReplyOn = child
			case "parent-reply":
				a.loseReplyOn = parent
			case "parent-rejected":
				a.failLoadOn = parent
			}
			ctx := context.Background()
			apply := func(resume bool, digest string) (rfc2317InstallState, error) {
				t.Helper()
				files, err := lockRFC2317Files(f.parent, f.child)
				if err != nil {
					t.Fatal(err)
				}
				defer files.Close()
				return installRFC2317(ctx, f.bundle, digest, binding, files, a, resume)
			}
			digest := checksum(string(f.data))
			first, err := apply(false, digest)
			if err == nil || first.Phase == rfc2317Installed {
				t.Fatal("lost response was mistaken for completion")
			}
			if lost == "child-reply" && !slices.Equal(a.reloads, []string{child}) {
				t.Fatal("parent was touched despite the interrupted child load", a.reloads)
			}
			if _, err = apply(false, digest); err == nil {
				t.Fatal("unresolved journal was silently overwritten")
			}
			if _, err = apply(true, checksum("different approved artifact")); err == nil {
				t.Fatal("resume accepted a different release digest")
			}
			parentOnDisk, err := os.ReadFile(f.parent)
			if err != nil {
				t.Fatal(err)
			}
			writeRFC2317TestFile(t, f.parent, append(bytes.Clone(parentOnDisk), []byte("; unexpected external change\n")...))
			if _, err = apply(true, digest); err == nil {
				t.Fatal("resume ignored external parent drift")
			}
			writeRFC2317TestFile(t, f.parent, parentOnDisk)
			a.loseReplyOn, a.failLoadOn = "", ""
			if got, err := apply(true, digest); err != nil || got.Phase != rfc2317Installed {
				t.Fatal("exact forward resume failed", got.Phase, err)
			}
			wantedReloads := []string{child, parent}
			if lost == "parent-rejected" {
				wantedReloads = append(wantedReloads, parent)
			}
			if got, err := apply(true, digest); err != nil || got.Phase != rfc2317Installed || !slices.Equal(a.reloads, wantedReloads) {
				t.Fatal("verified reload was repeated", got.Phase, a.reloads, err)
			}
			for i, path := range []string{f.child, f.parent} {
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != f.bundle.Files[i].Content {
					t.Fatal("final files do not match the approved paired bytes", err)
				}
			}
		})
	}
}
