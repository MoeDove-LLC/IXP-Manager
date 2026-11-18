package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type storageDirectoryBinding struct {
	info    os.FileInfo
	mountID uint64
	alias   bool
}

type storageCheckStage struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
}

type storageCheckResult struct {
	Status    string              `json:"status"`
	CheckedAt time.Time           `json:"checkedAt"`
	Checks    []storageCheckStage `json:"checks"`
}

func (result *storageCheckResult) stage(id, code string) bool {
	stage := storageCheckStage{ID: id, Status: "passed"}
	if code != "" {
		stage.Status, stage.Code = "failed", code
		result.Status = "failed"
	}
	result.Checks = append(result.Checks, stage)
	return code == ""
}

func (result *storageCheckResult) checkContext(ctx context.Context) bool {
	if ctx.Err() != nil {
		if result.Status == "ok" {
			result.Status = "canceled"
		}
		return false
	}
	return true
}

func captureStorageDirectory(path string) storageDirectoryBinding {
	root, binding, _ := openStorageDirectory(path)
	if root != nil {
		_ = root.Close()
	}
	return binding
}

func openStorageDirectory(path string) (*os.Root, storageDirectoryBinding, string) {
	path = filepath.Clean(path)
	entry, err := os.Lstat(path)
	if err != nil {
		return nil, storageDirectoryBinding{}, "directory-unavailable"
	}
	alias := entry.Mode()&os.ModeSymlink != 0
	if !entry.IsDir() && !alias {
		return nil, storageDirectoryBinding{}, "directory-unsafe"
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, storageDirectoryBinding{}, "directory-unavailable"
	}
	binding, err := storageRootIdentity(root)
	if err != nil {
		_ = root.Close()
		return nil, storageDirectoryBinding{}, "binding-unavailable"
	}
	if binding.info.Mode().Perm()&0022 != 0 {
		_ = root.Close()
		return nil, storageDirectoryBinding{}, "directory-unsafe"
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(entry, current) || (!alias && !os.SameFile(binding.info, entry)) {
		_ = root.Close()
		return nil, storageDirectoryBinding{}, "directory-changed"
	}
	binding.alias = alias
	return root, binding, ""
}

func storageRootIdentity(root *os.Root) (storageDirectoryBinding, error) {
	dir, err := root.Open(".")
	if err != nil {
		return storageDirectoryBinding{}, err
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil {
		return storageDirectoryBinding{}, err
	}
	mountID, err := storageMountID(dir)
	return storageDirectoryBinding{info: info, mountID: mountID}, err
}

// Linux bind mounts can have the same device and inode but a different mount ID.
func storageMountID(dir *os.File) (uint64, error) {
	if runtime.GOOS != "linux" {
		return 0, nil
	}
	file, err := os.Open(filepath.Join("/proc/self/fdinfo", strconv.FormatUint(uint64(dir.Fd()), 10)))
	if err != nil {
		return 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, 4096))
	for scanner.Scan() {
		value, found := strings.CutPrefix(scanner.Text(), "mnt_id:")
		if !found {
			continue
		}
		id, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err == nil && id != 0 {
			return id, nil
		}
		break
	}
	return 0, errors.New("storage mount identity unavailable")
}

func (s *Store) boundStorageRoot() (*os.Root, uint64, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, 0, "store-closed"
	}
	root, current, code := openStorageDirectory(filepath.Dir(s.path))
	if code != "" {
		return nil, 0, code
	}
	if code = s.verifyStorageBinding(root, current); code != "" {
		_ = root.Close()
		return nil, 0, code
	}
	return root, current.mountID, ""
}

// Call with the Store lock held so that Close cannot release the held lock file.
func (s *Store) verifyStorageBinding(root *os.Root, current storageDirectoryBinding) string {
	if s.storageDirectory.info == nil || s.lock == nil {
		return "binding-unavailable"
	}
	if !os.SameFile(s.storageDirectory.info, current.info) || s.storageDirectory.mountID != current.mountID || s.storageDirectory.alias != current.alias {
		return "directory-changed"
	}
	entry, err := root.Lstat(".lock")
	if err != nil {
		return "lock-unavailable"
	}
	if !entry.Mode().IsRegular() || entry.Mode().Perm()&0022 != 0 {
		return "lock-unsafe"
	}
	held, err := s.lock.Stat()
	if err != nil {
		return "lock-unavailable"
	}
	if !held.Mode().IsRegular() || !os.SameFile(entry, held) {
		return "lock-changed"
	}
	return ""
}

func (s *Store) checkStorage(ctx context.Context) (result storageCheckResult) {
	result = storageCheckResult{Status: "ok", Checks: []storageCheckStage{}}
	defer func() { result.CheckedAt = time.Now().UTC() }()
	if !result.checkContext(ctx) {
		return
	}
	root, mountID, code := s.boundStorageRoot()
	if !result.stage("binding", code) {
		return
	}
	defer root.Close()
	runStorageScratch(ctx, root, mountID, &result)
	if result.Status != "ok" || !result.checkContext(ctx) {
		return
	}
	if err := syncStorageRoot(root); err != nil {
		result.stage("sync-directory", "directory-sync-failed")
		return
	}
	result.stage("sync-directory", "")
	if !result.checkContext(ctx) {
		return
	}
	current, _, code := s.boundStorageRoot()
	if current != nil {
		_ = current.Close()
	}
	result.stage("verify-binding", code)
	result.checkContext(ctx)
	return
}

