package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"unsafe"

	"github.com/moedove/ixp-manager/internal/provision"
)

const maxRFC2317InstallStateBytes = 64 << 10

type rfc2317InstallDir struct {
	file *os.File
	stat syscall.Stat_t
}

type rfc2317InstallTarget struct {
	path, name string
	dir        *rfc2317InstallDir
	limit      int64
	staticZone bool
}

// The locks are on directory inodes so different state names do not permit
// overlapping installs. The maintenance window also excludes writers that do
// not participate in the advisory lock.
type rfc2317InstallFiles struct {
	parent, child, state rfc2317InstallTarget
	dirs                 []*rfc2317InstallDir
}

func safeRFC2317InstallDir(stat syscall.Stat_t) bool {
	return stat.Mode&syscall.S_IFMT == syscall.S_IFDIR && stat.Uid == uint32(os.Geteuid()) && stat.Mode&0022 == 0
}

func openRFC2317InstallTarget(path string) (rfc2317InstallTarget, error) {
	file, name, stat, err := openRFC2317Parent(path)
	if err != nil {
		return rfc2317InstallTarget{}, err
	}
	if !safeRFC2317InstallDir(stat) {
		file.Close()
		return rfc2317InstallTarget{}, errors.New("target directory must be owned by the effective user and not group/other writable")
	}
	return rfc2317InstallTarget{
		path: path, name: name, dir: &rfc2317InstallDir{file: file, stat: stat},
		limit: provision.MaxRFC2317ParentBytes, staticZone: true,
	}, nil
}

