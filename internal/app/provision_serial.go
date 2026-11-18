package app

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/provision"
)

func provisionSerialZone(zone string) bool {
	canonical, err := provision.SafeFQDN(zone)
	return err == nil && canonical == zone && (strings.HasSuffix(zone, ".in-addr.arpa.") || strings.HasSuffix(zone, ".ip6.arpa."))
}

type provisionZoneSerial struct {
	zone   string
	serial uint32
}

func provisionRevisionSerials(d ProvisionRevision) []provisionZoneSerial {
	if d.Kind == "dns-arpa" {
		return []provisionZoneSerial{{d.Zone, d.Options.DNS.Serial}}
	}
	if p := d.RFC2317; p != nil && d.Kind == provision.RFC2317MergeKind {
		return []provisionZoneSerial{{p.ParentZone, p.ParentSerial}, {p.ChildZone, p.ChildSerial}}
	}
	return nil
}

func rfc2317ApprovedZone(s *State, zone string) bool {
	approved, exists := s.ProvisionApprovedSerials[zone]
	if !exists {
		return false
	}
	for _, revision := range s.ProvisionRevisions {
		p := revision.RFC2317
		if p != nil && revision.Status == "approved" && (p.ParentZone == zone && p.ParentSerial == approved || p.ChildZone == zone && p.ChildSerial == approved) {
			return true
		}
	}
	return false
}

// The heads outlive profiles and retained artifacts. Every retained serial must
// remain in the unambiguous RFC 1982 window behind its head. Numeric max/min is
// invalid here, including during startup, backup validation and resource export.
func validateProvisionSerials(s *State) error {
	for zone := range s.ProvisionSerials {
		if !provisionSerialZone(zone) {
			return errors.New("invalid issued DNS serial zone")
		}
	}
	for zone, approved := range s.ProvisionApprovedSerials {
		issued, exists := s.ProvisionSerials[zone]
		order, err := provision.CompareSerial(approved, issued)
		if !exists || err != nil || order > 0 {
			return errors.New("approved DNS serial is not within the issued RFC1982 window")
		}
	}
	seen := map[string]map[uint32]bool{}
	for _, d := range s.ProvisionRevisions {
		for _, version := range provisionRevisionSerials(d) {
			issued, exists := s.ProvisionSerials[version.zone]
			order, err := provision.CompareSerial(version.serial, issued)
			if !exists || err != nil || order > 0 {
				return errors.New("DNS serial accounting does not match retained RFC1982 history")
			}
			if seen[version.zone] == nil {
				seen[version.zone] = map[uint32]bool{}
			}
			if seen[version.zone][version.serial] {
				return errors.New("duplicate retained DNS serial; serial reuse is forbidden")
			}
			seen[version.zone][version.serial] = true
			if d.Status == "approved" {
				approved, exists := s.ProvisionApprovedSerials[version.zone]
				order, err := provision.CompareSerial(version.serial, approved)
				if !exists || err != nil || order > 0 {
					return errors.New("approved DNS revision does not match its RFC1982 ledger")
				}
			}
		}
	}
	return nil
}

// A profile baseline is an explicit operator-supplied observation, never a
// guessed production SOA. Map presence distinguishes an actual zero from no
// ledger. A known zone cannot become new when its profile is deleted/recreated.
func provisionSerialReference(s *State, profile Record, zone string) (uint32, bool, error) {
	isNew, baseline, err := dnsProfileBaseline(profile)
	if err != nil {
		return 0, false, err
	}
	issued, hasIssued := s.ProvisionSerials[zone]
	if !hasIssued {
		return baseline, !isNew, nil
	}
	if isNew {
		return issued, true, nil
	}
	order, err := provision.CompareSerial(baseline, issued)
	if err != nil {
		return 0, false, fmt.Errorf("DNS baseline and issued serial require an explicit RFC1982 reconciliation: %w", err)
	}
	if order > 0 {
		return baseline, true, nil
	}
	return issued, true, nil
}

