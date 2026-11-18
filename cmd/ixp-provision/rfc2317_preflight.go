package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/moedove/ixp-manager/internal/provision"
)

type rfc2317LocalFile struct {
	bytes   []byte
	name    string
	parent  syscall.Stat_t
	file    syscall.Stat_t
	present bool
}

// Linux O_PATH is available on the supported Linux kernels but is missing
// from Go's syscall package on amd64. Open a descriptor for inspection first
// so a FIFO, device or link is rejected before an actual read open.
const rfc2317OPath = 0x200000

func sameRFC2317File(a, b syscall.Stat_t) bool {
	return sameRFC2317Inode(a, b) && a.Mode == b.Mode && a.Uid == b.Uid && a.Gid == b.Gid && a.Nlink == b.Nlink &&
		a.Size == b.Size && a.Ctim == b.Ctim && a.Mtim == b.Mtim
}

func sameRFC2317Inode(a, b syscall.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino
}

func (first rfc2317LocalFile) matches(second rfc2317LocalFile) bool {
	return sameRFC2317Inode(first.parent, second.parent) && first.name == second.name && first.present == second.present &&
		(!first.present || sameRFC2317File(first.file, second.file) && bytes.Equal(first.bytes, second.bytes))
}

func openRFC2317Parent(path string) (*os.File, string, syscall.Stat_t, error) {
	var stat syscall.Stat_t
	if !filepath.IsAbs(path) || path == "/" || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return nil, "", stat, errors.New("expected a clean absolute file path")
	}
	parts := strings.Split(path[1:], "/")
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", stat, errors.New("local file root unavailable")
	}
	for _, part := range parts[:len(parts)-1] {
		next, err := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		syscall.Close(fd)
		if err != nil {
			return nil, "", stat, errors.New("parent directory is missing or contains a symlink")
		}
		fd = next
	}
	if err := syscall.Fstat(fd, &stat); err != nil {
		syscall.Close(fd)
		return nil, "", stat, errors.New("cannot prove the local parent directory identity")
	}
	return os.NewFile(uintptr(fd), filepath.Dir(path)), parts[len(parts)-1], stat, nil
}

func readRFC2317LocalFile(path string, limit int64, staticZone bool) (rfc2317LocalFile, error) {
	parent, name, stat, err := openRFC2317Parent(path)
	if err != nil {
		return rfc2317LocalFile{}, err
	}
	defer parent.Close()
	return readRFC2317Leaf(parent, name, stat, limit, staticZone, nil)
}

func readRFC2317Leaf(dir *os.File, name string, stat syscall.Stat_t, limit int64, staticZone bool, check func(syscall.Stat_t) bool) (rfc2317LocalFile, error) {
	result := rfc2317LocalFile{parent: stat, name: name}
	fd := int(dir.Fd())
	flags := rfc2317OPath | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	if staticZone {
		sidecar, err := syscall.Openat(fd, name+".jnl", flags, 0)
		if err == nil {
			syscall.Close(sidecar)
		}
		if !errors.Is(err, syscall.ENOENT) {
			return result, errors.New("a BIND journal sidecar exists or its absence cannot be verified")
		}
	}
	leaf, err := syscall.Openat(fd, name, flags, 0)
	if errors.Is(err, syscall.ENOENT) {
		// This is absence of the leaf under an already verified parent
		// directory. A missing intermediate directory is never absence.
		return result, nil
	}
	if err != nil {
		return result, errors.New("local file unavailable")
	}
	defer syscall.Close(leaf)
	if syscall.Fstat(leaf, &result.file) != nil || result.file.Mode&syscall.S_IFMT != syscall.S_IFREG || result.file.Size < 0 || result.file.Size > limit {
		return result, errors.New("local file must be a bounded regular file without symlinks")
	}
	if check != nil && !check(result.file) {
		return result, errors.New("local file ownership, link count or permissions are unsafe")
	}
	// Resolve the generation we just inspected via its open descriptor,
	// not the mutable directory entry supplied by the operator.
	file, err := os.Open("/proc/self/fd/" + strconv.Itoa(leaf))
	if err != nil {
		return result, errors.New("cannot read the exact verified local file")
	}
	defer file.Close()
	var readStat syscall.Stat_t
	if syscall.Fstat(int(file.Fd()), &readStat) != nil || !sameRFC2317File(result.file, readStat) {
		return result, errors.New("local file changed before reading")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) != result.file.Size || syscall.Fstat(int(file.Fd()), &readStat) != nil || !sameRFC2317File(result.file, readStat) {
		return result, errors.New("local file changed during its bounded read")
	}
	result.bytes, result.present = raw, true
	return result, nil
}

