package mailingsync

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func cleanPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("private file path required")
	}
	p, e := filepath.Abs(path)
	if e != nil {
		return "", errors.New("invalid file path")
	}
	for dir := filepath.Dir(p); ; dir = filepath.Dir(dir) {
		info, e := os.Lstat(dir)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("file parents must be real directories")
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 && st.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return "", errors.New("file parent is replaceable by another user")
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	return p, nil
}
func ReadPrivate(path string, limit int64) ([]byte, error) {
	p, e := cleanPath(path)
	if e != nil {
		return nil, e
	}
	f, e := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, errors.New("private file unavailable")
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return nil, errors.New("private file metadata unavailable")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || info.Size() > limit {
		return nil, errors.New("input must be an owned 0600 single-link regular file within its bound")
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, errors.New("private file exceeds bound or could not be read")
	}
	return b, nil
}
func WriteNew(path string, raw []byte) error { return persist(path, raw, false) }
func persist(path string, raw []byte, replace bool) error {
	p, e := cleanPath(path)
	if e != nil {
		return e
	}
	if len(raw) > maxDocument {
		return errors.New("output exceeds bound")
	}
	dir := filepath.Dir(p)
	f, e := os.CreateTemp(dir, ".mailingsync-")
	if e != nil {
		return errors.New("private output staging unavailable")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(raw)
	}
	if e == nil {
		e = f.Sync()
	}
	if ce := f.Close(); e == nil {
		e = ce
	}
	if e != nil {
		return errors.New("private output could not be synced")
	}
	if replace {
		if _, e = ReadPrivate(p, maxDocument); e != nil {
			return e
		}
		e = os.Rename(f.Name(), p)
	} else {
		e = os.Link(f.Name(), p)
		if e == nil {
			e = os.Remove(f.Name())
		}
	}
	if e != nil {
		return errors.New("private output could not be published; inspect existing files")
	}
	d, e := os.Open(dir)
	if e != nil {
		return errors.New("output directory sync unavailable")
	}
	defer d.Close()
	if d.Sync() != nil {
		return errors.New("output directory sync failed")
	}
	return nil
}
func lock(path string) (func(), error) {
	p, e := cleanPath(path + ".lock")
	if e != nil {
		return nil, e
	}
	f, e := os.OpenFile(p, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if e != nil {
		return nil, errors.New("journal lock unavailable")
	}
	info, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, errors.New("journal lock metadata unavailable")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		f.Close()
		return nil, errors.New("journal lock is not private")
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil, errors.New("another synchronizer holds this journal")
	}
	return func() { f.Close() }, nil
}
func LoadConfig(path string) (Config, error) {
	b, e := ReadPrivate(path, 64<<10)
	if e != nil {
		return Config{}, e
	}
	return DecodeConfig(b)
}
func LoadPlan(path string, c Config) (Plan, error) {
	b, e := ReadPrivate(path, maxDocument)
	if e != nil {
		return Plan{}, e
	}
	return DecodePlan(b, c)
}
