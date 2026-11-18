package traffic

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

var legacyID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
var legacyHash = regexp.MustCompile(`^[a-f0-9]{64}$`)
var legacyDecimal = regexp.MustCompile(`^([+-]?)([0-9]+)(?:\.([0-9]*))?(?:[eE]([+-]?[0-9]{1,3}))?$`)
var legacyZone = regexp.MustCompile(`^[A-Za-z0-9_+-]+(?:/[A-Za-z0-9_+-]+)*$`)
var legacyPeriods = []string{"day", "week", "month", "year"}
var legacyPeriodSeconds = map[string]int64{"day": 119988, "week": 719712, "month": 2879712, "year": 31622400}

func legacyDigest(b []byte) string                { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func legacyPayload(b LegacyBatch) ([]byte, error) { b.ID = ""; b.Checksum = ""; return json.Marshal(b) }
func legacySeal(b *LegacyBatch) error {
	raw, e := legacyPayload(*b)
	if e != nil {
		return e
	}
	if len(raw) > LegacyMaxBatchBytes {
		return errors.New("legacy normalized batch exceeds64MiB")
	}
	b.Checksum = legacyDigest(raw)
	b.ID = b.Checksum
	return nil
}
func legacySourceValidate(s LegacySource) (*time.Location, error) {
	if strings.TrimSpace(s.Name) == "" || len(s.Name) > 200 || strings.ContainsAny(s.Name, "\x00\r\n") {
		return nil, errors.New("explicit bounded legacy source name is required")
	}
	if s.Timezone == "" || s.Timezone == "Local" || !legacyZone.MatchString(s.Timezone) {
		return nil, errors.New("explicit IANA source timezone is required")
	}
	loc, e := time.LoadLocation(s.Timezone)
	if e != nil {
		return nil, errors.New("unknown source timezone")
	}
	switch s.Backend {
	case "mrtg", "rrd", "sflow-rrd":
	default:
		return nil, errors.New("source backend must be mrtg, rrd or sflow-rrd")
	}
	return loc, nil
}
func ParseLegacy(input LegacyInput) (LegacyBatch, error) {
	b := LegacyBatch{Version: 1, Format: input.Format, Source: input.Source, Rows: []LegacyRow{}, Notices: []string{"Legacy report rows retain their original windows and units. They are never added to live counter deltas or implicitly summed across overlapping windows."}}
	b.Columns = append([]LegacyColumn(nil), input.Columns...)
	if len(input.Content) == 0 || len(input.Content) > LegacyMaxInputBytes {
		return b, errors.New("legacy input must contain1..16MiB of export data")
	}
	loc, e := legacySourceValidate(input.Source)
	if e != nil {
		return b, e
	}
	if len(input.Windows) > LegacyMaxRows || len(input.Owners) > 2000 {
		return b, errors.New("legacy declarations exceed bounds")
	}
	if input.Format != LegacyTableJSON && len(input.Windows) > 0 {
		return b, errors.New("RRD exported intervals cannot be replaced by declared windows")
	}
	b.InputChecksum = legacyDigest([]byte(input.Content))
	switch input.Format {
	case LegacyTableJSON:
		b.Rows, e = legacyTableRows(input, loc)
	case LegacyRRDJSON, LegacyRRDXML:
		b.Rows, e = legacyRRDRows(input, loc)
	default:
		e = errors.New("unsupported explicit legacy export format")
	}
	if e != nil {
		return b, e
	}
	if len(b.Rows) == 0 || len(b.Rows) > LegacyMaxRows {
		return b, errors.New("legacy export must produce1..20000 rows")
	}
	if e = legacyApplyDeclarations(&b, input); e != nil {
		return b, e
	}
	sort.Slice(b.Rows, func(i, j int) bool { return b.Rows[i].ID < b.Rows[j].ID })
	if e = legacySeal(&b); e != nil {
		return b, e
	}
	return b, nil
}
func decodeLegacyJSON(raw string, v any) error {
	if e := legacyJSONShape(raw); e != nil {
		return e
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	if e := d.Decode(v); e != nil {
		return errors.New("invalid legacy JSON export")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("legacy JSON contains trailing data")
	}
	return nil
}
func legacyScalar(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case json.Number:
		return string(x), nil
	default:
		return "", errors.New("legacy scalar must be a string or JSON number")
	}
}
func legacyIdentifier(v any) (string, error) {
	s, e := legacyScalar(v)
	if e != nil || !legacyID.MatchString(s) {
		return "", errors.New("invalid legacy record/entity identifier")
	}
	return s, nil
}
func legacyNumber(v any, present bool) (*string, string, error) {
	if !present {
		return nil, "missing", nil
	}
	if v == nil {
		return nil, "null", nil
	}
	s, e := legacyScalar(v)
	if e != nil {
		return nil, "", e
	}
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "nan") || strings.EqualFold(s, "-nan") || strings.EqualFold(s, "+nan") || strings.EqualFold(s, "unknown") || s == "U" {
		return nil, "nan", nil
	}
	if len(s) > 256 {
		return nil, "", errors.New("legacy decimal exceeds bound")
	}
	parts := legacyDecimal.FindStringSubmatch(s)
	if parts == nil {
		return nil, "", errors.New("legacy numeric value must be a finite decimal or NaN")
	}
	exponent := 0
	if parts[4] != "" {
		exponent, e = strconv.Atoi(parts[4])
		if e != nil || exponent < -100 || exponent > 100 {
			return nil, "", errors.New("legacy decimal exponent exceeds bound")
		}
	}
	if len(parts[2])+len(parts[3]) > 128 {
		return nil, "", errors.New("legacy decimal precision exceeds128 digits")
	}
	scale := len(parts[3]) - exponent
	if scale < 0 {
		scale = 0
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, "", errors.New("invalid exact legacy decimal")
	}
	out := r.FloatString(scale)
	if strings.Contains(out, ".") {
		out = strings.TrimRight(strings.TrimRight(out, "0"), ".")
	}
	if out == "-0" || out == "+0" {
		out = "0"
	}
	return &out, "", nil
}
func legacySetMeasure(m *LegacyMeasure, name string, v any, present bool) error {
	number, unknown, e := legacyNumber(v, present)
	if e != nil {
		return e
	}
	if m.Unknown == nil {
		m.Unknown = map[string]string{}
	}
	if unknown != "" {
		m.Unknown[name] = unknown
	} else {
		delete(m.Unknown, name)
	}
	switch name {
	case "average":
		m.Average = number
	case "maximum":
		m.Maximum = number
	case "minimum":
		m.Minimum = number
	case "last":
		m.Last = number
	case "total":
		m.Total = number
	default:
		return errors.New("unsupported legacy statistic")
	}
	return nil
}
func legacyDay(v any, loc *time.Location) (string, *time.Time, error) {
	if v == nil {
		return "", nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", nil, errors.New("original day must be a date label")
	}
	date, e := time.Parse("2006-01-02", s)
	if e != nil || date.Format("2006-01-02") != s {
		return "", nil, errors.New("invalid original legacy day")
	}
	start, _, e := legacyCalendarBounds(s, loc)
	if e != nil {
		return s, nil, nil
	} // retain report labels even when that local date was skipped
	return s, &start, nil
}
func legacyMaxAt(m *LegacyMeasure, v any, loc *time.Location) error {
	if v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok || len(s) > 64 {
		return errors.New("legacy maximum timestamp must be bounded text")
	}
	m.OriginalMaxAt = s
	if t, e := time.Parse(time.RFC3339Nano, s); e == nil {
		u := t.UTC()
		m.MaxAt = &u
		return nil
	}
	t, e := time.ParseInLocation("2006-01-02 15:04:05", s, loc)
	if e != nil || t.Format("2006-01-02 15:04:05") != s {
		return errors.New("invalid/nonexistent local legacy maximum timestamp")
	}
	for _, offset := range []time.Duration{-2 * time.Hour, -time.Hour, -30 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour} {
		if t.Add(offset).In(loc).Format("2006-01-02 15:04:05") == s {
			if m.Unknown == nil {
				m.Unknown = map[string]string{}
			}
			m.Unknown["maxAt"] = "ambiguous-local-time"
			return nil
		}
	}
	u := t.UTC()
	m.MaxAt = &u
	return nil
}
func legacyUnits(category string) (string, string, error) {
	switch category {
	case "bits":
		return "bits", "bits/s", nil
	case "pkts", "bcasts":
		return "packets", "packets/s", nil
	case "errs", "discs":
		return "events", "events/s", nil
	}
	return "", "", errors.New("unsupported legacy category; its unit cannot be inferred safely")
}
func legacyTableRows(input LegacyInput, loc *time.Location) ([]LegacyRow, error) {
	s := input.Source
	if s.Table != "traffic_daily" && s.Table != "traffic_daily_phys_ints" && s.Table != "p2p_daily_stats" {
		return nil, errors.New("explicit supported legacy table is required")
	}
	if len(input.Columns) > 0 || s.Unit != "" || s.EntityKind != "" || s.EntityID != "" {
		return nil, errors.New("table exports use their own category/entity columns, not RRD mappings")
	}
	if s.Table == "p2p_daily_stats" && s.Backend != "sflow-rrd" {
		return nil, errors.New("P2P daily source must explicitly declare sflow-rrd")
	}
	if s.Table != "p2p_daily_stats" && s.Backend == "sflow-rrd" {
		return nil, errors.New("interface/member reports require mrtg or rrd source")
	}
	var records []map[string]any
	if e := decodeLegacyJSON(input.Content, &records); e != nil {
		return nil, e
	}
	if len(records) > LegacyMaxRows {
		return nil, errors.New("too many legacy source records")
	}
	out := []LegacyRow{}
	ids := map[string]bool{}
	for _, record := range records {
		id, e := legacyIdentifier(record["id"])
		if e != nil || ids[id] {
			return nil, errors.New("legacy table requires distinct explicit record IDs")
		}
		ids[id] = true
		day, dayAt, e := legacyDay(record["day"], loc)
		if e != nil {
			return nil, e
		}
		allowed := map[string]bool{"id": true, "day": true, "created_at": true, "updated_at": true}
		if s.Table == "p2p_daily_stats" {
			allowed["cust_id"] = true
			allowed["peer_id"] = true
			entity, e := legacyIdentifier(record["cust_id"])
			if e != nil {
				return nil, e
			}
			peer, e := legacyIdentifier(record["peer_id"])
			if e != nil {
				return nil, e
			}
			if dayAt == nil {
				return nil, errors.New("P2P daily rows require their original calendar day")
			}
			for _, family := range []int{4, 6} {
				row := LegacyRow{RecordID: id, SourceKind: "legacy-p2p-daily", EntityKind: "customer", EntityID: entity, PeerEntityID: peer, OriginalDay: day, Category: "bits", Family: family, TotalUnit: "bits", RateUnit: "bits/s", StaffOnly: true, MaxSemantics: "legacy-sum-of-independent-maxima", Notices: []string{"P2P totals are legacy sampled estimates; maxima were summed across independent interface graphs and are not simultaneous peaks.", "The date identifies a requested calendar day; actual RRD sample coverage/NaN conversion cannot be reconstructed from this table."}}
				start, end, e := legacyCalendarBounds(day, loc)
				if e != nil {
					return nil, e
				}
				row.Window = LegacyWindow{Kind: "calendar-day", Period: "calendar-day", Start: &start, End: &end, NominalSeconds: int64(end.Sub(start) / time.Second), BoundaryQuality: "calendar-label"}
				for _, direction := range []string{"in", "out"} {
					measure := &row.In
					if direction == "out" {
						measure = &row.Out
					}
					for _, stat := range []string{"total", "max"} {
						name := fmt.Sprintf("ipv%d_%s_%s", family, stat, direction)
						allowed[name] = true
						kind := stat
						if stat == "max" {
							kind = "maximum"
						}
						v, present := record[name]
						if e = legacySetMeasure(measure, kind, v, present); e != nil {
							return nil, fmt.Errorf("record %s %s: %w", id, name, e)
						}
					}
				}
				row.ID = legacyDigest([]byte(s.Table + "/" + id + "/" + strconv.Itoa(family)))
				out = append(out, row)
			}
		} else {
			allowed["category"] = true
			column := "cust_id"
			kind := "customer"
			if s.Table == "traffic_daily_phys_ints" {
				column = "physicalinterface_id"
				kind = "physical-interface"
			} else {
				allowed["ixp_id"] = true
			}
			allowed[column] = true
			entity, e := legacyIdentifier(record[column])
			if e != nil {
				return nil, e
			}
			category, ok := record["category"].(string)
			if !ok {
				return nil, errors.New("legacy category is required")
			}
			totalUnit, rateUnit, e := legacyUnits(category)
			if e != nil {
				return nil, e
			}
			for _, period := range legacyPeriods {
				row := LegacyRow{ID: legacyDigest([]byte(s.Table + "/" + id + "/" + period)), RecordID: id, SourceKind: "legacy-" + s.Backend + "-window", EntityKind: kind, EntityID: entity, OriginalDay: day, Category: category, TotalUnit: totalUnit, RateUnit: rateUnit, StaffOnly: true, MaxSemantics: "legacy-reported-maximum", Window: LegacyWindow{Kind: "rolling", Period: period, NominalSeconds: legacyPeriodSeconds[period], BoundaryQuality: "unknown"}, Notices: []string{"Original day is the report-save date, not the end of this rolling window. Overlapping window totals must not be added.", "Legacy graph processing could replace unavailable samples with zero; a stored zero cannot now establish complete observed coverage."}}
				for _, direction := range []string{"in", "out"} {
					measure := &row.In
					if direction == "out" {
						measure = &row.Out
					}
					for short, long := range map[string]string{"avg": "average", "max": "maximum", "tot": "total"} {
						name := period + "_" + short + "_" + direction
						allowed[name] = true
						v, present := record[name]
						if e = legacySetMeasure(measure, long, v, present); e != nil {
							return nil, fmt.Errorf("record %s %s: %w", id, name, e)
						}
					}
					if s.Table == "traffic_daily_phys_ints" {
						name := period + "_max_" + direction + "_at"
						allowed[name] = true
						if e = legacyMaxAt(measure, record[name], loc); e != nil {
							return nil, e
						}
					}
				}
				out = append(out, row)
			}
		}
		for name := range record {
			if !allowed[name] {
				return nil, fmt.Errorf("unrecognized column %q in legacy table", name)
			}
		}
		if len(out) > LegacyMaxRows {
			return nil, errors.New("normalized legacy rows exceed20000")
		}
	}
	return out, nil
}
func legacyApplyDeclarations(b *LegacyBatch, input LegacyInput) error {
	windows := map[string]LegacyDeclaredWindow{}
	for _, w := range input.Windows {
		key := w.RecordID + "/" + w.Period
		if !legacyID.MatchString(w.RecordID) || w.Start.IsZero() || !w.End.After(w.Start) || w.Evidence == "" || len(w.Evidence) > 1000 {
			return errors.New("explicit historical window requires valid bounds and evidence")
		}
		if _, ok := windows[key]; ok {
			return errors.New("duplicate declared legacy window")
		}
		windows[key] = w
	}
	used := map[string]bool{}
	for _, owner := range input.Owners {
		if !legacyID.MatchString(owner.EntityID) || !legacyID.MatchString(owner.MemberID) || !legacyHash.MatchString(owner.MemberRevision) || !legacyHash.MatchString(owner.ReferenceSHA256) || owner.ValidFrom.IsZero() || !owner.ValidTo.After(owner.ValidFrom) || strings.TrimSpace(owner.Description) == "" || len(owner.Description) > 1000 {
			return errors.New("historical ownership requires member generation, dated range and evidence checksum")
		}
		switch owner.EntityKind {
		case "customer", "physical-interface", "dataset":
		default:
			return errors.New("invalid historical entity kind")
		}
	}
	for i := range b.Rows {
		row := &b.Rows[i]
		key := row.RecordID + "/" + row.Window.Period
		if w, ok := windows[key]; ok {
			start, end := w.Start.UTC(), w.End.UTC()
			row.Window.Start = &start
			row.Window.End = &end
			row.Window.BoundaryQuality = "declared"
			row.Window.Evidence = w.Evidence
			used[key] = true
		}
		if row.Window.BoundaryQuality != "declared" && row.Window.BoundaryQuality != "export" {
			continue
		}
		for _, owner := range input.Owners {
			if owner.EntityKind != row.EntityKind || owner.EntityID != row.EntityID || row.Window.Start == nil || row.Window.End == nil || owner.ValidFrom.After(*row.Window.Start) || owner.ValidTo.Before(*row.Window.End) {
				continue
			}
			if row.OwnerEvidence != nil {
				return errors.New("ambiguous historical ownership declarations")
			}
			copy := owner
			copy.ValidFrom = copy.ValidFrom.UTC()
			copy.ValidTo = copy.ValidTo.UTC()
			row.OwnerEvidence = &copy
			row.MemberID = owner.MemberID
			row.MemberRevision = owner.MemberRevision
			row.StaffOnly = false
		}
	}
	if len(used) != len(windows) {
		return errors.New("declared window does not match an imported record/period")
	}
	return nil
}
func ValidateLegacyBatch(b LegacyBatch) error {
	if b.Version != 1 || !legacyHash.MatchString(b.ID) || b.ID != b.Checksum || !legacyHash.MatchString(b.InputChecksum) || len(b.Rows) == 0 || len(b.Rows) > LegacyMaxRows {
		return errors.New("invalid legacy batch identity/size")
	}
	loc, e := legacySourceValidate(b.Source)
	if e != nil {
		return e
	}
	raw, e := legacyPayload(b)
	if e != nil || len(raw) > LegacyMaxBatchBytes || legacyDigest(raw) != b.Checksum {
		return errors.New("legacy batch checksum mismatch")
	}
	seen := map[string]bool{}
	for _, r := range b.Rows {
		if !legacyHash.MatchString(r.ID) || seen[r.ID] || !legacyID.MatchString(r.EntityID) || !legacyID.MatchString(r.RecordID) {
			return errors.New("invalid legacy row identity")
		}
		seen[r.ID] = true
		if e := validateLegacySemantics(b, r, loc); e != nil {
			return e
		}
		if !r.StaffOnly && (r.OwnerEvidence == nil || !legacyHash.MatchString(r.MemberRevision) || r.MemberID == "") {
			return errors.New("legacy member access lacks evidence")
		}
		if r.Window.Start != nil && (r.Window.End == nil || !r.Window.End.After(*r.Window.Start)) {
			return errors.New("invalid legacy window")
		}
		for _, m := range []LegacyMeasure{r.In, r.Out} {
			for _, v := range []*string{m.Average, m.Maximum, m.Minimum, m.Last, m.Total} {
				if v != nil {
					normalized, unknown, e := legacyNumber(*v, true)
					if e != nil || unknown != "" || normalized == nil || *normalized != *v {
						return errors.New("invalid normalized legacy decimal")
					}
				}
			}
		}
	}
	return nil
}
func legacyStrictDecode(raw []byte, v any) error {
	if e := legacyJSONShape(string(raw)); e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing legacy JSON")
	}
	return nil
}
