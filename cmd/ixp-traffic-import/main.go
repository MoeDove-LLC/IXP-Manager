package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/traffic"
)

func main() {
	if e := run(os.Args[1:], os.Stdin, os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run(args []string, stdin io.Reader, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: ixp-traffic-import preview|import|list|query|delete|validate|export-rrd [flags]")
	}
	command := args[0]
	fs := flag.NewFlagSet("ixp-traffic-import "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	input := fs.String("input", "", "offline export file, or - for stdin")
	spec := fs.String("spec", "", "complete LegacyInput JSON declaration, including content")
	rrdtool := fs.String("rrdtool", "", "trusted absolute RRDtool binary path")
	library := fs.String("rrd-library-directory", "", "trusted optional native shared-library directory")
	rrdRoot := fs.String("rrd-root", "", "trusted local RRD snapshot directory")
	rrdFile := fs.String("rrd-file", "", "relative RRD snapshot below rrd-root")
	inDS := fs.String("in-ds", "", "explicit incoming RRD data-source name")
	outDS := fs.String("out-ds", "", "explicit outgoing RRD data-source name")
	statistics := fs.String("statistics", "average,maximum", "fixed RRD consolidations: average,maximum,minimum,last")
	from := fs.String("from", "", "absolute RFC3339 time (known-window query or native export)")
	to := fs.String("to", "", "absolute RFC3339 time (known-window query or native export)")
	step := fs.Int64("step", 0, "explicit native RRD export step in seconds")
	format := fs.String("format", traffic.LegacyTableJSON, "ixp-table-json, rrd-xport-json or rrd-xport-xml")
	source := fs.String("source", "", "explicit source description")
	backend := fs.String("backend", "", "mrtg, rrd or sflow-rrd")
	zone := fs.String("timezone", "", "explicit IANA source timezone")
	table := fs.String("table", "", "legacy SQL table name")
	unit := fs.String("unit", "", "explicit RRD export value unit: bits, bytes, packets, events")
	entityKind := fs.String("entity-kind", "", "legacy entity kind")
	entityID := fs.String("entity-id", "", "legacy entity ID")
	declarations := fs.String("declarations", "", "optional private JSON columns/windows/owners declarations")
	storeDir := fs.String("store", "", "independent legacy store directory (offline exclusive writer)")
	checksum := fs.String("checksum", "", "explicit reviewed SHA256 for import/delete")
	batchID := fs.String("batch", "", "immutable batch ID for query/delete")
	period := fs.String("period", "", "original report period")
	category := fs.String("category", "", "original category")
	family := fs.Int("family", 0, "original address family")
	fromDay := fs.String("from-day", "", "original report day label, inclusive")
	toDay := fs.String("to-day", "", "original report day label, inclusive")
	memberID := fs.String("member-id", "", "historically evidenced member ID filter")
	memberRevision := fs.String("member-revision", "", "historical member generation filter")
	offset := fs.Int("offset", 0, "filtered row offset")
	limit := fs.Int("limit", 100, "page size1..1000")
	if e := fs.Parse(args[1:]); e != nil || fs.NArg() != 0 {
		return errors.New("invalid legacy import flags")
	}
	write := func(v any) error {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(v)
	}
	parseTime := func(value string) (*time.Time, error) {
		if value == "" {
			return nil, nil
		}
		parsed, e := time.Parse(time.RFC3339, value)
		if e != nil {
			return nil, errors.New("explicit times must use RFC3339 with an offset")
		}
		return &parsed, nil
	}
	windowFrom, e := parseTime(*from)
	if e != nil {
		return e
	}
	windowTo, e := parseTime(*to)
	if e != nil {
		return e
	}
	if command == "export-rrd" {
		if windowFrom == nil || windowTo == nil || *input != "" || *spec != "" || *storeDir != "" || *checksum != "" || *declarations != "" {
			return errors.New("export-rrd requires explicit from/to and only exports; review/import its JSON separately")
		}
		exported, e := traffic.ExportLegacyRRD(context.Background(), traffic.RRDReadOptions{Binary: *rrdtool, LibraryDirectory: *library, Root: *rrdRoot, File: *rrdFile, InDS: *inDS, OutDS: *outDS, Statistics: strings.Split(*statistics, ","), From: *windowFrom, To: *windowTo, StepSeconds: *step, Source: traffic.LegacySource{Name: *source, Timezone: *zone, Backend: *backend, Unit: *unit, EntityKind: *entityKind, EntityID: *entityID}})
		if e != nil {
			return e
		}
		return write(exported)
	}
	if command == "preview" || command == "import" {
		if (*input == "") == (*spec == "") {
			return errors.New("exactly one of --input or --spec is required")
		}
		var raw []byte
		var e error
		var in traffic.LegacyInput
		if *spec != "" {
			if *source != "" || *backend != "" || *zone != "" || *table != "" || *unit != "" || *entityKind != "" || *entityID != "" || *declarations != "" {
				return errors.New("--spec cannot be combined with separate source/declaration flags")
			}
			raw, e = readExport(*spec, traffic.LegacyMaxBatchBytes)
			if e != nil {
				return e
			}
			d := json.NewDecoder(bytes.NewReader(raw))
			d.DisallowUnknownFields()
			if e = d.Decode(&in); e != nil || d.Decode(new(any)) != io.EOF {
				return errors.New("invalid complete legacy input specification")
			}
		} else {
			if *input == "-" {
				if stdin == nil {
					return errors.New("stdin export is unavailable")
				}
				raw, e = io.ReadAll(io.LimitReader(stdin, traffic.LegacyMaxInputBytes+1))
				if len(raw) > traffic.LegacyMaxInputBytes {
					return errors.New("legacy input exceeds16MiB")
				}
			} else {
				raw, e = readExport(*input, traffic.LegacyMaxInputBytes)
			}
			if e != nil {
				return e
			}
			in = traffic.LegacyInput{Format: *format, Source: traffic.LegacySource{Name: *source, Timezone: *zone, Backend: *backend, Table: *table, Unit: *unit, EntityKind: *entityKind, EntityID: *entityID}, Content: string(raw)}
			if *declarations != "" {
				data, e := readExport(*declarations, 4<<20)
				if e != nil {
					return e
				}
				var declared struct {
					Columns []traffic.LegacyColumn         `json:"columns"`
					Windows []traffic.LegacyDeclaredWindow `json:"windows"`
					Owners  []traffic.LegacyOwnerEvidence  `json:"owners"`
				}
				d := json.NewDecoder(bytes.NewReader(data))
				d.DisallowUnknownFields()
				if e = d.Decode(&declared); e != nil || d.Decode(new(any)) != io.EOF {
					return errors.New("invalid legacy declarations JSON")
				}
				in.Columns = declared.Columns
				in.Windows = declared.Windows
				in.Owners = declared.Owners
			}
		}
		batch, e := traffic.ParseLegacy(in)
		if e != nil {
			return e
		}
		if command == "preview" {
			return write(batch)
		}
		if *checksum == "" || *checksum != batch.Checksum {
			return errors.New("--checksum must match the exact reviewed preview")
		}
		if *storeDir == "" {
			return errors.New("--store is required")
		}
		store, e := traffic.OpenLegacy(*storeDir)
		if e != nil {
			return e
		}
		defer store.Close()
		meta, e := store.Put(batch, *checksum)
		if e != nil {
			return e
		}
		return write(meta)
	}
	if *storeDir == "" {
		return errors.New("--store is required")
	}
	if _, e := os.Lstat(filepath.Join(*storeDir, "legacy-manifest.json")); e != nil {
		return errors.New("an existing legacy store is required")
	}
	if command == "validate" {
		if e := traffic.LegacyValidateFiles(*storeDir); e != nil {
			return e
		}
		files, e := traffic.LegacyListFiles(*storeDir)
		if e != nil {
			return e
		}
		return write(struct {
			Valid bool     `json:"valid"`
			Files []string `json:"files"`
		}{true, files})
	}
	if command != "list" && command != "query" && command != "delete" {
		return errors.New("unsupported legacy command")
	}
	store, e := traffic.OpenLegacy(*storeDir)
	if e != nil {
		return e
	}
	defer store.Close()
	switch command {
	case "list":
		catalog, e := store.List()
		if e != nil {
			return e
		}
		return write(catalog)
	case "query":
		result, e := store.Query(traffic.LegacyQuery{BatchID: *batchID, EntityKind: *entityKind, EntityID: *entityID, OriginalDayFrom: *fromDay, OriginalDayTo: *toDay, WindowFrom: windowFrom, WindowTo: windowTo, Period: *period, Category: *category, Family: *family, MemberID: *memberID, MemberRevision: *memberRevision, Offset: *offset, Limit: *limit})
		if e != nil {
			return e
		}
		return write(result)
	case "delete":
		if e := store.Delete(*batchID, *checksum); e != nil {
			return e
		}
		return write(struct {
			Deleted string `json:"deleted"`
		}{*batchID})
	}
	return errors.New("unsupported legacy command")
}
func readExport(path string, limit int64) ([]byte, error) {
	file, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	defer file.Close()
	info, e := file.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("offline legacy input must be a bounded regular file")
	}
	raw, e := io.ReadAll(io.LimitReader(file, limit+1))
	if e != nil || int64(len(raw)) > limit {
		return nil, errors.New("offline legacy input exceeds its bound")
	}
	return raw, nil
}
