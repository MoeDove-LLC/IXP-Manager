package provision

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxRFC2317Bytes = 1 << 20
const RFC2317Kind = "dns-rfc2317-review"

type RFC2317OwnerClaim struct {
	Owner  string `json:"owner"`
	State  string `json:"state"` // explicitly absent, or matching cname; other data is a conflict
	Target string `json:"target,omitempty"`
}
type RFC2317DelegationClaim struct {
	Owner       string   `json:"owner"`
	State       string   `json:"state"` // explicitly absent, or matching ns
	NameServers []string `json:"nameServers,omitempty"`
}
type RFC2317ParentReview struct {
	Zone           string                 `json:"zone"`
	SnapshotSHA256 string                 `json:"snapshotSHA256"`
	ObservedSerial *uint32                `json:"observedSerial"`
	TTL            uint32                 `json:"ttl"`
	Delegation     RFC2317DelegationClaim `json:"delegation"`
	Owners         []RFC2317OwnerClaim    `json:"owners"`
}
type RFC2317PTR struct {
	Address string `json:"address"`
	Name    string `json:"name"`
}
type RFC2317Input struct {
	Version             int                 `json:"version"`
	Parent              RFC2317ParentReview `json:"parent"`
	ChildZone           string              `json:"childZone"`
	First               *uint16             `json:"first"`
	Last                *uint16             `json:"last"`
	NewChildZone        bool                `json:"newChildZone"`
	PreviousChildSerial *uint32             `json:"previousChildSerial"`
	DNS                 DNSOptions          `json:"dns"`
	PTRs                []RFC2317PTR        `json:"ptrs"`
}
type RFC2317File struct {
	Path     string `json:"path"`
	Zone     string `json:"zone"`
	Complete bool   `json:"complete"`
	SHA256   string `json:"sha256"`
	Content  string `json:"content"`
}
type RFC2317Bundle struct {
	Version                int           `json:"version"`
	Kind                   string        `json:"kind"`
	Input                  RFC2317Input  `json:"input"`
	InputSHA256            string        `json:"inputSHA256"`
	Files                  []RFC2317File `json:"files"`
	Approved               bool          `json:"approved"`
	Activated              bool          `json:"activated"`
	ParentIntegrated       bool          `json:"parentIntegrated"`
	ParentSnapshotVerified bool          `json:"parentSnapshotVerified"`
}
type RFC2317Validation struct {
	Child                  Validation `json:"child"`
	ParentFragment         Validation `json:"parentFragment"`
	ParentSnapshotVerified bool       `json:"parentSnapshotVerified"`
	ParentIntegrated       bool       `json:"parentIntegrated"`
	Approved               bool       `json:"approved"`
	Activated              bool       `json:"activated"`
}

func rfc2317Hash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func rfc2317Digest(v string) bool {
	b, e := hex.DecodeString(v)
	return e == nil && len(b) == 32 && strings.ToLower(v) == v
}

