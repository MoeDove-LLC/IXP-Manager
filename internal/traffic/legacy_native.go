package traffic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// RRDReadOptions are trusted local CLI configuration, never fields from a web
// source declaration. Only a private copy below Root is passed to fixed xport
// arguments; no daemon, CDEF, remote URI, or caller-supplied command is accepted.
type RRDReadOptions struct {
	Binary           string
	LibraryDirectory string
	Root             string
	File             string
	InDS             string
	OutDS            string
	Statistics       []string
	From             time.Time
	To               time.Time
	StepSeconds      int64
	Source           LegacySource
}

var legacyDS = regexp.MustCompile(`^[A-Za-z0-9_]{1,19}$`)

type legacyBoundedOutput struct {
	bytes.Buffer
	limit int
}

func (b *legacyBoundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("native RRD output exceeds bound")
	}
	return b.Buffer.Write(p)
}
func legacyRRDEnvironment(library string) ([]string, error) {
	env := []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "TZ=UTC"}
	if library != "" {
		if !filepath.IsAbs(library) || strings.ContainsAny(library, ":\x00\r\n") {
			return nil, errors.New("RRDtool library directory must be an explicit local absolute directory")
		}
		info, e := os.Lstat(library)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return nil, errors.New("RRDtool library directory must be trusted and not writable by others")
		}
		env = append(env, "LD_LIBRARY_PATH="+library)
	}
	return env, nil
}
func ExportLegacyRRD(ctx context.Context, options RRDReadOptions) (LegacyInput, error) {
	var input LegacyInput
	if _, e := legacySourceValidate(options.Source); e != nil {
		return input, e
	}
	if options.Source.Backend != "rrd" && options.Source.Backend != "sflow-rrd" {
		return input, errors.New("native RRD export requires declared rrd or sflow-rrd source")
	}
	if options.Source.Table != "" || !legacyID.MatchString(options.Source.EntityID) {
		return input, errors.New("native RRD export requires a legacy entity/dataset identity")
	}
	switch options.Source.EntityKind {
	case "customer", "physical-interface", "dataset":
	default:
		return input, errors.New("invalid native RRD entity kind")
	}
	switch options.Source.Unit {
	case "bits", "bytes", "packets", "events":
	default:
		return input, errors.New("native RRD export requires its original rate unit")
	}
	if !filepath.IsAbs(options.Binary) {
		return input, errors.New("trusted RRDtool binary must be an explicit absolute path")
	}
	binaryInfo, e := os.Lstat(options.Binary)
	if e != nil || !binaryInfo.Mode().IsRegular() || binaryInfo.Mode().Perm()&0111 == 0 || binaryInfo.Mode().Perm()&0022 != 0 {
		return input, errors.New("RRDtool binary must be a trusted regular executable")
	}
	if !filepath.IsAbs(options.Root) || !filepath.IsLocal(options.File) || options.File == "." {
		return input, errors.New("native RRD source requires an absolute trusted root and relative contained file")
	}
	if info, e := os.Lstat(options.Root); e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return input, errors.New("RRD source root must be a real directory")
	}
	if options.InDS == "" && options.OutDS == "" {
		return input, errors.New("at least one explicitly named RRD data source is required")
	}
	for _, name := range []string{options.InDS, options.OutDS} {
		if name != "" && !legacyDS.MatchString(name) {
			return input, errors.New("RRD data-source names must be1..19 letters, digits or underscores")
		}
	}
	if options.From.IsZero() || !options.To.After(options.From) || options.From.Unix() < 0 || options.To.Unix() > 7258118400 || options.To.Sub(options.From) > 3660*24*time.Hour || options.StepSeconds < 1 || options.StepSeconds > 366*86400 {
		return input, errors.New("native RRD export requires explicit bounded absolute times and step")
	}
	statistics := append([]string(nil), options.Statistics...)
	if len(statistics) == 0 {
		statistics = []string{"average", "maximum"}
	}
	if len(statistics) > 4 {
		return input, errors.New("too many RRD statistics")
	}
	seen := map[string]bool{}
	for _, stat := range statistics {
		switch stat {
		case "average", "maximum", "minimum", "last":
		default:
			return input, errors.New("unsupported native RRD consolidation")
		}
		if seen[stat] {
			return input, errors.New("duplicate native RRD consolidation")
		}
		seen[stat] = true
	}
	env, e := legacyRRDEnvironment(options.LibraryDirectory)
	if e != nil {
		return input, e
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	version := exec.CommandContext(ctx, options.Binary, "--version")
	version.Env = env
	versionOut := &legacyBoundedOutput{limit: 8192}
	versionErr := &legacyBoundedOutput{limit: 4096}
	version.Stdout = versionOut
	version.Stderr = versionErr
	if e = version.Run(); e != nil || !strings.HasPrefix(versionOut.String(), "RRDtool ") {
		return input, errors.New("trusted executable did not identify as RRDtool")
	}
	root, e := os.OpenRoot(options.Root)
	if e != nil {
		return input, e
	}
	defer root.Close()
	source, e := root.OpenFile(options.File, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return input, errors.New("cannot open contained local RRD snapshot")
	}
	defer source.Close()
	before, e := source.Stat()
	if e != nil || !before.Mode().IsRegular() || before.Size() < 16 || before.Size() > 128<<20 {
		return input, errors.New("RRD snapshot must be a regular file of16 bytes..128MiB")
	}
	temporary, e := os.MkdirTemp("", "ixp-legacy-rrd-")
	if e != nil {
		return input, e
	}
	defer os.RemoveAll(temporary)
	copyFile, e := os.OpenFile(filepath.Join(temporary, "source.rrd"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return input, e
	}
	copied, e := io.Copy(copyFile, io.LimitReader(source, (128<<20)+1))
	closeErr := copyFile.Close()
	if e != nil || closeErr != nil || copied != before.Size() {
		return input, errors.New("RRD snapshot changed or exceeded its copy bound")
	}
	after, e := source.Stat()
	if e != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return input, errors.New("RRD snapshot changed while copying; supply an offline snapshot")
	}
	args := []string{"xport", "--json", "--showtime", "--start", strconv.FormatInt(options.From.Unix(), 10), "--end", strconv.FormatInt(options.To.Unix(), 10), "--step", strconv.FormatInt(options.StepSeconds, 10), "--maxrows", strconv.Itoa(LegacyMaxRows)}
	columns := []LegacyColumn{}
	exports := []string{}
	cf := map[string]string{"average": "AVERAGE", "maximum": "MAX", "minimum": "MIN", "last": "LAST"}
	for _, direction := range []struct{ name, ds string }{{"in", options.InDS}, {"out", options.OutDS}} {
		if direction.ds == "" {
			continue
		}
		for _, stat := range statistics {
			index := len(columns)
			alias := "v" + strconv.Itoa(index)
			legend := direction.name + "-" + stat
			args = append(args, fmt.Sprintf("DEF:%s=source.rrd:%s:%s", alias, direction.ds, cf[stat]))
			exports = append(exports, "XPORT:"+alias+":"+legend)
			columns = append(columns, LegacyColumn{Index: index, Legend: legend, Direction: direction.name, Statistic: stat})
		}
	}
	args = append(args, exports...)
	command := exec.CommandContext(ctx, options.Binary, args...)
	command.Dir = temporary
	command.Env = env
	stdout := &legacyBoundedOutput{limit: LegacyMaxInputBytes}
	stderr := &legacyBoundedOutput{limit: 4096}
	command.Stdout = stdout
	command.Stderr = stderr
	if e = command.Run(); e != nil {
		return input, errors.New("offline RRDtool export failed or exceeded its limits")
	}
	input = LegacyInput{Format: LegacyRRDJSON, Source: options.Source, Content: stdout.String(), Columns: columns}
	if _, e = ParseLegacy(input); e != nil {
		return LegacyInput{}, errors.New("RRDtool output did not satisfy the bounded legacy export contract")
	}
	return input, nil
}
