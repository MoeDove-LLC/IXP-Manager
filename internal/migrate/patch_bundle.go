package migrate

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/core"
)

// PatchBundle is a migration package, never a native state/credential restore.
type PatchBundle struct {
	Version        int                         `json:"version"`
	Kind           string                      `json:"kind"`
	Source         string                      `json:"source"`
	Resources      map[string][]map[string]any `json:"resources"`
	CoreBundles    []core.Bundle               `json:"coreBundles,omitempty"`
	SFlowReceivers []SFlowReceiverImport       `json:"sflowReceivers,omitempty"`
	Warnings       []string                    `json:"warnings"`
	Patching       PatchingImport              `json:"patching"`
}

func validatePatchBundle(b *PatchBundle, content bool) error {
	if b.Version != 1 || b.Kind != PatchBundleKind || b.Source != "ixp-manager-legacy" || b.Resources == nil {
		return errors.New("invalid patch bundle manifest")
	}
	return ValidatePatching(&b.Patching, content)
}

// OpenPatchSource walks fixed hash-derived components using descriptor-relative
// O_NOFOLLOW. Neither an original filename nor a manifest path is opened.
func OpenPatchSource(rootName string, file PatchFileSource) (*os.File, error) {
	rel, err := PatchStoragePath(file.StorageKey)
	if err != nil {
		return nil, err
	}
	current, err := openPatchRoot(rootName)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= syscall.O_DIRECTORY
		}
		next, e := syscall.Openat(int(current.Fd()), part, flags, 0)
		current.Close()
		if e != nil {
			return nil, fmt.Errorf("attachment %s content is missing or unsafe", file.ID)
		}
		current = os.NewFile(uintptr(next), "legacy-attachment")
	}
	st, err := current.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() != file.Size || st.Size() > PatchFileLimit {
		current.Close()
		return nil, fmt.Errorf("attachment %s is not a matching bounded regular file", file.ID)
	}
	if stat, ok := st.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
		current.Close()
		return nil, fmt.Errorf("attachment %s must not be hard-linked", file.ID)
	}
	return current, nil
}

// openPatchRoot also rejects symlinks in ancestors of the explicitly selected
// root. O_NOFOLLOW on the final root component alone does not cover those.
func openPatchRoot(rootName string) (*os.File, error) {
	if rootName == "" {
		return nil, errors.New("an explicit legacy file root is required")
	}
	absolute, err := filepath.Abs(rootName)
	if err != nil {
		return nil, err
	}
	flags := syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	fd, err := syscall.Open(string(filepath.Separator), flags, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), "legacy-root")
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(absolute), string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		next, e := syscall.Openat(int(current.Fd()), part, flags, 0)
		current.Close()
		if e != nil {
			return nil, errors.New("legacy file root must be an explicitly supplied real directory without symlink components")
		}
		current = os.NewFile(uintptr(next), "legacy-root")
	}
	return current, nil
}

const PatchCompressedLimit = PatchBundleLimit + (8 << 20)

type patchBundleLimits struct {
	manifest, file, unpacked, compressed int64
	entries                              int
}

func standardPatchBundleLimits() patchBundleLimits {
	return patchBundleLimits{manifest: PatchManifestLimit, file: PatchFileLimit, unpacked: PatchBundleLimit, compressed: PatchCompressedLimit, entries: PatchEntryLimit}
}
func tarEntryBytes(size int64) int64 { return 512 + ((size+511)/512)*512 }