// Owner declarations describe the operator's reviewed parent snapshot. They are
// not an attestation that the parent was read, remains current, or was changed.
func NormalizeRFC2317(input RFC2317Input) (RFC2317Input, error) {
	in := input
	in.Parent.Owners = append([]RFC2317OwnerClaim{}, input.Parent.Owners...)
	in.Parent.Delegation.NameServers = append([]string(nil), input.Parent.Delegation.NameServers...)
	in.PTRs = append([]RFC2317PTR{}, input.PTRs...)
	if in.Version != 1 || in.First == nil || in.Last == nil || in.Parent.ObservedSerial == nil || !rfc2317Digest(in.Parent.SnapshotSHA256) || in.Parent.TTL == 0 || in.Parent.TTL > 604800 || input.Parent.Owners == nil || input.PTRs == nil {
		return in, errors.New("RFC2317 requires version1, explicit range, reviewed parent checksum/serial/TTL and owner/PTR arrays")
	}
	prefix, err := netip.ParsePrefix(in.DNS.Prefix)
	if err != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() || prefix.Bits() < 25 || prefix.Bits() > 32 || !prefix.Addr().IsGlobalUnicast() {
		return in, errors.New("RFC2317 prefix must be canonical unicast IPv4 /25../32 within one /24")
	}
	base := prefix.Addr().As4()
	first := uint16(base[3])
	last := first + uint16((1<<uint(32-prefix.Bits()))-1)
	if *in.First != first || *in.Last != last || last > 255 {
		return in, errors.New("explicit RFC2317 range does not exactly match its prefix")
	}
	// Copy pointers so normalization never leaves a bundle aliased to mutable
	// caller-owned range or serial variables.
	in.First, in.Last = &first, &last
	serial := *in.Parent.ObservedSerial
	in.Parent.ObservedSerial = &serial
	parentPrefix := netip.PrefixFrom(prefix.Addr(), 24).Masked().String()
	parent, _ := ReverseZone(parentPrefix)
	in.Parent.Zone, err = SafeFQDN(in.Parent.Zone)
	if err != nil || in.Parent.Zone != parent {
		return in, errors.New("explicit parent must be the prefix's exact /24 in-addr.arpa zone")
	}
	in.ChildZone, err = SafeFQDN(in.ChildZone)
	wantChild := fmt.Sprintf("%d-%d.%s", first, last, parent)
	if err != nil || in.ChildZone != wantChild {
		return in, errors.New("reviewed child zone must use the exact first-last label directly below its parent; alternate RFC2317 naming schemes are not supported")
	}
	if in.NewChildZone {
		if in.PreviousChildSerial != nil {
			return in, errors.New("new child zone cannot also declare an existing serial")
		}
	} else {
		if in.PreviousChildSerial == nil {
			return in, errors.New("existing child requires its explicitly observed SOA serial")
		}
		previous := *in.PreviousChildSerial
		in.PreviousChildSerial = &previous
		order, err := CompareSerial(in.DNS.Serial, previous)
		if err != nil || order <= 0 {
			return in, errors.New("child serial must advance the explicit baseline within the RFC1982 comparison window")
		}
	}
	if in.DNS.NameSuffix != "" || in.DNS.ActiveOnly {
		return in, errors.New("RFC2317 PTRs must be explicitly selected and named; fallback naming and implicit active filtering are unavailable")
	}
	temporary := in.DNS
	temporary.Prefix = parentPrefix
	options, err := NormalizeOptions(Options{Kind: "dns-arpa", Families: []int{4}, DNS: temporary})
	if err != nil {
		return in, err
	}
	in.DNS = options.DNS
	in.DNS.Prefix = prefix.String()
	in.Parent.Delegation.Owner, err = SafeFQDN(in.Parent.Delegation.Owner)
	if err != nil || in.Parent.Delegation.Owner != in.ChildZone {
		return in, errors.New("reviewed delegation owner must be exactly the child zone")
	}
	switch in.Parent.Delegation.State {
	case "absent":
		if len(in.Parent.Delegation.NameServers) != 0 {
			return in, errors.New("absent delegation cannot declare existing NS records")
		}
	case "ns":
		seen := map[string]bool{}
		for i, raw := range in.Parent.Delegation.NameServers {
			name, e := SafeFQDN(raw)
			if e != nil || seen[name] {
				return in, errors.New("invalid or duplicate existing delegation nameserver")
			}
			seen[name] = true
			in.Parent.Delegation.NameServers[i] = name
		}
		sort.Strings(in.Parent.Delegation.NameServers)
		if !reflect.DeepEqual(in.Parent.Delegation.NameServers, in.DNS.NameServers) {
			return in, errors.New("existing parent delegation targets differ; do not overwrite them implicitly")
		}
	default:
		return in, errors.New("delegation must be explicitly absent or the exact matching NS set; conflicting data requires separate review")
	}
	if len(in.Parent.Owners) != int(last-first)+1 {
		return in, errors.New("every parent CNAME owner in the exact range requires one explicit observation")
	}
	expected := map[string]string{}
	for n := first; n <= last; n++ {
		expected[fmt.Sprintf("%d.%s", n, parent)] = fmt.Sprintf("%d.%s", n, in.ChildZone)
	}
	seen := map[string]bool{}
	for i, claim := range in.Parent.Owners {
		owner, e := SafeFQDN(claim.Owner)
		target, exists := expected[owner]
		if e != nil || !exists || seen[owner] {
			return in, errors.New("duplicate, missing or out-of-range parent CNAME ownership declaration")
		}
		seen[owner] = true
		claim.Owner = owner
		switch claim.State {
		case "absent":
			if claim.Target != "" {
				return in, errors.New("absent owner cannot carry an existing CNAME target")
			}
		case "cname":
			claim.Target, e = SafeFQDN(claim.Target)
			if e != nil || claim.Target != target {
				return in, errors.New("existing CNAME points elsewhere; chained/reassigned aliases are not overwritten")
			}
		default:
			return in, errors.New("parent owner has conflicting or unknown data; PTR/CNAME coexistence is forbidden")
		}
		in.Parent.Owners[i] = claim
	}
	sort.Slice(in.Parent.Owners, func(i, j int) bool { return in.Parent.Owners[i].Owner < in.Parent.Owners[j].Owner })
	if len(in.PTRs) > int(last-first)+1 {
		return in, errors.New("PTR count exceeds delegated range")
	}
	addresses := map[netip.Addr]bool{}
	for i, record := range in.PTRs {
		address, e := netip.ParseAddr(record.Address)
		if e != nil || !address.Is4() || !prefix.Contains(address) || addresses[address] {
			return in, errors.New("PTR addresses must be unique IPv4 owners within the exact delegated range")
		}
		addresses[address] = true
		name, e := SafeFQDN(record.Name)
		if e != nil {
			return in, errors.New("PTR requires an explicit safe target hostname")
		}
		in.PTRs[i] = RFC2317PTR{address.String(), name}
	}
	sort.Slice(in.PTRs, func(i, j int) bool {
		a, _ := netip.ParseAddr(in.PTRs[i].Address)
		b, _ := netip.ParseAddr(in.PTRs[j].Address)
		return a.Compare(b) < 0
	})
	return in, nil
}

