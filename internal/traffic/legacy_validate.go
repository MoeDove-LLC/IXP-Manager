package traffic

import (
	"errors"
	"strings"
	"time"
)

func validateLegacySemantics(batch LegacyBatch, row LegacyRow, loc *time.Location) error {
	if len(row.Notices) > 16 {
		return errors.New("too many legacy row notices")
	}
	for _, notice := range row.Notices {
		if len(notice) > 2000 || strings.ContainsRune(notice, 0) {
			return errors.New("invalid legacy notice")
		}
	}
	var e error
	if row.OriginalDay != "" {
		if _, _, e = legacyDay(row.OriginalDay, loc); e != nil {
			return e
		}
	}
	switch batch.Format {
	case LegacyTableJSON:
		if len(batch.Columns) != 0 {
			return errors.New("table batch cannot contain RRD column declarations")
		}
		if batch.Source.Table == "p2p_daily_stats" {
			if batch.Source.Backend != "sflow-rrd" || row.SourceKind != "legacy-p2p-daily" || row.EntityKind != "customer" || !legacyID.MatchString(row.PeerEntityID) || row.OriginalDay == "" || row.Category != "bits" || row.Family != 4 && row.Family != 6 || row.TotalUnit != "bits" || row.RateUnit != "bits/s" || row.Window.Kind != "calendar-day" || row.Window.Period != "calendar-day" || row.MaxSemantics != "legacy-sum-of-independent-maxima" {
				return errors.New("invalid P2P legacy report semantics")
			}
			if row.Window.BoundaryQuality != "calendar-label" && row.Window.BoundaryQuality != "declared" {
				return errors.New("invalid P2P boundary quality")
			}
			if row.ID != legacyDigest([]byte(batch.Source.Table+"/"+row.RecordID+"/"+map[int]string{4: "4", 6: "6"}[row.Family])) {
				return errors.New("P2P row identity mismatch")
			}
		} else {
			kind := "customer"
			if batch.Source.Table == "traffic_daily_phys_ints" {
				kind = "physical-interface"
			} else if batch.Source.Table != "traffic_daily" {
				return errors.New("unknown legacy table")
			}
			total, rate, e := legacyUnits(row.Category)
			if e != nil {
				return e
			}
			if batch.Source.Backend != "rrd" && batch.Source.Backend != "mrtg" || row.SourceKind != "legacy-"+batch.Source.Backend+"-window" || row.EntityKind != kind || row.PeerEntityID != "" || row.Family != 0 || row.TotalUnit != total || row.RateUnit != rate || row.Window.Kind != "rolling" || legacyPeriodSeconds[row.Window.Period] == 0 || row.Window.NominalSeconds != legacyPeriodSeconds[row.Window.Period] || row.MaxSemantics != "legacy-reported-maximum" {
				return errors.New("invalid legacy rolling-window semantics")
			}
			if row.Window.BoundaryQuality != "unknown" && row.Window.BoundaryQuality != "declared" {
				return errors.New("rolling windows cannot acquire inferred exact bounds")
			}
			if row.ID != legacyDigest([]byte(batch.Source.Table+"/"+row.RecordID+"/"+row.Window.Period)) {
				return errors.New("rolling row identity mismatch")
			}
		}
	case LegacyRRDJSON, LegacyRRDXML:
		if batch.Source.Backend != "rrd" && batch.Source.Backend != "sflow-rrd" || len(batch.Columns) < 1 || len(batch.Columns) > 8 || row.SourceKind != "legacy-rrd-rate" || row.EntityID != batch.Source.EntityID || row.EntityKind != batch.Source.EntityKind || row.OriginalDay != "" || row.Category != batch.Source.Unit || row.TotalUnit != batch.Source.Unit || row.RateUnit != batch.Source.Unit+"/s" || row.Window.Kind != "rate-bucket" || row.Window.Period != "step" || row.Window.BoundaryQuality != "export" || row.Window.NominalSeconds < 1 || row.Window.NominalSeconds > 366*86400 || row.Window.Start == nil || row.Window.End == nil || row.Window.End.Sub(*row.Window.Start) != time.Duration(row.Window.NominalSeconds)*time.Second || row.MaxSemantics != "RRD-consolidated-rate" || row.In.Total != nil || row.Out.Total != nil {
			return errors.New("RRD rates must retain their exported intervals and cannot become counter totals")
		}
		if row.ID != legacyDigest([]byte("rrd/"+row.RecordID)) {
			return errors.New("RRD row identity mismatch")
		}
	default:
		return errors.New("unknown legacy batch format")
	}
	if row.Window.BoundaryQuality == "unknown" {
		if row.Window.Start != nil || row.Window.End != nil || row.Window.Evidence != "" {
			return errors.New("unknown legacy window cannot contain invented bounds")
		}
	} else if row.Window.Start == nil || row.Window.End == nil || row.Window.Start.IsZero() || !row.Window.End.After(*row.Window.Start) {
		return errors.New("legacy window requires complete increasing bounds")
	}
	if row.Window.BoundaryQuality == "declared" && (strings.TrimSpace(row.Window.Evidence) == "" || len(row.Window.Evidence) > 1000) {
		return errors.New("declared window lacks historical evidence")
	}
	if row.StaffOnly {
		if row.OwnerEvidence != nil || row.MemberID != "" || row.MemberRevision != "" {
			return errors.New("staff-only row contains inconsistent member authorization")
		}
	} else {
		owner := row.OwnerEvidence
		if owner == nil || owner.EntityKind != row.EntityKind || owner.EntityID != row.EntityID || owner.MemberID != row.MemberID || owner.MemberRevision != row.MemberRevision || !legacyID.MatchString(owner.MemberID) || !legacyHash.MatchString(owner.MemberRevision) || !legacyHash.MatchString(owner.ReferenceSHA256) || owner.ValidFrom.IsZero() || !owner.ValidTo.After(owner.ValidFrom) || strings.TrimSpace(owner.Description) == "" || len(owner.Description) > 1000 || row.Window.Start == nil || row.Window.End == nil || owner.ValidFrom.After(*row.Window.Start) || owner.ValidTo.Before(*row.Window.End) || row.Window.BoundaryQuality != "export" && row.Window.BoundaryQuality != "declared" {
			return errors.New("legacy member attribution is not covered by exact-window historical evidence")
		}
	}
	for _, m := range []LegacyMeasure{row.In, row.Out} {
		values := map[string]*string{"average": m.Average, "maximum": m.Maximum, "minimum": m.Minimum, "last": m.Last, "total": m.Total}
		for name, reason := range m.Unknown {
			if name != "maxAt" {
				value, ok := values[name]
				if !ok || value != nil {
					return errors.New("legacy unknown marker conflicts with numeric value")
				}
			} else if m.MaxAt != nil {
				return errors.New("ambiguous legacy maximum timestamp was resolved implicitly")
			}
			switch reason {
			case "missing", "null", "nan", "ambiguous-local-time", "rate-only-export":
			default:
				return errors.New("unsupported legacy unknown-value reason")
			}
		}
		if len(m.OriginalMaxAt) > 64 {
			return errors.New("oversized original maximum timestamp")
		}
	}
	return nil
}