func allocateProvisionSerial(s *State, profile Record, options provision.Options) (uint32, error) {
	if options.Kind != "dns-arpa" {
		return 0, nil
	}
	if err := validateProvisionSerials(s); err != nil {
		return 0, err
	}
	zone, err := provision.ReverseZone(options.DNS.Prefix)
	if err != nil {
		return 0, err
	}
	if rfc2317ApprovedZone(s, zone) {
		return 0, errors.New("this zone uses an approved RFC 2317 pair; use a complete paired review to preserve its delegation")
	}
	previous, known, err := provisionSerialReference(s, profile, zone)
	if err != nil {
		return 0, err
	}
	next := provision.NextSOASerial(previous)
	if !known {
		// Only an explicitly new, never-issued zone may choose its initial value.
		now := time.Now().Unix()
		if now > 0 && uint64(now) <= uint64(^uint32(0)) {
			next = uint32(now)
		}
	}
	check := func(reference uint32) error {
		order, err := provision.CompareSerial(next, reference)
		if err != nil || order <= 0 {
			return errors.New("next DNS serial is outside the unambiguous RFC1982 window; reconcile the explicit baseline and retained history")
		}
		return nil
	}
	if issued, exists := s.ProvisionSerials[zone]; exists {
		if err := check(issued); err != nil {
			return 0, err
		}
	}
	if approved, exists := s.ProvisionApprovedSerials[zone]; exists {
		if err := check(approved); err != nil {
			return 0, err
		}
	}
	isNew, baseline, _ := dnsProfileBaseline(profile)
	if !isNew {
		if err := check(baseline); err != nil {
			return 0, err
		}
	}
	for _, d := range s.ProvisionRevisions {
		for _, version := range provisionRevisionSerials(d) {
			if version.zone == zone {
				if err := check(version.serial); err != nil {
					return 0, err
				}
			}
		}
	}
	if s.ProvisionSerials == nil {
		s.ProvisionSerials = map[string]uint32{}
	}
	s.ProvisionSerials[zone] = next
	return next, nil
}

func approveProvisionSerial(s *State, d ProvisionRevision) error {
	if err := validateProvisionSerials(s); err != nil {
		return err
	}
	if d.Kind == "dns-arpa" && rfc2317ApprovedZone(s, d.Zone) {
		return errors.New("this zone uses an approved RFC 2317 pair; use a complete paired review to preserve its delegation")
	}
	if p := d.RFC2317; p != nil && !p.approvalHeadsMatch(s) {
		return errors.New("parent or child approval has changed; merge against the current pair before approving")
	}
	versions := provisionRevisionSerials(d)
	for _, version := range versions {
		issued, exists := s.ProvisionSerials[version.zone]
		order, err := provision.CompareSerial(version.serial, issued)
		if !exists || err != nil || order > 0 {
			return errors.New("DNS revision has no matching issued serial")
		}
		if approved, exists := s.ProvisionApprovedSerials[version.zone]; exists {
			order, err := provision.CompareSerial(version.serial, approved)
			if err != nil || order <= 0 {
				return errors.New("DNS serial does not advance the approved RFC1982 baseline; generate a new revision")
			}
		}
	}
	if s.ProvisionApprovedSerials == nil {
		s.ProvisionApprovedSerials = map[string]uint32{}
	}
	for _, version := range versions {
		s.ProvisionApprovedSerials[version.zone] = version.serial
	}
	return nil
}

func provisionSerialApproved(s *State, d ProvisionRevision) bool {
	versions := provisionRevisionSerials(d)
	for _, version := range versions {
		approved, present := s.ProvisionApprovedSerials[version.zone]
		if !present || approved != version.serial {
			return false
		}
	}
	return len(versions) == 0 || validateProvisionSerials(s) == nil
}

func reserveRFC2317Serials(s *State, d ProvisionRevision) error {
	if err := validateProvisionSerials(s); err != nil {
		return err
	}
	versions := provisionRevisionSerials(d)
	for _, version := range versions {
		if head, exists := s.ProvisionSerials[version.zone]; exists {
			order, err := provision.CompareSerial(version.serial, head)
			if err != nil || order <= 0 {
				return fmt.Errorf("the %s serial must advance its issued RFC1982 head", version.zone)
			}
		}
		for _, revision := range s.ProvisionRevisions {
			for _, retained := range provisionRevisionSerials(revision) {
				if retained.zone == version.zone {
					order, err := provision.CompareSerial(version.serial, retained.serial)
					if err != nil || order <= 0 {
						return fmt.Errorf("the %s serial would collide with retained RFC1982 history", version.zone)
					}
				}
			}
		}
	}
	if s.ProvisionSerials == nil {
		s.ProvisionSerials = map[string]uint32{}
	}
	for _, version := range versions {
		s.ProvisionSerials[version.zone] = version.serial
	}
	return nil
}
