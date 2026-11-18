package migrate

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// OpenDocstoreSource reads only the exact recorded disk/path beneath an explicit
// offline root. No application configuration, remote disk, or display name is used.
func OpenDocstoreSource(diskRoots map[string]string, file DocstoreFileSource) (*os.File, error) {
	if err := ValidateDocstoreStorageKey(file.StorageKey); err != nil {
		return nil, err
	}
	root, ok := diskRoots[file.Facts["disk"]]
	if !ok || root == "" {
		return nil, fmt.Errorf("document %s requires explicit root for disk %q", file.ID, file.Facts["disk"])
	}
	current, err := openPatchRoot(root)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(file.StorageKey, "/")
	for i, part := range parts {
		flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= syscall.O_DIRECTORY
		}
		next, e := syscall.Openat(int(current.Fd()), part, flags, 0)
		current.Close()
		if e != nil {
			return nil, fmt.Errorf("document %s content is missing or unsafe", file.ID)
		}
		current = os.NewFile(uintptr(next), "legacy-document")
	}
	st, err := current.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() < 0 || st.Size() > DocstoreFileLimit {
		current.Close()
		return nil, fmt.Errorf("document %s is not a bounded regular file", file.ID)
	}
	if stat, ok := st.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
		current.Close()
		return nil, fmt.Errorf("document %s must not be hard-linked", file.ID)
	}
	return current, nil
}

func sameDocstoreSource(before, after os.FileInfo) bool {
	a, aok := before.Sys().(*syscall.Stat_t)
	b, bok := after.Sys().(*syscall.Stat_t)
	return aok && bok && a.Dev == b.Dev && a.Ino == b.Ino && a.Ctim == b.Ctim && a.Mtim == b.Mtim && b.Nlink == 1
}

// WriteDocstoreBundle privately snapshots source files before writing the first
// (complete) manifest. Publication is atomic and never replaces an existing file.
func WriteDocstoreBundle(output string, diskRoots map[string]string, bundle DocstoreBundle) error {
	return writeDocstoreBundle(output, diskRoots, bundle, standardDocstoreBundleLimits())
}
func writeDocstoreBundle(output string, diskRoots map[string]string, bundle DocstoreBundle, limits patchBundleLimits) error {
	if output == "" || output == "-" {
		return errors.New("docstore bundles require an explicit new output file")
	}
	if err := validateDocstoreBundle(&bundle, false); err != nil {
		return err
	}
	if len(bundle.Docstore.Files)+1 > limits.entries {
		return errors.New("docstore bundle exceeds entry limit")
	}
	var declared int64
	for _, file := range bundle.Docstore.Files {
		if file.Size > limits.file || file.Size > limits.unpacked-declared {
			return errors.New("docstore bundle exceeds file or cumulative limit")
		}
		declared += file.Size
	}
	declared = 0
	// Digest calculation belongs to the private snapshot, not the caller's DTO.
	bundle.Docstore.Files = append([]DocstoreFileSource(nil), bundle.Docstore.Files...)
	if len(bundle.Docstore.Files) > 0 && len(diskRoots) == 0 {
		return errors.New("--docstore-disk-root is required for attachment bytes")
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		return errors.New("bundle output must not already exist")
	}
	parent, err := os.Open(filepath.Dir(output))
	if err != nil {
		return err
	}
	defer parent.Close()
	stage, err := os.MkdirTemp(filepath.Dir(output), ".docstore-migrate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	staged := map[string]string{}
	for i := range bundle.Docstore.Files {
		item := &bundle.Docstore.Files[i]
		input, e := OpenDocstoreSource(diskRoots, *item)
		if e != nil {
			return e
		}
		before, e := input.Stat()
		if e != nil {
			input.Close()
			return e
		}
		item.Size = before.Size()
		if item.Size > limits.file || item.Size > limits.unpacked-declared {
			input.Close()
			return errors.New("docstore snapshot exceeds cumulative or file limit")
		}
		declared += item.Size
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
		if e != nil || closeErr != nil || statErr != nil || n != item.Size || !before.ModTime().Equal(after.ModTime()) || after.Size() != before.Size() || !sameDocstoreSource(before, after) {
			return fmt.Errorf("attachment %s changed or could not be copied completely", item.ID)
		}
		if stat, ok := after.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
			return fmt.Errorf("attachment %s became hard-linked during snapshot", item.ID)
		}
		checksum := hex.EncodeToString(h.Sum(nil))
		if item.Checksum != "" && item.Checksum != checksum {
			return fmt.Errorf("document %s supplied checksum mismatch", item.ID)
		}
		if original := strings.ToLower(item.Facts["sha256"]); original != "" && original != checksum {
			return fmt.Errorf("document %s original checksum mismatch", item.ID)
		}
		item.Checksum = checksum
		staged[item.Entry] = out.Name()
	}
	if err := validateDocstoreBundle(&bundle, true); err != nil {
		return err
	}
	manifest, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	if int64(len(manifest)) > limits.manifest {
		return errors.New("docstore manifest exceeds32 MiB")
	}
	unpacked := tarEntryBytes(int64(len(manifest))) + 1024
	for _, f := range bundle.Docstore.Files {
		unpacked += tarEntryBytes(f.Size)
	}
	if unpacked > limits.unpacked {
		return errors.New("docstore bundle exceeds cumulative unpacked limit")
	}
	outputFile, err := os.CreateTemp(filepath.Dir(output), ".docstore-bundle-")
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
	for _, f := range bundle.Docstore.Files {
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
		return errors.New("docstore bundle exceeds compressed size limit")
	}
	if err = outputFile.Sync(); err != nil {
		return err
	}
	if err = outputFile.Close(); err != nil {
		return err
	}
	if err = os.Link(outputFile.Name(), output); err != nil {
		return fmt.Errorf("publish docstore bundle without overwrite: %w", err)
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
