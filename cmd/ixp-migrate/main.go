// ixp-migrate converts an offline, table-oriented IXP Manager JSON export.
// It never connects to a database or applies routing changes.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/moedove/ixp-manager/internal/migrate"
)

const maxInput = 128 << 20

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "ixp-migrate:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("ixp-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "-", "legacy table JSON, or - for stdin")
	output := flags.String("output", "-", "new import JSON, or - for stdout; existing files are never replaced")
	strict := flags.Bool("strict", false, "reject conversion if any data needs manual migration")
	patchBundle := flags.Bool("patch-bundle", false, "write a bounded private patch history/attachment tar.gz bundle instead of JSON")
	patchRoot := flags.String("patch-files-root", "", "explicit offline legacy default-disk root; no configuration or environment files are read")
	logoBundle := flags.Bool("logo-bundle", false, "write a bounded private member-logo JSON bundle")
	logoRoot := flags.String("logo-public-root", "", "explicit offline legacy public root containing logos; no URLs or configuration are read")
	docstoreBundle := flags.Bool("docstore-bundle", false, "write a bounded private legacy Docstore bundle")
	mailingArchive := flags.Bool("mailing-archive", false, "write a private mailing choice archive without restoring subscriptions")
	accountArchive := flags.Bool("account-archive", false, "write private user and customer_to_users source evidence without creating accounts or permissions")
	noteArchive := flags.Bool("note-archive", false, "write a private archive of original notes and note preferences, without restoring grants")
	auditArchive := flags.Bool("audit-archive", false, "write a private historical archive of legacy log metadata")
	auditSource := flags.String("audit-source", "legacy-ixp-manager", "label for the source database's legacy log IDs")
	var diskRoots docstoreDiskRoots
	flags.Var(&diskRoots, "docstore-disk-root", "repeat disk=/explicit/offline/root for each referenced legacy disk")
	resourcesOnly := flags.Bool("resources-only", false, "explicitly export only resource inventory; file/history data require separate bundles")
	peerDrafts := flags.Bool("peer-drafts", false, "explicitly derive disabled peer candidates from reviewed source evidence")
	peerLimitSchema := flags.String("peer-limit-schema", "", "declared source max-prefix columns: shared or split")
	peerAsOf := flags.String("peer-as-of", "", "declared source database calendar date, YYYY-MM-DD, for peer eligibility")
	sourceTimezone := flags.String("source-timezone", "unknown", "declared source datetime timezone (or unknown); DATE fields retain day precision")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	modes := 0
	for _, enabled := range []bool{*patchBundle, *docstoreBundle, *logoBundle, *noteArchive, *mailingArchive, *accountArchive, *auditArchive, *resourcesOnly} {
		if enabled {
			modes++
		}
	}
	if modes > 1 {
		return errors.New("patch, Docstore, logo, note, mailing, account, audit archive and resources-only modes are mutually exclusive")
	}
	peerOptions := peerDraftOptions{Enabled: *peerDrafts, LimitSchema: *peerLimitSchema, AsOf: *peerAsOf}
	if err := peerOptions.validate(); err != nil {
		return err
	}
	if *peerDrafts && (*patchBundle || *docstoreBundle || *logoBundle || *noteArchive || *mailingArchive || *accountArchive || *auditArchive) {
		return errors.New("peer drafts are only available with ordinary inventory or resources-only output")
	}
	if *patchRoot != "" && !*patchBundle {
		return errors.New("--patch-files-root requires --patch-bundle")
	}
	if *logoRoot != "" && !*logoBundle {
		return errors.New("--logo-public-root requires --logo-bundle")
	}
	if len(diskRoots) > 0 && !*docstoreBundle {
		return errors.New("--docstore-disk-root requires --docstore-bundle")
	}
	r := stdin
	if *input != "-" {
		f, err := os.Open(*input)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	// Reject ambiguous keys and malformed Unicode before decoding identities.
	// Reuse the bounded source validator already used by the private archives;
	// errors intentionally do not echo source values or ignored credentials.
	raw, err := io.ReadAll(io.LimitReader(r, maxInput+1))
	if err != nil {
		return errors.New("could not read bounded legacy source export")
	}
	if len(raw) > maxInput {
		return errors.New("input exceeds 128 MiB")
	}
	if err = migrate.ValidateNoteArchiveSourceJSON(raw); err != nil {
		return errors.New("legacy source JSON must be unambiguous valid UTF-8 within structural bounds")
	}
	if *accountArchive {
		var envelope map[string]json.RawMessage
		if json.Unmarshal(raw, &envelope) != nil || len(envelope) != 1 || envelope["tables"] == nil {
			return errors.New("account source requires the exact tables envelope")
		}
	}
	r = bytes.NewReader(raw)
	limited := &io.LimitedReader{R: r, N: maxInput + 1}
	decoder := json.NewDecoder(limited)
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var source legacyExport
	if err := decoder.Decode(&source); err != nil {
		return fmt.Errorf("invalid legacy export: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("expected exactly one JSON document")
	}
	if limited.N <= 0 {
		return errors.New("input exceeds 128 MiB")
	}
	if *accountArchive {
		archive, err := buildAccountArchive(source, *sourceTimezone)
		if err != nil {
			return err
		}
		if *strict {
			return errors.New("strict account archive refused: every source account requires explicit identity and membership review")
		}
		fmt.Fprintln(stderr, "review: account source evidence only; no current accounts or permissions are created; current identity proof and explicit organization mapping are required")
		for _, finding := range migrate.AccountArchiveReview(archive) {
			fmt.Fprintf(stderr, "review: %s %s: %s\n", finding.Entity, finding.LegacyID, finding.Code)
		}
		return migrate.WriteAccountArchive(*output, *archive)
	}
	if *auditArchive {
		if *output == "" || *output == "-" {
			return errors.New("audit archives require an explicit new output file")
		}
		if *strict {
			return errors.New("strict audit archive refused: legacy messages and model snapshots require separate private retention")
		}
		data, err := buildLegacyAuditArchive(source, *auditSource, *sourceTimezone)
		if err != nil {
			return err
		}
		fmt.Fprintln(stderr, "review: legacy audit metadata only; raw messages and model attributes were omitted")
		return writeImportOutput(*output, data, stdout)
	}
	if *mailingArchive {
		archive, err := buildMailingArchive(source)
		if err != nil {
			return err
		}
		if *strict {
			return errors.New("strict mailing archive refused: every source choice requires identity mapping and account review")
		}
		fmt.Fprintln(stderr, "review: mailing choices are source evidence only; explicit identity/list mapping and account review are required")
		return migrate.WriteMailingArchive(*output, *archive)
	}
	if *noteArchive {
		archive, err := buildNoteArchive(source, *sourceTimezone)
		if err != nil {
			return err
		}
		for _, warning := range archive.Warnings {
			fmt.Fprintln(stderr, "review:", warning)
		}
		if *strict && len(archive.Warnings) > 0 {
			return errors.New("strict note archive refused: source values require review; no output written")
		}
		return migrate.WriteNoteArchive(*output, *archive)
	}
	result, err := convertWithPeerDrafts(source, peerOptions)
	if result.Patching != nil {
		if e := migrate.ValidatePatchTimezone(*sourceTimezone); e != nil {
			return e
		}
		result.Patching.SourceTimezone = *sourceTimezone
		if *sourceTimezone == "unknown" {
			result.Warnings = append(result.Warnings, "patching: source datetime timezone is unknown; original values are retained without inventing UTC timestamps")
		}
	}
	if result.Docstore != nil {
		if e := migrate.ValidatePatchTimezone(*sourceTimezone); e != nil {
			return e
		}
		result.Docstore.SourceTimezone = *sourceTimezone
		if *sourceTimezone == "unknown" {
			result.Warnings = append(result.Warnings, "docstore: source timezone is unknown; dates remain source text")
		}
	}
	if result.Docstore != nil && !*docstoreBundle {
		result.Warnings = append(result.Warnings, "Legacy Docstore bytes and source snapshots are excluded from this output; create --docstore-bundle with explicit disk roots separately")
	}
	if len(result.LogoSources) > 0 && !*logoBundle {
		result.Warnings = append(result.Warnings, "Member logo images are excluded; create --logo-bundle --logo-public-root separately, import privately and review publication")
	}
	if *resourcesOnly {
		result.Patching = nil
		result.Docstore = nil
		result.Warnings = append(result.Warnings, "resources-only: file content and legacy archival metadata are explicitly omitted; preserve and import their separate bundles")
	}
	for _, warning := range result.Warnings {
		fmt.Fprintln(stderr, "review:", warning)
	}
	if err != nil {
		return err
	}
	if *strict && len(result.Warnings) != 0 {
		return errors.New("strict conversion refused: manual migration is required; no output written")
	}
	if *docstoreBundle {
		if result.Docstore == nil {
			return errors.New("Docstore bundle requires legacy Docstore records")
		}
		return migrate.WriteDocstoreBundle(*output, map[string]string(diskRoots), migrate.DocstoreBundle{Version: 1, Kind: migrate.DocstoreBundleKind, Source: result.Source, Warnings: result.Warnings, Docstore: *result.Docstore})
	}
	if *logoBundle {
		if *output == "-" || *output == "" {
			return errors.New("logo bundles require an explicit private output file")
		}
		data, e := buildLogoBundle(result.LogoSources, *logoRoot)
		if e != nil {
			return e
		}
		return writeImportOutput(*output, data, stdout)
	}
	if *patchBundle {
		if result.Patching == nil {
			return errors.New("patch bundle requires legacy patch records")
		}
		resources := map[string][]map[string]any{}
		for kind, items := range result.Resources {
			for _, item := range items {
				resources[kind] = append(resources[kind], map[string]any(item))
			}
		}
		return migrate.WritePatchBundle(*output, *patchRoot, migrate.PatchBundle{Version: 1, Kind: migrate.PatchBundleKind, Source: result.Source, Resources: resources, CoreBundles: result.CoreBundles, SFlowReceivers: result.SFlowReceivers, Warnings: result.Warnings, Patching: *result.Patching})
	}
	if result.Patching != nil && len(result.Patching.Files) > 0 {
		return errors.New("attachments require --patch-bundle --patch-files-root and a new --output file")
	}
	if *patchRoot != "" {
		return errors.New("--patch-files-root requires --patch-bundle")
	}
	result.Docstore = nil // Explicit warning above: dedicated archive, never mixed into resource JSON.
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeImportOutput(*output, data, stdout)
}

func writeImportOutput(output string, data []byte, stdout io.Writer) error {
	var err error
	if output == "-" {
		_, err = stdout.Write(data)
		return err
	}
	// Create a complete private temporary file, then link without replacing an
	// existing file. An interrupted write cannot leave a seemingly valid export.
	f, err := os.CreateTemp(filepath.Dir(output), ".ixp-migrate-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Link(f.Name(), output); err != nil {
		return fmt.Errorf("create output without overwrite: %w", err)
	}
	return nil
}