func RenderRFC2317(input RFC2317Input) (RFC2317Bundle, error) {
	in, err := NormalizeRFC2317(input)
	if err != nil {
		return RFC2317Bundle{}, err
	}
	var child, parent strings.Builder
	o := in.DNS
	fmt.Fprintf(&child, "; Generated by MoeDove IXP Manager. RFC2317 child authority; NOT approved or activated.\n$ORIGIN %s\n$TTL %d\n@ IN SOA %s %s (\n  %d ; serial\n  %d ; refresh\n  %d ; retry\n  %d ; expire\n  %d ; minimum\n)\n", in.ChildZone, o.TTL, o.PrimaryNS, o.ResponsibleMailbox, o.Serial, o.Refresh, o.Retry, o.Expire, o.Minimum)
	for _, ns := range o.NameServers {
		fmt.Fprintf(&child, "@ IN NS %s\n", ns)
	}
	for _, record := range in.PTRs {
		address, _ := netip.ParseAddr(record.Address)
		b := address.As4()
		fmt.Fprintf(&child, "%d.%s IN PTR %s\n", b[3], in.ChildZone, record.Name)
	}
	fmt.Fprintf(&parent, "; Generated by MoeDove IXP Manager. RFC2317 parent delegation FRAGMENT; NOT a complete zone.\n; Parent: %s\n; Operator-reviewed snapshot SHA256: %s\n; Observed parent SOA: %d (reference only; not reserved or advanced).\n; Merge/conflict checking, parent serial advancement and separate approvals remain required.\n", in.Parent.Zone, in.Parent.SnapshotSHA256, *in.Parent.ObservedSerial)
	for _, ns := range o.NameServers {
		fmt.Fprintf(&parent, "%s %d IN NS %s\n", in.ChildZone, in.Parent.TTL, ns)
	}
	for n := *in.First; n <= *in.Last; n++ {
		fmt.Fprintf(&parent, "%d.%s %d IN CNAME %d.%s\n", n, in.Parent.Zone, in.Parent.TTL, n, in.ChildZone)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return RFC2317Bundle{}, err
	}
	bundle := RFC2317Bundle{Version: 1, Kind: RFC2317Kind, Input: in, InputSHA256: rfc2317Hash(raw), Files: []RFC2317File{{Path: "child.zone", Zone: in.ChildZone, Complete: true, Content: child.String()}, {Path: "parent.fragment", Zone: in.Parent.Zone, Complete: false, Content: parent.String()}}}
	for i := range bundle.Files {
		bundle.Files[i].SHA256 = rfc2317Hash([]byte(bundle.Files[i].Content))
	}
	return bundle, nil
}
func ValidateRFC2317Bundle(bundle RFC2317Bundle) error {
	expected, err := RenderRFC2317(bundle.Input)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(bundle, expected) {
		return errors.New("RFC2317 bundle differs from its exact generated range/owner/serial contract")
	}
	return nil
}
func EncodeRFC2317(bundle RFC2317Bundle) ([]byte, error) {
	if err := ValidateRFC2317Bundle(bundle); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil || len(raw)+1 > MaxRFC2317Bytes {
		return nil, errors.New("RFC2317 bundle exceeds size bound")
	}
	return append(raw, '\n'), nil
}

