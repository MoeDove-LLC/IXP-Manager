package provision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type RFC2317PreflightFile struct {
	Present bool    `json:"present"`
	SHA256  string  `json:"sha256,omitempty"`
	Serial  *uint32 `json:"serial,omitempty"`
}

type RFC2317PreflightZone struct {
	Zone      string               `json:"zone"`
	Current   RFC2317PreflightFile `json:"current"`
	Candidate RFC2317PreflightFile `json:"candidate"`
}

// RFC2317TargetEvidence binds both declared target snapshots to candidate file
// hashes. The caller must separately confirm that its local path observations
// remained current until it emitted this evidence.
type RFC2317TargetEvidence struct {
	Parent                   RFC2317PreflightZone `json:"parent"`
	Child                    RFC2317PreflightZone `json:"child"`
	NativeCandidatesVerified bool                 `json:"nativeCandidatesVerified"`
}

// PreflightRFC2317 checks target file bytes and both merged candidates. The
// command layer supplies these bytes from explicit target paths without symlinks.
// ChildPresent distinguishes a missing file from an empty existing file.
func PreflightRFC2317(ctx context.Context, bundle RFC2317MergedBundle, parent, child []byte, childPresent bool, options ValidatorOptions) (RFC2317TargetEvidence, error) {
	if err := CheckRFC2317MergedBundle(bundle); err != nil {
		return RFC2317TargetEvidence{}, err
	}
	in := bundle.Review.Input
	if len(parent) == 0 || len(parent) > MaxRFC2317ParentBytes || rfc2317Hash(parent) != bundle.ParentSnapshotSHA256 {
		return RFC2317TargetEvidence{}, errors.New("target parent does not match the approved snapshot SHA256")
	}
	// The merged bundle's own snapshot and SOA are already validated. The
	// matching digest pins the current target parent to those identical bytes.
	parentSerial := bundle.PreviousParentSerial
	currentChild := RFC2317PreflightFile{Present: childPresent}
	if in.NewChildZone {
		if childPresent || len(child) != 0 {
			return RFC2317TargetEvidence{}, errors.New("target child path must be absent for the approved new child")
		}
	} else {
		if !childPresent {
			return RFC2317TargetEvidence{}, errors.New("target child file is missing for the approved existing child")
		}
		// Parse the actual input before any native validator is started. In
		// particular, $INCLUDE and $GENERATE cannot reach BIND in this path.
		current, err := parseRFC2317Parent(string(child), in.ChildZone, MaxRFC2317ParentBytes)
		if err != nil {
			return RFC2317TargetEvidence{}, errors.New("target child is outside the safe complete master-file subset or has the wrong zone identity")
		}
		if current.serial != *in.PreviousChildSerial {
			return RFC2317TargetEvidence{}, errors.New("target child SOA serial differs from the approved baseline")
		}
		currentChild.Serial = &current.serial
		currentChild.SHA256 = rfc2317Hash(child)
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if _, err := ValidateRFC2317Merge(ctx, bundle, options); err != nil {
		return RFC2317TargetEvidence{}, err
	}
	if childPresent {
		if err := checkRFC2317CurrentChild(ctx, child, in.ChildZone, *currentChild.Serial, options); err != nil {
			return RFC2317TargetEvidence{}, err
		}
	}
	return RFC2317TargetEvidence{
		Parent: RFC2317PreflightZone{
			Zone:      in.Parent.Zone,
			Current:   RFC2317PreflightFile{Present: true, SHA256: bundle.ParentSnapshotSHA256, Serial: &parentSerial},
			Candidate: RFC2317PreflightFile{Present: true, SHA256: bundle.Files[1].SHA256, Serial: &bundle.ParentSerial},
		},
		Child: RFC2317PreflightZone{
			Zone:      in.ChildZone,
			Current:   currentChild,
			Candidate: RFC2317PreflightFile{Present: true, SHA256: bundle.Files[0].SHA256, Serial: &bundle.ChildSerial},
		},
		NativeCandidatesVerified: true,
	}, nil
}

func checkRFC2317CurrentChild(ctx context.Context, raw []byte, zone string, serial uint32, options ValidatorOptions) error {
	binary := options.NamedCheckzone
	if binary == "" {
		binary = "named-checkzone"
	}
	dir, err := os.MkdirTemp("", "moedove-rfc2317-target-")
	if err != nil {
		return errors.New("cannot open private scratch space for target child verification")
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "current.zone")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		return errors.New("cannot copy exact target child to private scratch space")
	}
	current, err := nativeRFC2317ZoneDump(ctx, binary, zone, file, filepath.Join(dir, "current.dump"))
	if err != nil || current.serial != serial {
		return errors.New("named-checkzone unavailable or native target child SOA differs from the approved baseline")
	}
	return nil
}
