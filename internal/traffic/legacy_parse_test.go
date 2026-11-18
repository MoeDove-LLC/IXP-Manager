package traffic

import (
	"os"
	"strings"
	"testing"
	"time"
)

func legacyTableInput(t *testing.T) LegacyInput {
	t.Helper()
	raw, e := os.ReadFile("testdata/legacy-traffic-daily.json")
	if e != nil {
		t.Fatal(e)
	}
	return LegacyInput{Format: LegacyTableJSON, Source: LegacySource{Name: "synthetic old MRTG export", Timezone: "Europe/Dublin", Backend: "mrtg", Table: "traffic_daily"}, Content: string(raw)}
}
func TestLegacySQLWindowsUnitsAndUnknownValues(t *testing.T) {
	batch, e := ParseLegacy(legacyTableInput(t))
	if e != nil {
		t.Fatal(e)
	}
	if len(batch.Rows) != 4 {
		t.Fatal("old rolling periods lost")
	}
	for _, row := range batch.Rows {
		if !row.StaffOnly || row.MemberID != "" || row.Window.Start != nil || row.Window.End != nil || row.Window.NominalSeconds != legacyPeriodSeconds[row.Window.Period] || row.OriginalDay != "2024-06-01" || row.TotalUnit != "bits" {
			t.Fatalf("inferred false legacy window/owner %+v", row)
		}
		if row.Window.Period == "day" && (row.In.Total == nil || *row.In.Total != "9007199254740993" || row.Out.Maximum != nil || row.Out.Unknown["maximum"] != "nan" || row.Out.Unknown["average"] != "null") {
			t.Fatal("precision/unknown lost", row)
		}
	}
	if e = ValidateLegacyBatch(batch); e != nil {
		t.Fatal(e)
	}
}
func TestLegacyNativeRRDXportJSONAndXMLPreserveRatesAndNaN(t *testing.T) {
	for _, format := range []string{LegacyRRDJSON, LegacyRRDXML} {
		t.Run(format, func(t *testing.T) {
			extension := "json"
			if format == LegacyRRDXML {
				extension = "xml"
			}
			raw, e := os.ReadFile("testdata/legacy-rrd-xport." + extension)
			if e != nil {
				t.Fatal(e)
			}
			input := LegacyInput{Format: format, Source: LegacySource{Name: "RRDtool1.7.2 synthetic GAUGE source", Timezone: "UTC", Backend: "rrd", Unit: "bytes", EntityKind: "dataset", EntityID: "fixture"}, Content: string(raw), Columns: []LegacyColumn{{Index: 0, Legend: "in-average", Direction: "in", Statistic: "average"}, {Index: 1, Legend: "out-average", Direction: "out", Statistic: "average"}}}
			batch, e := ParseLegacy(input)
			if e != nil {
				t.Fatal(e)
			}
			if len(batch.Rows) != 3 {
				t.Fatal("native export row count changed")
			}
			var first *LegacyRow
			for i := range batch.Rows {
				if batch.Rows[i].RecordID == "1704067500" {
					first = &batch.Rows[i]
				}
			}
			if first == nil || first.In.Average == nil || *first.In.Average != "30" || first.Out.Average != nil || first.In.Total != nil || first.OriginalDay != "" || first.Window.Start.Unix() != 1704067200 || first.Window.End.Unix() != 1704067500 || first.Window.BoundaryQuality != "export" || !first.StaffOnly {
				t.Fatalf("native timestamp/unknown/rate semantics %+v", first)
			}
			if e = ValidateLegacyBatch(batch); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestLegacyP2PDayDSTAndIndependentPeaks(t *testing.T) {
	input := LegacyInput{Format: LegacyTableJSON, Source: LegacySource{Name: "synthetic P2P export", Timezone: "Europe/Dublin", Backend: "sflow-rrd", Table: "p2p_daily_stats"}, Content: `[{"id":1,"cust_id":42,"peer_id":43,"day":"2024-03-31","ipv4_total_in":"18446744073709551615","ipv4_total_out":null,"ipv4_max_in":"123","ipv4_max_out":"NaN"}]`}
	batch, e := ParseLegacy(input)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range batch.Rows {
		if row.Window.NominalSeconds != 23*3600 || row.Window.BoundaryQuality != "calendar-label" || row.MaxSemantics != "legacy-sum-of-independent-maxima" || !row.StaffOnly {
			t.Fatalf("P2P false day/peak semantics %+v", row)
		}
	}
}
func TestLegacyOwnerEvidenceNeedsKnownHistoricalBounds(t *testing.T) {
	input := legacyTableInput(t)
	from := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	input.Owners = []LegacyOwnerEvidence{{EntityKind: "customer", EntityID: "42", MemberID: "historical-member", MemberRevision: strings.Repeat("a", 64), ValidFrom: from, ValidTo: to, ReferenceSHA256: strings.Repeat("b", 64), Description: "synthetic contemporaneous ownership register"}}
	batch, e := ParseLegacy(input)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range batch.Rows {
		if !row.StaffOnly {
			t.Fatal("unknown rolling bound authorized historical owner")
		}
	}
	input.Windows = []LegacyDeclaredWindow{{RecordID: "101", Period: "day", Start: time.Date(2024, 5, 30, 0, 0, 0, 0, time.UTC), End: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), Evidence: "synthetic original extraction boundaries"}}
	batch, e = ParseLegacy(input)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range batch.Rows {
		if row.Window.Period == "day" {
			if row.StaffOnly || row.MemberID != "historical-member" || row.MemberRevision != strings.Repeat("a", 64) {
				t.Fatal("covered historical evidence was not retained")
			}
		} else if !row.StaffOnly {
			t.Fatal("evidence leaked into an unknown period")
		}
	}
}

func TestLegacyRejectsAmbiguousInputsAndUnsafeExports(t *testing.T) {
	base := legacyTableInput(t)
	for _, tc := range []struct {
		name   string
		change func(*LegacyInput)
	}{{"missing-timezone", func(in *LegacyInput) { in.Source.Timezone = "" }}, {"environment-timezone", func(in *LegacyInput) { in.Source.Timezone = "Local" }}, {"duplicate-key", func(in *LegacyInput) {
		in.Content = `[{"id":1,"id":2,"cust_id":42,"day":"2024-06-01","category":"bits"}]`
	}}, {"unknown-category", func(in *LegacyInput) { in.Content = `[{"id":1,"cust_id":42,"day":"2024-06-01","category":"invented"}]` }}, {"unknown-column", func(in *LegacyInput) {
		in.Content = `[{"id":1,"cust_id":42,"day":"2024-06-01","category":"bits","password":"not-a-supported-statistic"}]`
	}}, {"infinity", func(in *LegacyInput) {
		in.Content = `[{"id":1,"cust_id":42,"day":"2024-06-01","category":"bits","day_tot_in":"Infinity"}]`
	}}} {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.change(&in)
			if _, e := ParseLegacy(in); e == nil {
				t.Fatal("ambiguous input accepted")
			}
		})
	}
	xmlInput := LegacyInput{Format: LegacyRRDXML, Source: LegacySource{Name: "synthetic XML", Timezone: "UTC", Backend: "rrd", Unit: "bytes", EntityKind: "dataset", EntityID: "source"}, Columns: []LegacyColumn{{Index: 0, Legend: "in", Direction: "in", Statistic: "average"}}, Content: `<!DOCTYPE xport [<!ENTITY x SYSTEM "file:///etc/passwd">]><xport><meta><start>300</start><end>300</end><step>300</step><rows>1</rows><columns>1</columns><legend><entry>in</entry></legend></meta><data><row><t>300</t><v>&x;</v></row></data></xport>`}
	if _, e := ParseLegacy(xmlInput); e == nil {
		t.Fatal("XML DTD/entity accepted")
	}
}

func TestLegacyCalendarMidnightDSTDoesNotShiftReportDay(t *testing.T) {
	input := LegacyInput{Format: LegacyTableJSON, Source: LegacySource{Name: "synthetic midnight DST report", Timezone: "America/Sao_Paulo", Backend: "sflow-rrd", Table: "p2p_daily_stats"}, Content: `[{"id":1,"cust_id":42,"peer_id":43,"day":"2018-11-04","ipv4_total_in":"1"}]`}
	batch, e := ParseLegacy(input)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range batch.Rows {
		if row.OriginalDay != "2018-11-04" || row.Window.NominalSeconds != 23*3600 {
			t.Fatalf("midnight DST changed reported day/length: %+v", row.Window)
		}
	}
}