func lockRFC2317Files(parent, child string) (*rfc2317InstallFiles, error) {
	if parent == child {
		return nil, errors.New("parent and child must use distinct target paths")
	}
	p, err := openRFC2317InstallTarget(parent)
	if err != nil {
		return nil, fmt.Errorf("target parent: %w", err)
	}
	f := &rfc2317InstallFiles{parent: p, dirs: []*rfc2317InstallDir{p.dir}}
	locked := false
	defer func() {
		if !locked {
			f.Close()
		}
	}()
	c, err := openRFC2317InstallTarget(child)
	if err != nil {
		return nil, fmt.Errorf("target child: %w", err)
	}
	if sameRFC2317Inode(p.dir.stat, c.dir.stat) {
		c.dir.file.Close()
		c.dir = p.dir
		if p.name == c.name || p.name+".jnl" == c.name || c.name+".jnl" == p.name {
			return nil, errors.New("parent and child must not alias a target leaf or BIND journal")
		}
	} else {
		f.dirs = append(f.dirs, c.dir)
		if c.dir.stat.Dev < p.dir.stat.Dev || (c.dir.stat.Dev == p.dir.stat.Dev && c.dir.stat.Ino < p.dir.stat.Ino) {
			f.dirs[0], f.dirs[1] = f.dirs[1], f.dirs[0]
		}
	}
	f.child = c
	stateName := ".moedove-rfc2317-" + checksum(p.name)[:32] + ".json"
	if p.name == stateName || (p.dir == c.dir && c.name == stateName) {
		return nil, errors.New("target path conflicts with the fixed RFC 2317 install state")
	}
	f.state = rfc2317InstallTarget{
		path: filepath.Join(filepath.Dir(p.path), stateName), name: stateName,
		dir: p.dir, limit: maxRFC2317InstallStateBytes,
	}
	for _, dir := range f.dirs {
		err := syscall.Flock(int(dir.file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("another RFC 2317 installation holds a target directory lock")
		}
		if err != nil {
			return nil, fmt.Errorf("cannot lock target directory: %w", err)
		}
	}
	for _, target := range []rfc2317InstallTarget{f.parent, f.child} {
		if _, err := target.directory(); err != nil {
			return nil, err
		}
	}
	locked = true
	return f, nil
}

func (f *rfc2317InstallFiles) Close() {
	for _, dir := range f.dirs {
		_ = dir.file.Close()
		dir.file = nil
	}
	f.dirs = nil
}

func (f *rfc2317InstallFiles) target(path string) (rfc2317InstallTarget, error) {
	switch path {
	case f.parent.path:
		return f.parent, nil
	case f.child.path:
		return f.child, nil
	default:
		return rfc2317InstallTarget{}, errors.New("path is not one of the two locked zone targets")
	}
}

func (t rfc2317InstallTarget) directory() (syscall.Stat_t, error) {
	if t.dir.file == nil {
		return syscall.Stat_t{}, errors.New("target directory lock is closed")
	}
	// Reopen only to verify the absolute path still resolves to the locked
	// directory. Reads and publication use the original descriptor.
	current, _, stat, err := openRFC2317Parent(t.path)
	if err != nil {
		return stat, err
	}
	current.Close()
	if !sameRFC2317Inode(stat, t.dir.stat) || !safeRFC2317InstallDir(stat) {
		return stat, errors.New("target directory identity or permissions changed")
	}
	return stat, nil
}

func (t rfc2317InstallTarget) safeFile(stat syscall.Stat_t) bool {
	return stat.Mode&syscall.S_IFMT == syscall.S_IFREG && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1 &&
		stat.Mode&07022 == 0 && (t.staticZone || stat.Mode&0777 == 0600)
}

func (t rfc2317InstallTarget) read() (rfc2317LocalFile, error) {
	stat, err := t.directory()
	if err != nil {
		return rfc2317LocalFile{}, err
	}
	return readRFC2317Leaf(t.dir.file, t.name, stat, t.limit, t.staticZone, t.safeFile)
}

func (f *rfc2317InstallFiles) Read(path string) (rfc2317LocalFile, error) {
	target, err := f.target(path)
	if err != nil {
		return rfc2317LocalFile{}, err
	}
	return target.read()
}

func (f *rfc2317InstallFiles) Install(path string, expected rfc2317LocalFile, data []byte) error {
	target, err := f.target(path)
	if err != nil {
		return err
	}
	if path == f.parent.path && !expected.present {
		return errors.New("target parent file must exist before installation")
	}
	return target.publish(expected, data)
}

func (f *rfc2317InstallFiles) ReadState() (rfc2317LocalFile, error) {
	return f.state.read()
}

func (f *rfc2317InstallFiles) WriteState(expected rfc2317LocalFile, data []byte) error {
	return f.state.publish(expected, data)
}

// O_TMPFILE keeps the staged inode private and unlinked until publication. An
// absent target gets its only link atomically, so a crash cannot leave a second
// staging link that makes the new leaf look like an untrusted hard link.
const rfc2317Tmpfile = 0x400000 | syscall.O_DIRECTORY
const rfc2317AtSymlinkFollow = 0x400

func linkRFC2317Stage(dir, staged int, name string) error {
	// Linux permits linking an anonymous inode without CAP_DAC_READ_SEARCH
	// through this local descriptor path with AT_SYMLINK_FOLLOW. Only the
	// internally opened staging fd is dereferenced, never a target pathname.
	from, err := syscall.BytePtrFromString("/proc/self/fd/" + strconv.Itoa(staged))
	if err != nil {
		return err
	}
	to, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_LINKAT, uintptr(dir), uintptr(unsafe.Pointer(from)), uintptr(dir), uintptr(unsafe.Pointer(to)), rfc2317AtSymlinkFollow, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func (t rfc2317InstallTarget) publish(expected rfc2317LocalFile, data []byte) error {
	if len(data) == 0 || int64(len(data)) > t.limit || (expected.present && !t.safeFile(expected.file)) {
		return errors.New("install bytes or expected mode, owner and link count are invalid")
	}
	dirStat, err := t.directory()
	if err != nil {
		return err
	}
	if !sameRFC2317Inode(dirStat, expected.parent) || expected.name != t.name {
		return errors.New("expected baseline was read from a different target path")
	}
	dir := int(t.dir.file.Fd())
	fd, err := syscall.Openat(dir, ".", rfc2317Tmpfile|syscall.O_WRONLY|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return fmt.Errorf("cannot stage an anonymous file in the locked directory: %w", err)
	}
	file := os.NewFile(uintptr(fd), "rfc2317-stage")
	defer file.Close()
	if n, err := file.Write(data); err != nil || n != len(data) {
		return errors.New("cannot write the complete staged file")
	}
	var staged syscall.Stat_t
	if syscall.Fstat(fd, &staged) != nil {
		return errors.New("cannot identify the staged file")
	}
	mode := uint32(0600)
	if expected.present {
		mode = expected.file.Mode & 0777
		if staged.Gid != expected.file.Gid && syscall.Fchown(fd, -1, int(expected.file.Gid)) != nil {
			return errors.New("cannot retain the existing file's group")
		}
	}
	if syscall.Fchmod(fd, mode) != nil || file.Sync() != nil || syscall.Fstat(fd, &staged) != nil {
		return errors.New("cannot sync the staged file and its permissions")
	}
	stageName := ""
	if expected.present {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return errors.New("cannot allocate a staging name")
		}
		stageName = ".moedove-rfc2317-stage-" + hex.EncodeToString(random[:])
		if err := linkRFC2317Stage(dir, fd, stageName); err != nil {
			return fmt.Errorf("cannot name the staged replacement: %w", err)
		}
		defer func() {
			if stageName != "" {
				_ = syscall.Unlinkat(dir, stageName)
			}
		}()
		named, err := readRFC2317Leaf(t.dir.file, stageName, dirStat, t.limit, false, t.safeFile)
		if err != nil || !named.present || !sameRFC2317Inode(named.file, staged) || !bytes.Equal(named.bytes, data) {
			return errors.New("the named replacement no longer matches the staged file")
		}
	}
	// The directory lock serializes cooperating writers between this exact
	// baseline read and publication. Rename itself is not a kernel CAS.
	current, err := t.read()
	if err != nil || !expected.matches(current) {
		return errors.New("target baseline changed or cannot be verified before publication")
	}
	if expected.present {
		err = syscall.Renameat(dir, stageName, dir, t.name)
		if err == nil {
			stageName = ""
		}
	} else {
		err = linkRFC2317Stage(dir, fd, t.name)
	}
	if err != nil {
		return fmt.Errorf("target publication failed: %w", err)
	}
	if err := t.dir.file.Sync(); err != nil {
		return errors.New("target was published but directory durability is unverified")
	}
	after, err := t.read()
	if err != nil || !after.present || !sameRFC2317Inode(after.file, staged) || after.file.Mode&0777 != mode || after.file.Gid != staged.Gid || !bytes.Equal(after.bytes, data) {
		return errors.New("target was published but exact candidate readback is unverified")
	}
	return nil
}
