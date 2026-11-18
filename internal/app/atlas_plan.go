package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/atlas"
)

func atlasRunChecksum(r AtlasRun) string {
	items := []Record{}
	for _, m := range r.Items {
		items = append(items, Record{"id": m.ID, "sourceMemberId": m.SourceMemberID, "targetMemberId": m.TargetMemberID, "targetAddress": m.TargetAddress, "probeIds": m.ProbeIDs, "estimatedCredits": m.EstimatedCredits, "estimatedSamples": m.EstimatedSamples})
	}
	raw, _ := json.Marshal(Record{"id": r.ID, "spec": r.Spec, "items": items, "baseChecksum": r.BaseChecksum, "providerOrigin": r.ProviderOrigin, "estimatedCredits": r.EstimatedCredits, "estimatedSamples": r.EstimatedSamples})
	return digest(string(raw))
}
func atlasMember(s *State, id string) (Record, uint32, error) {
	m := find(s, "members", id)
	asn := number(m, "asn")
	if m == nil || str(m, "status") != "active" || asn <= 0 || asn > 4294967294 || asn != float64(uint32(asn)) {
		return nil, 0, errors.New("Atlas requires an active member with a valid ASN")
	}
	return m, uint32(asn), nil
}
func atlasProbeByID(probes []atlas.Probe, id int64, af int, asn uint32) (atlas.Probe, error) {
	for _, p := range probes {
		if p.ID == id {
			a, address := p.ASNV4, p.AddressV4
			if af == 6 {
				a, address = p.ASNV6, p.AddressV6
			}
			if p.Status == "connected" && p.IsPublic && a == asn && atlas.ValidTarget(address, af) {
				return p, nil
			}
		}
	}
	return atlas.Probe{}, errors.New("selected probe is not a connected public probe for this member and family")
}
func atlasProbeAddress(p atlas.Probe, af int) string {
	if af == 6 {
		return p.AddressV6
	}
	return p.AddressV4
}
func atlasOnVLAN(s *State, member, vlan string, af int) bool {
	for _, c := range s.Resources["connections"] {
		if str(c, "memberId") == member && str(c, "vlanId") == vlan && str(c, "status") == "active" && str(c, fmt.Sprintf("ipv%d", af)) != "" {
			return true
		}
	}
	return false
}
func (a *App) atlasPlanLocked(s *State, p AtlasProposal, now time.Time) (AtlasProposal, []AtlasMeasurement, string, int, int, error) {
	fail := func(message string) (AtlasProposal, []AtlasMeasurement, string, int, int, error) {
		return p, nil, "", 0, 0, errors.New(message)
	}
	if len(p.Name) < 1 || len(p.Name) > 120 || strings.ContainsAny(p.Name, "\r\n\x00") {
		return fail("Atlas run name must be1..120 characters without controls")
	}
	if p.Mode == "" {
		p.Mode = "single"
	}
	if p.Mode != "single" && p.Mode != "matrix" {
		return fail("Atlas mode must be single or matrix")
	}
	if p.AF != 4 && p.AF != 6 {
		return fail("Atlas address family must be4 or6")
	}
	type source struct {
		member string
		asn    uint32
		probes []atlas.Probe
		ids    []int64
		target string
	}
	sources := []source{}
	bindings := []any{}
	load := func(member string, ids []int64, targetID int64, needTarget bool) (source, error) {
		m, asn, err := atlasMember(s, member)
		if err != nil {
			return source{}, err
		}
		probes, err := a.atlasProbesLocked(s, member, p.AF, now)
		if err != nil {
			return source{}, err
		}
		ids = append([]int64(nil), ids...)
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		if len(ids) < 1 || len(ids) > atlas.MaxProbes {
			return source{}, errors.New("select1..20 source probes")
		}
		selected := []any{}
		for i, id := range ids {
			if i > 0 && id == ids[i-1] {
				return source{}, errors.New("duplicate source probe")
			}
			probe, err := atlasProbeByID(probes, id, p.AF, asn)
			if err != nil {
				return source{}, err
			}
			selected = append(selected, []any{id, asn, atlasProbeAddress(probe, p.AF)})
		}
		target := ""
		if needTarget {
			probe, err := atlasProbeByID(probes, targetID, p.AF, asn)
			if err != nil {
				return source{}, err
			}
			target = atlasProbeAddress(probe, p.AF)
		}
		bindings = append(bindings, Record{"memberId": member, "asn": asn, "status": str(m, "status"), "selected": selected, "targetProbeId": targetID, "target": target})
		return source{member, asn, probes, ids, target}, nil
	}
	items := []AtlasMeasurement{}
	if p.Mode == "matrix" {
		if !identifier.MatchString(p.VLANID) || find(s, "vlans", p.VLANID) == nil || len(p.Participants) < 2 || len(p.Participants) > 10 {
			return fail("matrix requires one existing VLAN and2..10 explicit members")
		}
		if p.SourceMemberID != "" || p.TargetMemberID != "" || p.TargetType != "" || p.TargetID != "" || len(p.ProbeIDs) != 0 {
			return fail("matrix cannot include single-target fields")
		}
		p.Participants = append([]AtlasParticipant(nil), p.Participants...)
		sort.Slice(p.Participants, func(i, j int) bool { return p.Participants[i].MemberID < p.Participants[j].MemberID })
		for i, part := range p.Participants {
			if i > 0 && part.MemberID == p.Participants[i-1].MemberID || !atlasOnVLAN(s, part.MemberID, p.VLANID, p.AF) {
				return fail("matrix members must be distinct and actively connected to the same VLAN and family")
			}
			src, err := load(part.MemberID, part.ProbeIDs, part.TargetProbeID, true)
			if err != nil {
				return p, nil, "", 0, 0, err
			}
			p.Participants[i].ProbeIDs = src.ids
			sources = append(sources, src)
		}
		for i, src := range sources {
			for j, dest := range sources {
				if i != j {
					items = append(items, AtlasMeasurement{SourceMemberID: src.member, TargetMemberID: dest.member, TargetAddress: dest.target, ProbeIDs: src.ids, Status: "draft"})
				}
			}
		}
		bindings = append(bindings, Record{"vlanId": p.VLANID})
	} else {
		if len(p.Participants) != 0 || p.VLANID != "" {
			return fail("single mode cannot include matrix fields")
		}
		src, err := load(p.SourceMemberID, p.ProbeIDs, 0, false)
		if err != nil {
			return p, nil, "", 0, 0, err
		}
		p.ProbeIDs = src.ids
		member, asn, err := atlasMember(s, p.TargetMemberID)
		if err != nil {
			return p, nil, "", 0, 0, err
		}
		target := ""
		switch p.TargetType {
		case "probe":
			id, e := strconv.ParseInt(p.TargetID, 10, 64)
			if e != nil || strconv.FormatInt(id, 10) != p.TargetID {
				return fail("targetId must be an explicit Atlas probe ID")
			}
			probes, e := a.atlasProbesLocked(s, p.TargetMemberID, p.AF, now)
			if e != nil {
				return p, nil, "", 0, 0, e
			}
			probe, e := atlasProbeByID(probes, id, p.AF, asn)
			if e != nil {
				return p, nil, "", 0, 0, e
			}
			target = atlasProbeAddress(probe, p.AF)
		case "connection":
			c := find(s, "connections", p.TargetID)
			if c == nil || str(c, "memberId") != p.TargetMemberID || str(c, "status") != "active" {
				return fail("target connection must currently belong to the selected member and be active")
			}
			target = str(c, fmt.Sprintf("ipv%d", p.AF))
			if ip, e := netip.ParseAddr(target); e == nil {
				target = ip.String()
			}
			bindings = append(bindings, Record{"connectionId": p.TargetID, "memberId": p.TargetMemberID, "vlanId": str(c, "vlanId"), "address": target, "status": str(c, "status")})
		default:
			return fail("targetType must be probe or connection")
		}
		bindings = append(bindings, Record{"targetMember": p.TargetMemberID, "asn": asn, "status": str(member, "status"), "target": target, "targetType": p.TargetType, "targetId": p.TargetID})
		items = append(items, AtlasMeasurement{SourceMemberID: src.member, TargetMemberID: p.TargetMemberID, TargetAddress: target, ProbeIDs: src.ids, Status: "draft"})
	}
	credits, samples := 0, 0
	for i := range items {
		m := &items[i]
		_, estimate, err := atlas.NormalizeSpec(atlas.Spec{Type: p.Type, AF: p.AF, Target: m.TargetAddress, ProbeIDs: m.ProbeIDs, IsOneoff: p.IsOneoff, IntervalSeconds: p.IntervalSeconds, DurationSeconds: p.DurationSeconds, StartAt: p.StartAt, IsPublic: p.IsPublic}, now)
		if err != nil {
			return p, nil, "", 0, 0, err
		}
		m.EstimatedCredits, m.EstimatedSamples = estimate.Credits, estimate.Samples
		credits += estimate.Credits
		samples += estimate.Samples
	}
	if credits > atlas.MaxEstimatedCredits || samples > atlas.MaxSamples {
		return fail("the entire run exceeds20000 estimated credits or1000 samples; reduce members, probes or duration")
	}
	raw, _ := json.Marshal(bindings)
	return p, items, digest(string(raw)), credits, samples, nil
}
func (a *App) atlasCurrentLocked(s *State, r *AtlasRun, now time.Time) error {
	_, _, base, credits, samples, err := a.atlasPlanLocked(s, r.Spec, now)
	if err != nil {
		return err
	}
	if base != r.BaseChecksum || credits != r.EstimatedCredits || samples != r.EstimatedSamples {
		return errors.New("Atlas target/probe ownership or topology changed; create and review a new proposal")
	}
	return nil
}
func atlasRunStatus(r *AtlasRun) {
	if r.Pruned {
		r.Status = "archived"
		return
	}
	counts := map[string]int{}
	for _, m := range r.Items {
		counts[m.Status]++
	}
	n := len(r.Items)
	for _, status := range []string{"creating", "uncertain", "stopping"} {
		if counts[status] > 0 {
			if counts[status] == n {
				r.Status = status
			} else {
				r.Status = "partial"
			}
			return
		}
	}
	if counts["draft"] == n {
		r.Status = "draft"
		return
	}
	if counts["approved"] == n {
		r.Status = "approved"
		return
	}
	if counts["running"] > 0 {
		r.Status = "running"
		return
	}
	if counts["scheduled"] > 0 {
		r.Status = "scheduled"
		return
	}
	if counts["approved"] > 0 {
		r.Status = "partial"
		return
	}
	if counts["failed"] > 0 {
		r.Status = "failed"
		return
	}
	if counts["stopped"] > 0 || counts["cancelled"] > 0 {
		r.Status = "stopped"
		return
	}
	r.Status = "completed"
}
func atlasSpec(r AtlasRun, m AtlasMeasurement) atlas.Spec {
	return atlas.Spec{Type: r.Spec.Type, AF: r.Spec.AF, Target: m.TargetAddress, ProbeIDs: m.ProbeIDs, IsOneoff: r.Spec.IsOneoff, IntervalSeconds: r.Spec.IntervalSeconds, DurationSeconds: r.Spec.DurationSeconds, StartAt: m.ScheduledStart, IsPublic: r.Spec.IsPublic, Description: m.Description}
}
