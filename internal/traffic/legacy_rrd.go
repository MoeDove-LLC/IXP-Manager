package traffic

import (
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

type legacyRRDExport struct {
	start, end, step int64
	legends          []string
	rows             []legacyRRDRow
}
type legacyRRDRow struct {
	at     int64
	values []any
}

func legacyRRDInteger(v any) (int64, error) {
	s, e := legacyScalar(v)
	if e != nil {
		return 0, e
	}
	n, e := strconv.ParseInt(s, 10, 64)
	if e != nil || strconv.FormatInt(n, 10) != s {
		return 0, errors.New("RRD metadata requires canonical integer timestamps/counts")
	}
	return n, nil
}
func legacyRRDRows(input LegacyInput, loc *time.Location) ([]LegacyRow, error) {
	source := input.Source
	if source.Backend != "rrd" && source.Backend != "sflow-rrd" {
		return nil, errors.New("RRD export requires explicit rrd or sflow-rrd source")
	}
	if source.Table != "" || !legacyID.MatchString(source.EntityID) {
		return nil, errors.New("RRD export requires its legacy dataset/entity ID, not a SQL table")
	}
	switch source.EntityKind {
	case "customer", "physical-interface", "dataset":
	default:
		return nil, errors.New("RRD export requires an explicit legacy entity kind")
	}
	switch source.Unit {
	case "bits", "bytes", "packets", "events":
	default:
		return nil, errors.New("RRD export requires an explicit original rate unit")
	}
	if len(input.Columns) < 1 || len(input.Columns) > 8 {
		return nil, errors.New("RRD export requires1..8 explicit column mappings")
	}
	var exported legacyRRDExport
	var e error
	if input.Format == LegacyRRDJSON {
		exported, e = legacyRRDJSONExport(input.Content)
	} else {
		exported, e = legacyRRDXMLExport(input.Content)
	}
	if e != nil {
		return nil, e
	}
	if exported.start < 0 || exported.end < exported.start || exported.end > 7258118400 || exported.step < 1 || exported.step > 366*86400 || exported.end-exported.start > int64(LegacyMaxRows)*exported.step || len(exported.rows) == 0 || len(exported.rows) > LegacyMaxRows {
		return nil, errors.New("RRD export time/grid bounds are invalid")
	}
	if (exported.end-exported.start)%exported.step != 0 || int64(len(exported.rows)) != (exported.end-exported.start)/exported.step+1 {
		return nil, errors.New("RRD export row count does not match its explicit grid")
	}
	if len(exported.legends) != len(input.Columns) {
		return nil, errors.New("every RRD export column must have an explicit mapping")
	}
	indices := map[int]bool{}
	slots := map[string]bool{}
	for _, column := range input.Columns {
		if column.Index < 0 || column.Index >= len(exported.legends) || indices[column.Index] || column.Legend != exported.legends[column.Index] || len(column.Legend) > 200 {
			return nil, errors.New("RRD column index/legend mismatch or duplicate")
		}
		indices[column.Index] = true
		if column.Direction != "in" && column.Direction != "out" {
			return nil, errors.New("RRD direction must be in or out")
		}
		switch column.Statistic {
		case "average", "maximum", "minimum", "last":
		default:
			return nil, errors.New("RRD exports contain explicit rates only, not counter totals")
		}
		slot := column.Direction + "/" + column.Statistic
		if slots[slot] {
			return nil, errors.New("RRD columns duplicate the same direction/statistic")
		}
		slots[slot] = true
	}
	rows := make([]LegacyRow, 0, len(exported.rows))
	for i, data := range exported.rows {
		expected := exported.start + int64(i)*exported.step
		if data.at != expected || len(data.values) != len(exported.legends) {
			return nil, errors.New("RRD row timestamp or column count contradicts metadata")
		}
		if data.at < exported.step {
			return nil, errors.New("RRD sample interval precedes Unix epoch")
		}
		start := time.Unix(data.at-exported.step, 0).UTC()
		end := time.Unix(data.at, 0).UTC()
		row := LegacyRow{ID: legacyDigest([]byte("rrd/" + strconv.FormatInt(data.at, 10))), RecordID: strconv.FormatInt(data.at, 10), SourceKind: "legacy-rrd-rate", EntityKind: source.EntityKind, EntityID: source.EntityID, Category: source.Unit, TotalUnit: source.Unit, RateUnit: source.Unit + "/s", StaffOnly: true, MaxSemantics: "RRD-consolidated-rate", Window: LegacyWindow{Kind: "rate-bucket", Period: "step", NominalSeconds: exported.step, Start: &start, End: &end, BoundaryQuality: "export"}, Notices: []string{"RRD xport timestamps are interval ends; these consolidated rate samples are not original counters. Totals are not inferred by integration.", "NaN/null samples remain unknown. This row is not added to any other report window."}}
		if source.Backend == "sflow-rrd" {
			row.Notices = append(row.Notices, "The declared source is sampled sFlow; its rates are estimates, not complete traffic counts.")
		}
		for _, m := range []*LegacyMeasure{&row.In, &row.Out} {
			for _, stat := range []string{"average", "maximum", "minimum", "last", "total"} {
				_ = legacySetMeasure(m, stat, nil, false)
			}
			m.Unknown["total"] = "rate-only-export"
		}
		for _, column := range input.Columns {
			measure := &row.In
			if column.Direction == "out" {
				measure = &row.Out
			}
			if e = legacySetMeasure(measure, column.Statistic, data.values[column.Index], true); e != nil {
				return nil, e
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}
func legacyRRDJSONExport(raw string) (legacyRRDExport, error) {
	var out legacyRRDExport
	var root map[string]any
	if e := decodeLegacyJSON(raw, &root); e != nil {
		return out, e
	}
	for key := range root {
		if key != "about" && key != "meta" && key != "data" {
			return out, errors.New("unsupported RRD JSON field")
		}
	}
	meta, ok := root["meta"].(map[string]any)
	if !ok {
		return out, errors.New("RRD JSON requires meta")
	}
	for key := range meta {
		switch key {
		case "start", "end", "step", "rows", "columns", "legend":
		default:
			return out, errors.New("unsupported RRD JSON metadata")
		}
	}
	var e error
	if out.start, e = legacyRRDInteger(meta["start"]); e != nil {
		return out, e
	}
	if out.end, e = legacyRRDInteger(meta["end"]); e != nil {
		return out, e
	}
	if out.step, e = legacyRRDInteger(meta["step"]); e != nil {
		return out, e
	}
	legends, ok := meta["legend"].([]any)
	if !ok || len(legends) == 0 || len(legends) > 8 {
		return out, errors.New("RRD JSON requires1..8 legends")
	}
	for _, entry := range legends {
		legend, ok := entry.(string)
		if !ok || len(legend) > 200 {
			return out, errors.New("invalid RRD legend")
		}
		out.legends = append(out.legends, legend)
	}
	rows, ok := root["data"].([]any)
	if !ok || len(rows) > LegacyMaxRows {
		return out, errors.New("RRD JSON requires bounded data rows")
	}
	if v, present := meta["rows"]; present {
		n, e := legacyRRDInteger(v)
		if e != nil || n != int64(len(rows)) {
			return out, errors.New("RRD JSON declared row count mismatch")
		}
	}
	if v, present := meta["columns"]; present {
		n, e := legacyRRDInteger(v)
		if e != nil || n != int64(len(legends)) {
			return out, errors.New("RRD JSON declared column count mismatch")
		}
	}
	timestampMode := -1
	for i, entry := range rows {
		values, ok := entry.([]any)
		if !ok {
			return out, errors.New("invalid RRD JSON row")
		}
		mode := len(values) - len(legends)
		if mode < 0 || mode > 1 || timestampMode >= 0 && mode != timestampMode {
			return out, errors.New("mixed or invalid RRD timestamp columns")
		}
		timestampMode = mode
		at := out.start + int64(i)*out.step
		if mode == 1 {
			at, e = legacyRRDInteger(values[0])
			if e != nil {
				return out, e
			}
			values = values[1:]
		}
		out.rows = append(out.rows, legacyRRDRow{at: at, values: values})
	}
	return out, nil
}
func legacyRRDXMLExport(raw string) (legacyRRDExport, error) {
	var out legacyRRDExport
	decoder := xml.NewDecoder(strings.NewReader(raw))
	decoder.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		if !strings.EqualFold(charset, "ISO-8859-1") {
			return nil, errors.New("unsupported RRD XML character encoding")
		}
		data, e := io.ReadAll(io.LimitReader(input, LegacyMaxInputBytes+1))
		if e != nil || len(data) > LegacyMaxInputBytes {
			return nil, errors.New("RRD XML character data exceeds bound")
		}
		var b strings.Builder
		for _, c := range data {
			b.WriteRune(rune(c))
		}
		return strings.NewReader(b.String()), nil
	}
	path := []string{}
	seen := map[string]int{}
	var text strings.Builder
	var row legacyRRDRow
	var rowValues []any
	rowTimestamp := false
	rootSeen := false
	metaRows, metaColumns := -1, -1
	allowed := map[string]bool{"xport": true, "xport/meta": true, "xport/meta/start": true, "xport/meta/end": true, "xport/meta/step": true, "xport/meta/rows": true, "xport/meta/columns": true, "xport/meta/legend": true, "xport/meta/legend/entry": true, "xport/data": true, "xport/data/row": true, "xport/data/row/t": true, "xport/data/row/v": true}
	for {
		token, e := decoder.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return out, errors.New("invalid RRD xport XML")
		}
		switch t := token.(type) {
		case xml.Directive:
			return out, errors.New("RRD XML directives/DTDs are forbidden")
		case xml.ProcInst:
			if t.Target != "xml" || rootSeen {
				return out, errors.New("RRD XML processing instructions are forbidden")
			}
		case xml.StartElement:
			if t.Name.Space != "" || len(t.Attr) != 0 {
				return out, errors.New("RRD XML namespaces/attributes are unsupported")
			}
			path = append(path, t.Name.Local)
			name := strings.Join(path, "/")
			if !allowed[name] || len(path) > 5 {
				return out, errors.New("unsupported RRD xport XML element")
			}
			seen[name]++
			if name == "xport" {
				if rootSeen {
					return out, errors.New("multiple RRD XML roots")
				}
				rootSeen = true
			}
			if name == "xport/data/row" {
				row = legacyRRDRow{}
				rowValues = nil
				rowTimestamp = false
				if len(out.rows) >= LegacyMaxRows {
					return out, errors.New("too many RRD XML rows")
				}
			}
			text.Reset()
		case xml.CharData:
			if len(path) == 0 {
				if strings.TrimSpace(string(t)) != "" {
					return out, errors.New("trailing RRD XML content")
				}
			} else {
				if text.Len()+len(t) > 1024 {
					return out, errors.New("RRD XML scalar exceeds bound")
				}
				text.Write(t)
			}
		case xml.EndElement:
			name := strings.Join(path, "/")
			value := strings.TrimSpace(text.String())
			switch name {
			case "xport/meta/start", "xport/meta/end", "xport/meta/step", "xport/meta/rows", "xport/meta/columns":
				if seen[name] != 1 {
					return out, errors.New("duplicate RRD XML metadata")
				}
				n, e := legacyRRDInteger(value)
				if e != nil {
					return out, e
				}
				switch t.Name.Local {
				case "start":
					out.start = n
				case "end":
					out.end = n
				case "step":
					out.step = n
				case "rows":
					if n < 0 || n > LegacyMaxRows {
						return out, errors.New("RRD XML row count exceeds bound")
					}
					metaRows = int(n)
				case "columns":
					if n < 1 || n > 8 {
						return out, errors.New("RRD XML column count exceeds bound")
					}
					metaColumns = int(n)
				}
			case "xport/meta/legend/entry":
				if len(out.legends) >= 8 || len(value) > 200 {
					return out, errors.New("too many/oversized RRD XML legends")
				}
				out.legends = append(out.legends, value)
			case "xport/data/row/t":
				if rowTimestamp {
					return out, errors.New("duplicate RRD XML sample timestamp")
				}
				at, e := legacyRRDInteger(value)
				if e != nil {
					return out, e
				}
				row.at = at
				rowTimestamp = true
			case "xport/data/row/v":
				if len(rowValues) >= 8 {
					return out, errors.New("too many RRD XML values")
				}
				rowValues = append(rowValues, value)
			case "xport/data/row":
				if !rowTimestamp {
					row.at = out.start + int64(len(out.rows))*out.step
				}
				row.values = rowValues
				out.rows = append(out.rows, row)
			}
			path = path[:len(path)-1]
			text.Reset()
		}
	}
	if !rootSeen || len(path) != 0 || seen["xport/meta"] != 1 || seen["xport/data"] != 1 || seen["xport/meta/start"] != 1 || seen["xport/meta/end"] != 1 || seen["xport/meta/step"] != 1 || metaRows != len(out.rows) || metaColumns != len(out.legends) {
		return out, errors.New("RRD XML metadata/data counts are incomplete or inconsistent")
	}
	return out, nil
}