func decodeRFC2317JSON(raw []byte, value any) error {
	return decodeRFC2317JSONLimit(raw, value, MaxRFC2317Bytes)
}

func decodeRFC2317JSONLimit(raw []byte, value any, limit int) error {
	if len(raw) == 0 || len(raw) > limit || !utf8.Valid(raw) {
		return errors.New("RFC2317 JSON exceeds size/encoding bounds")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	nodes := 0
	var walk func(int) error
	walk = func(depth int) error {
		nodes++
		if depth > 20 || nodes > 12000 {
			return errors.New("RFC2317 JSON structure exceeds bounds")
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := t.(json.Delim); ok {
			switch delimiter {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, e := d.Token()
					if e != nil {
						return e
					}
					name, ok := key.(string)
					if !ok {
						return errors.New("invalid key")
					}
					for _, r := range name {
						if r > 127 {
							return errors.New("non-ASCII contract field")
						}
					}
					folded := strings.ToLower(name)
					if seen[folded] {
						return errors.New("duplicate contract field")
					}
					seen[folded] = true
					if e = walk(depth + 1); e != nil {
						return e
					}
				}
			case '[':
				for d.More() {
					if e := walk(depth + 1); e != nil {
						return e
					}
				}
			default:
				return errors.New("invalid delimiter")
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := walk(0); err != nil {
		return errors.New("invalid or ambiguous RFC2317 JSON")
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing RFC2317 JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return errors.New("invalid typed RFC2317 fields")
	}
	return nil
}
func DecodeRFC2317Input(raw []byte) (RFC2317Input, error) {
	var in RFC2317Input
	if err := decodeRFC2317JSON(raw, &in); err != nil {
		return in, err
	}
	return NormalizeRFC2317(in)
}
func DecodeRFC2317(raw []byte) (RFC2317Bundle, error) {
	var b RFC2317Bundle
	if err := decodeRFC2317JSON(raw, &b); err != nil {
		return b, err
	}
	canonical, err := EncodeRFC2317(b)
	if err != nil {
		return b, err
	}
	if !bytes.Equal(raw, canonical) {
		return b, errors.New("RFC2317 bundle must retain its exact canonical JSON bytes")
	}
	return b, nil
}

// This validates only generated files. The wrapper is deliberately synthetic,
// never returned as an authority artifact and never substitutes for merging
// with/revalidating the actual complete parent zone under its owner's approval.
func ValidateRFC2317(ctx context.Context, bundle RFC2317Bundle, options ValidatorOptions) (RFC2317Validation, error) {
	if err := ValidateRFC2317Bundle(bundle); err != nil {
		return RFC2317Validation{}, err
	}
	binary := options.NamedCheckzone
	if binary == "" {
		binary = "named-checkzone"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "moedove-rfc2317-")
	if err != nil {
		return RFC2317Validation{}, err
	}
	defer os.RemoveAll(dir)
	child := filepath.Join(dir, "child.zone")
	parent := filepath.Join(dir, "synthetic-parent.zone")
	if err = os.WriteFile(child, []byte(bundle.Files[0].Content), 0600); err != nil {
		return RFC2317Validation{}, err
	}
	wrapper := fmt.Sprintf("; SYNTHETIC validation wrapper only; never install.\n$ORIGIN %s\n$TTL 300\n@ IN SOA ns.parent-check.invalid. hostmaster.parent-check.invalid. ( %d 3600 600 604800 300 )\n@ IN NS ns.parent-check.invalid.\n", bundle.Input.Parent.Zone, *bundle.Input.Parent.ObservedSerial) + bundle.Files[1].Content
	if err = os.WriteFile(parent, []byte(wrapper), 0600); err != nil {
		return RFC2317Validation{}, err
	}
	if _, err = command(ctx, binary, "-i", "local", bundle.Input.ChildZone, child); err != nil {
		return RFC2317Validation{}, errors.New("named-checkzone unavailable or rejected RFC2317 child zone")
	}
	if _, err = command(ctx, binary, "-i", "local", bundle.Input.Parent.Zone, parent); err != nil {
		return RFC2317Validation{}, errors.New("named-checkzone unavailable or rejected parent fragment in synthetic wrapper")
	}
	return RFC2317Validation{Child: Validation{"native", "Generated complete child parsed; serial is caller-supplied, not reserved or approved"}, ParentFragment: Validation{"native-synthetic-wrapper", "Fragment parsed only inside a synthetic parent; actual parent snapshot, ownership, merge, serial advancement and approval remain unverified"}}, nil
}