// WritePatchBundle privately snapshots source files before writing the first
// (complete) manifest. Publication is atomic and never replaces an existing file.
func WritePatchBundle(output, sourceRoot string, bundle PatchBundle) error {
	return writePatchBundle(output, sourceRoot, bundle, standardPatchBundleLimits())
}
func writePatchBundle(output, sourceRoot string, bundle PatchBundle, limits patchBundleLimits) error {
	if output == "" || output == "-" {
		return errors.New("patch bundles require an explicit new output file")
	}
	if err := validatePatchBundle(&bundle, false); err != nil {
		return err
	}
	if len(bundle.Patching.Files)+1 > limits.entries {
		return errors.New("patch bundle exceeds entry limit")
	}
	var declared int64
	for _, file := range bundle.Patching.Files {
		if file.Size > limits.file || file.Size > limits.unpacked-declared {
			return errors.New("patch bundle exceeds file or cumulative limit")
		}
		declared += file.Size
	}
	// Digest calculation belongs to the private snapshot, not the caller's DTO.
	bundle.Patching.Files = append([]PatchFileSource(nil), bundle.Patching.Files...)
	if len(bundle.Patching.Files) > 0 && sourceRoot == "" {
		return errors.New("--patch-files-root is required for attachment bytes")
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		return errors.New("bundle output must not already exist")
	}
	parent, err := os.Open(filepath.Dir(output))
	if err != nil {
		return err
	}
	defer parent.Close()
	stage, err := os.MkdirTemp(filepath.Dir(output), ".patch-migrate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	staged := map[string]string{}
	for i := range bundle.Patching.Files {
		item := &bundle.Patching.Files[i]
		input, e := OpenPatchSource(sourceRoot, *item)
		if e != nil {
			return e
		}
		before, e := input.Stat()
		if e != nil {
			input.Close()
			return e
		}
		out, e := os.CreateTemp(stage, "content-")
		if e != nil {
			input.Close()
			return e
		}
		h := sha256.New()
		n, e := io.Copy(io.MultiWriter(out, h), io.LimitReader(input, limits.file+1))
		after, statErr := input.Stat()
		input.Close()
		if e == nil {
			e = out.Sync()
		}
		closeErr := out.Close()
		if e != nil || closeErr != nil || statErr != nil || n != item.Size || !before.ModTime().Equal(after.ModTime()) || after.Size() != before.Size() {
			return fmt.Errorf("attachment %s changed or could not be copied completely", item.ID)
		}
		if stat, ok := after.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
			return fmt.Errorf("attachment %s became hard-linked during snapshot", item.ID)
		}
		item.Checksum = hex.EncodeToString(h.Sum(nil))
		staged[item.Entry] = out.Name()
	}
	manifest, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	if int64(len(manifest)) > limits.manifest {
		return errors.New("patch manifest exceeds32 MiB")
	}
	unpacked := tarEntryBytes(int64(len(manifest))) + 1024
	for _, f := range bundle.Patching.Files {
		unpacked += tarEntryBytes(f.Size)
	}
	if unpacked > limits.unpacked {
		return errors.New("patch bundle exceeds cumulative unpacked limit")
	}
	outputFile, err := os.CreateTemp(filepath.Dir(output), ".patch-bundle-")
	if err != nil {
		return err
	}
	defer os.Remove(outputFile.Name())
	defer outputFile.Close()
	gz := gzip.NewWriter(outputFile)
	tw := tar.NewWriter(gz)
	header := func(name string, size int64) error {
		return tw.WriteHeader(&tar.Header{Name: name, Size: size, Mode: 0600, Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR})
	}
	if err = header("manifest.json", int64(len(manifest))); err == nil {
		_, err = tw.Write(manifest)
	}
	if err != nil {
		return err
	}
	for _, f := range bundle.Patching.Files {
		if err = header(f.Entry, f.Size); err != nil {
			return err
		}
		input, e := os.Open(staged[f.Entry])
		if e != nil {
			return e
		}
		h := sha256.New()
		n, e := io.Copy(io.MultiWriter(tw, h), input)
		input.Close()
		if e != nil || n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.Checksum {
			return errors.New("private staged content failed integrity check")
		}
	}
	if err = tw.Close(); err != nil {
		return err
	}
	if err = gz.Close(); err != nil {
		return err
	}
	if info, e := outputFile.Stat(); e != nil || info.Size() > limits.compressed {
		return errors.New("patch bundle exceeds compressed size limit")
	}
	if err = outputFile.Sync(); err != nil {
		return err
	}
	if err = outputFile.Close(); err != nil {
		return err
	}
	if err = os.Link(outputFile.Name(), output); err != nil {
		return fmt.Errorf("publish patch bundle without overwrite: %w", err)
	}
	if err = parent.Sync(); err != nil {
		// A complete but non-durable publication is not reported as success.
		// Only undo our own newly linked inode, never a replaced destination.
		stagedInfo, e1 := os.Stat(outputFile.Name())
		publishedInfo, e2 := os.Lstat(output)
		if e1 == nil && e2 == nil && os.SameFile(stagedInfo, publishedInfo) {
			if cleanup := os.Remove(output); cleanup != nil {
				return errors.Join(err, fmt.Errorf("could not remove non-durable publication: %w", cleanup))
			}
			_ = parent.Sync()
		}
		return err
	}
	return nil
}

type patchCounterReader struct {
	io.Reader
	count int64
}

func (r *patchCounterReader) Read(p []byte) (int, error) {
	n, e := r.Reader.Read(p)
	r.count += int64(n)
	return n, e
}

