package as112ops

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/moedove/ixp-manager/internal/as112"
)

func noSymlinks(path string) error {
	if !cleanAbs(path) {
		return errors.New("invalid private path")
	}
	for p := path; p != "/"; p = filepath.Dir(p) {
		s, e := os.Lstat(p)
		if e != nil {
			return e
		}
		if s.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink in private instance path")
		}
	}
	return nil
}
func privateDir(path string) error {
	if e := noSymlinks(path); e != nil {
		return e
	}
	s, e := os.Stat(path)
	if e != nil {
		return e
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	if !ok || !s.IsDir() || s.Mode().Perm() != 0700 || st.Uid != uint32(os.Geteuid()) {
		return errors.New("instance directory must be owned by this user with mode 0700")
	}
	return nil
}
func privateRead(path string, limit int64) ([]byte, error) {
	if e := noSymlinks(path); e != nil {
		return nil, e
	}
	before, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("private input is not a regular file")
	}
	f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	s, e := f.Stat()
	if e != nil {
		return nil, e
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	if !ok || !os.SameFile(before, s) || !s.Mode().IsRegular() || s.Mode().Perm() != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || s.Size() > limit {
		return nil, errors.New("invalid private regular file")
	}
	return io.ReadAll(io.LimitReader(f, limit+1))
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func writeNew(path string, raw []byte) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(raw)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	return ce
}
func writeAtomic(dir, name string, raw []byte) error {
	f, e := os.CreateTemp(dir, ".write-")
	if e != nil {
		return e
	}
	path := f.Name()
	defer os.Remove(path)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(raw)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(path, filepath.Join(dir, name)); e != nil {
		return e
	}
	return syncDir(dir)
}
func ReadStatus(root string) (Status, error) {
	var s Status
	if e := privateDir(root); e != nil {
		return s, e
	}
	raw, e := privateRead(filepath.Join(root, "status.json"), 64<<10)
	if e != nil {
		return s, e
	}
	if e = json.Unmarshal(raw, &s); e != nil {
		return s, e
	}
	if s.Version != 1 || !validHash(s.PlanChecksum) || s.Sequence > 1000000 || validateBinding(s.Binding) != nil {
		return s, errors.New("invalid persisted AS112 status")
	}
	return s, nil
}
func (c *Controller) persist() error {
	c.state.UpdatedAt = c.now().UTC()
	raw, e := json.MarshalIndent(c.state, "", "  ")
	if e != nil {
		return e
	}
	return writeAtomic(c.plan.Root, "status.json", append(raw, '\n'))
}
func (c *Controller) versionDir() string {
	return filepath.Join(c.plan.Root, "versions", c.plan.Checksum)
}
func (c *Controller) verifyInstalled() error {
	link, e := os.Readlink(filepath.Join(c.plan.Root, "current"))
	if e != nil || link != filepath.Join("versions", c.plan.Checksum) {
		return errors.New("AS112 current pointer differs from approved version")
	}
	return c.verifyVersion(c.versionDir())
}
func (c *Controller) verifyVersion(dir string) error {
	if e := privateDir(dir); e != nil {
		return e
	}
	b, e := as112.Decode(c.plan.DNSBundle)
	if e != nil {
		return e
	}
	want := map[string]string{"active.conf": c.plan.ActiveConfig, "standby.conf": c.plan.StandbyConfig, "bundle.json": c.plan.DNSBundle}
	for _, f := range b.Files {
		want[filepath.Join("dns", f.Path)] = f.Content
	}
	count := 0
	e = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("version contains symlink")
		}
		if d.IsDir() {
			return privateDir(path)
		}
		rel, e := filepath.Rel(dir, path)
		if e != nil {
			return e
		}
		text, ok := want[rel]
		if !ok {
			return fmt.Errorf("unexpected version file")
		}
		raw, e := privateRead(path, 40<<20)
		if e != nil || string(raw) != text {
			return errors.New("installed artifact differs from reviewed bytes")
		}
		count++
		return nil
	})
	if e != nil {
		return e
	}
	if count != len(want) {
		return errors.New("incomplete installed version")
	}
	return nil
}
func (c *Controller) publish(ctxVersion string) error {
	if e := c.verifyVersion(ctxVersion); e != nil {
		return e
	}
	if e := os.Rename(ctxVersion, c.versionDir()); e != nil {
		return e
	}
	if e := syncDir(filepath.Dir(c.versionDir())); e != nil {
		return e
	}
	link := filepath.Join(c.plan.Root, ".current-new")
	if e := os.Symlink(filepath.Join("versions", c.plan.Checksum), link); e != nil {
		return e
	}
	if e := os.Rename(link, filepath.Join(c.plan.Root, "current")); e != nil {
		return e
	}
	return syncDir(c.plan.Root)
}
