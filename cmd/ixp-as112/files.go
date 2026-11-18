package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/moedove/ixp-manager/internal/as112ops"
)

func terminationSignal() os.Signal { return syscall.SIGTERM }

func hashString(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func cleanPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("file path required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("file path unavailable")
	}
	for current := filepath.Dir(absolute); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("file parent must exist without symlinks")
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 && owner.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return "", errors.New("file parents must be owned by root or this user and not replaceable by another user")
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return absolute, nil
}
func readPrivate(path string, maximum int64) ([]byte, error) {
	path, err := cleanPath(path)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("private input unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, errors.New("private input metadata unavailable")
	}
	native, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || native.Uid != uint32(os.Geteuid()) || native.Nlink != 1 || info.Size() > maximum {
		return nil, errors.New("input must be an owned 0600 regular file within its size bound")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(raw)) > maximum {
		return nil, errors.New("private input read failed or exceeded its bound")
	}
	return raw, nil
}
func readPlan(path string) (as112ops.Plan, error) {
	raw, err := readPrivate(path, 40<<20)
	if err != nil {
		return as112ops.Plan{}, errors.New("private canonical plan unavailable")
	}
	return as112ops.DecodePlan(raw)
}
func writeNewPrivate(path string, raw []byte) error {
	path, err := cleanPath(path)
	if err != nil {
		return err
	}
	parent := filepath.Dir(path)
	temporary, err := os.CreateTemp(parent, ".as112-plan-")
	if err != nil {
		return errors.New("private output staging unavailable")
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(raw)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("private output could not be persisted")
	}
	// Link publishes complete bytes atomically and refuses every existing leaf,
	// including symlinks. Remove the staging name before reporting completion.
	if err = os.Link(temporary.Name(), path); err != nil {
		return errors.New("output already exists or cannot be published; nothing overwritten")
	}
	if err = os.Remove(temporary.Name()); err != nil {
		return errors.New("output published but staging cleanup failed; inspect before retrying")
	}
	directory, err := os.Open(parent)
	if err != nil {
		return errors.New("output published but directory sync unavailable; inspect before retrying")
	}
	defer directory.Close()
	if directory.Sync() != nil {
		return errors.New("output published but directory sync failed; inspect before retrying")
	}
	return nil
}