// ReadPatchBundle accepts only the exact manifest-declared regular entries.
// It checks cumulative decompressed bytes, entry count, each digest and size,
// gzip footer/trailing streams and tar padding before returning a usable DTO.
// consume receives provisional content: callers must discard it on ANY error
// and must not commit imported state until this function succeeds.
func ReadPatchBundle(input io.Reader, consume func(PatchFileSource, io.Reader) error) (*PatchBundle, error) {
	return readPatchBundle(input, consume, standardPatchBundleLimits())
}
func readPatchBundle(input io.Reader, consume func(PatchFileSource, io.Reader) error, limits patchBundleLimits) (*PatchBundle, error) {
	compressed := &io.LimitedReader{R: input, N: limits.compressed + 1}
	buffer := bufio.NewReader(compressed)
	gz, err := gzip.NewReader(buffer)
	if err != nil {
		return nil, errors.New("invalid gzip patch bundle")
	}
	defer gz.Close()
	gz.Multistream(false)
	limited := &io.LimitedReader{R: gz, N: limits.unpacked + 1}
	h, err := readPatchTarHeader(limited)
	if err != nil || h.Name != "manifest.json" || h.Size < 1 || h.Size > limits.manifest {
		return nil, errors.New("first bundle entry must be a bounded regular manifest.json")
	}
	raw, err := io.ReadAll(io.LimitReader(limited, h.Size))
	if err != nil || int64(len(raw)) != h.Size {
		return nil, errors.New("could not read complete patch manifest")
	}
	if err = readPatchTarPadding(limited, h.Size); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var bundle PatchBundle
	if err = decoder.Decode(&bundle); err != nil {
		return nil, fmt.Errorf("invalid patch manifest: %w", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("one patch manifest required")
	}
	if err = validatePatchBundle(&bundle, true); err != nil {
		return nil, err
	}
	if len(bundle.Patching.Files)+1 > limits.entries {
		return nil, errors.New("patch bundle exceeds entry limit")
	}
	expected := map[string]PatchFileSource{}
	for _, f := range bundle.Patching.Files {
		if f.Size > limits.file {
			return nil, errors.New("patch attachment exceeds file limit")
		}
		expected[f.Entry] = f
	}
	seen := map[string]bool{"manifest.json": true}
	count := 1
	for {
		h, err = readPatchTarHeader(limited)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		count++
		if count > limits.entries || seen[h.Name] {
			return nil, errors.New("unsafe or duplicate patch bundle entry")
		}
		f, ok := expected[h.Name]
		if !ok || h.Size != f.Size || h.Size > limits.file {
			return nil, errors.New("unexpected or mismatched patch bundle entry")
		}
		seen[h.Name] = true
		hash := sha256.New()
		reader := &patchCounterReader{Reader: io.TeeReader(io.LimitReader(limited, h.Size), hash)}
		if consume == nil {
			_, err = io.Copy(io.Discard, reader)
		} else {
			err = consume(f, reader)
		}
		if err != nil {
			return nil, err
		}
		if reader.count != f.Size || hex.EncodeToString(hash.Sum(nil)) != f.Checksum {
			return nil, fmt.Errorf("attachment %s content integrity mismatch", f.ID)
		}
		if err = readPatchTarPadding(limited, h.Size); err != nil {
			return nil, err
		}
	}
	if len(seen) != len(expected)+1 {
		return nil, errors.New("patch bundle is missing declared attachments")
	}
	padding := make([]byte, 32768)
	for {
		n, e := limited.Read(padding)
		for _, v := range padding[:n] {
			if v != 0 {
				return nil, errors.New("unexpected data after patch tar")
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, errors.New("invalid patch gzip footer")
		}
		if limited.N <= 0 {
			return nil, errors.New("patch bundle exceeds cumulative unpacked limit")
		}
	}
	if limited.N <= 0 {
		return nil, errors.New("patch bundle exceeds cumulative unpacked limit")
	}
	if _, err = buffer.ReadByte(); err != io.EOF {
		return nil, errors.New("trailing or concatenated patch bundle data")
	}
	if compressed.N <= 0 {
		return nil, errors.New("patch bundle exceeds compressed size limit")
	}
	return &bundle, nil
}

// archive/tar transparently consumes GNU long-name and PAX headers in Next.
// Inspect each physical header first so extensions cannot evade entry limits or
// override the manifest-declared path/type. No archive path is ever extracted.
func readPatchTarHeader(reader io.Reader) (*tar.Header, error) {
	var block [512]byte
	if _, err := io.ReadFull(reader, block[:]); err != nil {
		return nil, errors.New("patch tar must contain complete headers and two end blocks")
	}
	if allPatchZero(block[:]) {
		if _, err := io.ReadFull(reader, block[:]); err != nil || !allPatchZero(block[:]) {
			return nil, errors.New("patch tar requires two zero end blocks")
		}
		return nil, io.EOF
	}
	if block[156] != tar.TypeReg && block[156] != tar.TypeRegA {
		return nil, errors.New("patch tar accepts regular files only; extension/link/special headers are forbidden")
	}
	tr := tar.NewReader(bytes.NewReader(block[:]))
	h, err := tr.Next()
	if err != nil {
		return nil, errors.New("invalid patch tar header")
	}
	if (h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA) || h.Linkname != "" || len(h.PAXRecords) > 0 || len(h.Xattrs) > 0 || h.Size < 0 || path.Clean(h.Name) != h.Name || strings.Contains(h.Name, "\\") || strings.HasPrefix(h.Name, "/") {
		return nil, errors.New("unsafe patch tar entry")
	}
	return h, nil
}
func allPatchZero(block []byte) bool {
	for _, b := range block {
		if b != 0 {
			return false
		}
	}
	return true
}
func readPatchTarPadding(reader io.Reader, size int64) error {
	padding := (512 - size%512) % 512
	if padding == 0 {
		return nil
	}
	var block [512]byte
	if _, err := io.ReadFull(reader, block[:padding]); err != nil || !allPatchZero(block[:padding]) {
		return errors.New("invalid patch tar content padding")
	}
	return nil
}
