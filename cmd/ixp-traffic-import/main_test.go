package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/traffic"
)

func TestLegacyCLIPreviewImportQueryKeepsRollingWindowUnknown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "traffic_daily.json")
	raw := []byte(`[{"id":1,"cust_id":42,"day":"2024-06-01","category":"bits","day_avg_in":"1.25e3","day_avg_out":null,"day_max_in":"2000","day_tot_in":"9007199254740993","week_tot_in":"18014398509481986","month_tot_in":"27021597764222979","year_tot_in":"36028797018963972"}]`)
	if e := os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	base := []string{"--input", path, "--source", "synthetic old MRTG table export", "--backend", "mrtg", "--timezone", "Europe/Dublin", "--table", "traffic_daily"}
	var output bytes.Buffer
	if e := run(append([]string{"preview"}, base...), nil, &output); e != nil {
		t.Fatal(e)
	}
	var batch traffic.LegacyBatch
	if e := json.Unmarshal(output.Bytes(), &batch); e != nil {
		t.Fatal(e)
	}
	if len(batch.Rows) != 4 {
		t.Fatal("missing original rolling periods")
	}
	store := filepath.Join(dir, "legacy")
	output.Reset()
	args := append([]string{"import"}, base...)
	args = append(args, "--store", store, "--checksum", batch.Checksum)
	if e := run(args, nil, &output); e != nil {
		t.Fatal(e)
	}
	output.Reset()
	if e := run([]string{"query", "--store", store, "--batch", batch.ID, "--period", "day"}, nil, &output); e != nil {
		t.Fatal(e)
	}
	var result traffic.LegacyResult
	if e := json.Unmarshal(output.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if !result.NoAggregation || len(result.Rows) != 1 || result.Rows[0].Window.NominalSeconds != 119988 || result.Rows[0].Window.Start != nil || result.Rows[0].Window.End != nil || !result.Rows[0].StaffOnly || result.Rows[0].In.Total == nil || *result.Rows[0].In.Total != "9007199254740993" || result.Rows[0].Out.Average != nil || result.Rows[0].OriginalDay != "2024-06-01" {
		t.Fatalf("legacy semantics changed: %+v", result)
	}
	after, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(after, raw) {
		t.Fatal("offline importer changed original source")
	}
	output.Reset()
	if e := run([]string{"validate", "--store", store}, nil, &output); e != nil || !strings.Contains(output.String(), `"valid": true`) {
		t.Fatalf("read-only store validation: %s %v", output.String(), e)
	}
}