func syncStorageRoot(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

type storageScratch struct {
	root        *os.Root
	dirInfo     os.FileInfo
	fileInfo    os.FileInfo
	fileName    string
	topFileInfo os.FileInfo
	topFileName string
}

func runStorageScratch(ctx context.Context, root *os.Root, mountID uint64, result *storageCheckResult) {
	if !result.checkContext(ctx) {
		return
	}
	name := ".storage-check-" + token(16)
	if err := root.Mkdir(name, 0700); err != nil {
		result.stage("directory", "scratch-directory-failed")
		return
	}
	scratch := storageScratch{}
	defer func() {
		result.stage("cleanup", scratch.cleanup(root, name))
	}()
	entry, err := root.Lstat(name)
	if err != nil || !entry.IsDir() || entry.Mode().Perm()&0077 != 0 {
		result.stage("directory", "scratch-directory-failed")
		return
	}
	scratch.dirInfo = entry
	scratch.root, err = root.OpenRoot(name)
	if err != nil {
		result.stage("directory", "scratch-directory-failed")
		return
	}
	opened, err := storageRootIdentity(scratch.root)
	if err != nil || !os.SameFile(entry, opened.info) || opened.mountID != mountID {
		result.stage("directory", "scratch-directory-failed")
		return
	}
	result.stage("directory", "")
	scratch.writeAndRead(ctx, root, result)
}

func (scratch *storageScratch) writeAndRead(ctx context.Context, parent *os.Root, result *storageCheckResult) {
	if !result.checkContext(ctx) {
		return
	}
	if !scratch.createTopLevelFile(parent) {
		result.stage("create", "scratch-create-failed")
		return
	}
	const pending, committed = "pending", "committed"
	file, err := scratch.root.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		result.stage("create", "scratch-create-failed")
		return
	}
	defer file.Close()
	scratch.fileName = pending
	scratch.fileInfo, err = file.Stat()
	if err != nil || !scratch.fileInfo.Mode().IsRegular() {
		result.stage("create", "scratch-create-failed")
		return
	}
	result.stage("create", "")
	if !result.checkContext(ctx) {
		return
	}
	payload := []byte(token(32))
	n, err := file.Write(payload)
	if err != nil || n != len(payload) {
		result.stage("write", "scratch-write-failed")
		return
	}
	result.stage("write", "")
	if !result.checkContext(ctx) {
		return
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		result.stage("sync-file", "scratch-sync-failed")
		return
	}
	result.stage("sync-file", "")
	if !result.checkContext(ctx) {
		return
	}
	if _, err := scratch.root.Lstat(committed); !errors.Is(err, os.ErrNotExist) {
		result.stage("rename", "scratch-rename-failed")
		return
	}
	if err := scratch.root.Rename(pending, committed); err != nil {
		result.stage("rename", "scratch-rename-failed")
		return
	}
	scratch.fileName = committed
	result.stage("rename", "")
	if !result.checkContext(ctx) {
		return
	}
	result.stage("readback", scratch.readback(payload))
}

func (scratch *storageScratch) createTopLevelFile(parent *os.Root) bool {
	name := ".storage-check-file-" + token(16)
	file, err := parent.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return false
	}
	scratch.topFileName = name
	scratch.topFileInfo, err = file.Stat()
	closeErr := file.Close()
	return err == nil && scratch.topFileInfo.Mode().IsRegular() && closeErr == nil
}

func (scratch *storageScratch) readback(payload []byte) string {
	file, err := scratch.root.OpenFile(scratch.fileName, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "scratch-readback-failed"
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(scratch.fileInfo, info) {
		return "scratch-readback-failed"
	}
	stored, err := io.ReadAll(io.LimitReader(file, int64(len(payload))+1))
	if err != nil {
		return "scratch-readback-failed"
	}
	if !bytes.Equal(stored, payload) {
		return "scratch-readback-mismatch"
	}
	return ""
}

func (scratch *storageScratch) cleanup(parent *os.Root, name string) string {
	if scratch.root != nil {
		defer scratch.root.Close()
	}
	fileRemoved := removeOwnedStorageFile(scratch.root, scratch.fileName, scratch.fileInfo)
	topRemoved := removeOwnedStorageFile(parent, scratch.topFileName, scratch.topFileInfo)
	entry, err := parent.Lstat(name)
	if err != nil || scratch.dirInfo == nil || !entry.IsDir() || !os.SameFile(scratch.dirInfo, entry) {
		return "scratch-cleanup-failed"
	}
	if err := parent.Remove(name); err != nil || !fileRemoved || !topRemoved {
		return "scratch-cleanup-failed"
	}
	return ""
}

func removeOwnedStorageFile(root *os.Root, name string, owned os.FileInfo) bool {
	if name == "" {
		return true
	}
	entry, err := root.Lstat(name)
	return err == nil && owned != nil && entry.Mode().IsRegular() && os.SameFile(owned, entry) && root.Remove(name) == nil
}