func runRFC2317Preflight(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("rfc2317-preflight", flag.ContinueOnError)
	fs.SetOutput(output)
	f := addRFC2317ArtifactFlags(fs)
	parent := fs.String("parent-file", "", "explicit absolute target parent master file")
	child := fs.String("child-file", "", "explicit absolute target child master file, including its absent leaf for a new child")
	done, err := parse(fs, args)
	if err != nil || done {
		return err
	}
	if *f.path == "" || !validChecksum(*f.checksum) || *f.parent == "" || *f.child == "" || *parent == "" || *child == "" {
		return errors.New("--file, approved --checksum, both expected zones and both target file paths are required")
	}
	bundlePath, err := filepath.Abs(*f.path)
	if err != nil || filepath.Clean(*f.path) != *f.path || *parent == *child || bundlePath == *parent || bundlePath == *child {
		return errors.New("bundle, target parent and target child must use distinct explicit file paths")
	}
	release, err := readRFC2317LocalFile(bundlePath, provision.MaxRFC2317MergeBytes, false)
	if err != nil || !release.present || checksum(string(release.bytes)) != *f.checksum {
		return errors.New("RFC2317 bundle is unavailable, unsafe or differs from the approved download SHA256")
	}
	bundle, err := provision.DecodeRFC2317Merged(release.bytes)
	if err != nil {
		return err
	}
	if err := f.verifyZones(bundle.Review.Input); err != nil {
		return err
	}
	parentFile, err := readRFC2317LocalFile(*parent, provision.MaxRFC2317ParentBytes, true)
	if err != nil {
		return errors.New("target parent: " + err.Error())
	}
	if !parentFile.present {
		return errors.New("target parent file is missing")
	}
	childFile, err := readRFC2317LocalFile(*child, provision.MaxRFC2317ParentBytes, true)
	if err != nil {
		return errors.New("target child: " + err.Error())
	}
	if childFile.present && parentFile.file.Dev == childFile.file.Dev && parentFile.file.Ino == childFile.file.Ino {
		return errors.New("target parent and child resolve to the same file")
	}
	evidence, err := provision.PreflightRFC2317(context.Background(), bundle, parentFile.bytes, childFile.bytes, childFile.present, provision.ValidatorOptions{NamedCheckzone: *f.binary})
	if err != nil {
		return err
	}
	for _, file := range []struct {
		path, label string
		old         rfc2317LocalFile
		limit       int64
		staticZone  bool
	}{
		{bundlePath, "approved bundle", release, provision.MaxRFC2317MergeBytes, false},
		{*parent, "target parent", parentFile, provision.MaxRFC2317ParentBytes, true},
		{*child, "target child", childFile, provision.MaxRFC2317ParentBytes, true},
	} {
		latest, err := readRFC2317LocalFile(file.path, file.limit, file.staticZone)
		if err != nil || !file.old.matches(latest) {
			return errors.New(file.label + " changed during preflight; no target evidence emitted")
		}
	}
	return json.NewEncoder(output).Encode(struct {
		Kind                string `json:"kind"`
		BundleSHA256        string `json:"bundleSHA256"`
		TargetFilesVerified bool   `json:"targetFilesVerified"`
		Activated           bool   `json:"activated"`
		provision.RFC2317TargetEvidence
	}{Kind: "dns-rfc2317-target-preflight", BundleSHA256: *f.checksum, TargetFilesVerified: true, RFC2317TargetEvidence: evidence})
}
