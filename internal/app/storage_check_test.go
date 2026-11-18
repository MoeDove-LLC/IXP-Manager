package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStoreStorageCheckHealthyLeavesBusinessStateUntouched(t *testing.T) {
	c, dir := setupClient(t)
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, name := range []string{"state.json", "security.key", ".lock"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = data
	}
	revision := c.a.store.revision
	result := c.a.store.checkStorage(context.Background())
	want := []string{"binding", "directory", "create", "write", "sync-file", "rename", "readback", "cleanup", "sync-directory", "verify-binding"}
	ids := []string{}
	for _, step := range result.Checks {
		if step.Status != "passed" || step.Code != "" {
			t.Fatalf("unexpected healthy step: %+v", step)
		}
		ids = append(ids, step.ID)
	}
	if result.Status != "ok" || result.CheckedAt.IsZero() || !reflect.DeepEqual(ids, want) {
		t.Fatalf("unexpected healthy result: %+v", result)
	}
	if c.a.store.revision != revision {
		t.Fatal("storage probe committed business state")
	}
	for name, data := range files {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("protected file %s changed: %v", name, err)
		}
	}
	after, err := os.ReadDir(dir)
	if err != nil || len(after) != len(before) {
		t.Fatal("storage probe left scratch entries", err)
	}
	for i := range after {
		if after[i].Name() != before[i].Name() {
			t.Fatal("storage probe changed data-directory entries")
		}
	}
}

func TestStoreStorageCheckRejectsChangedBindings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   string
		change func(*testing.T, *Store, string)
	}{
		{"closed", "store-closed", func(t *testing.T, s *Store, _ string) {
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
		}},
		{"directory replaced with same lock inode", "directory-changed", func(t *testing.T, _ *Store, dir string) {
			moved := moveStorageDirFixture(t, dir)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(filepath.Join(moved, ".lock"), filepath.Join(dir, ".lock")); err != nil {
				t.Fatal(err)
			}
		}},
		{"new root symlink to same directory", "directory-changed", func(t *testing.T, _ *Store, dir string) {
			moved := moveStorageDirFixture(t, dir)
			if err := os.Symlink(moved, dir); err != nil {
				t.Fatal(err)
			}
		}},
		{"lock replaced", "lock-changed", func(t *testing.T, _ *Store, dir string) {
			path := filepath.Join(dir, ".lock")
			if err := os.Rename(path, path+".held"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"lock symlink to held inode", "lock-unsafe", func(t *testing.T, _ *Store, dir string) {
			path := filepath.Join(dir, ".lock")
			if err := os.Rename(path, path+".held"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(".lock.held", path); err != nil {
				t.Fatal(err)
			}
		}},
		{"same inode different mount identity", "directory-changed", func(t *testing.T, s *Store, _ string) {
			if runtime.GOOS != "linux" {
				t.Skip("Linux mount ID")
			}
			s.storageDirectory.mountID++
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := storageCheckFixture(t)
			tc.change(t, s, dir)
			got := s.checkStorage(context.Background())
			if got.Status != "failed" || !reflect.DeepEqual(got.Checks, []storageCheckStage{{"binding", "failed", tc.code}}) {
				t.Fatalf("unsafe binding reached scratch operations: %+v", got)
			}
			body, err := json.Marshal(got)
			if err != nil || bytes.Contains(body, []byte(dir)) {
				t.Fatal("private data path escaped into storage report", err)
			}
			assertNoStorageScratch(t, dir)
		})
	}
}

func TestStoreStorageCheckAllowsStableConfiguredAliasAndRejectsRetarget(t *testing.T) {
	parent := t.TempDir()
	first, replacement := filepath.Join(parent, "real"), filepath.Join(parent, "replacement")
	for _, dir := range []string{first, replacement} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(parent, "configured")
	if err := os.Symlink(first, alias); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(alias)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if got := s.checkStorage(context.Background()); got.Status != "ok" || len(got.Checks) != 10 {
		t.Fatalf("stable configured storage alias was rejected: %+v", got)
	}
	if err := os.Link(filepath.Join(first, ".lock"), filepath.Join(replacement, ".lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacement, alias); err != nil {
		t.Fatal(err)
	}
	got := s.checkStorage(context.Background())
	if got.Status != "failed" || !reflect.DeepEqual(got.Checks, []storageCheckStage{{"binding", "failed", "directory-changed"}}) {
		t.Fatalf("retargeted alias reached scratch despite changed directory: %+v", got)
	}
	assertNoStorageScratch(t, first)
	assertNoStorageScratch(t, replacement)
}

func TestStoreStorageCheckChmodDeniedScratch(t *testing.T) {
	s, dir := storageCheckFixture(t)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	got := s.checkStorage(context.Background())
	if got.Status == "ok" {
		t.Skip("the running UID can create a directory despite mode 0500")
	}
	want := []storageCheckStage{{"binding", "passed", ""}, {"directory", "failed", "scratch-directory-failed"}}
	if got.Status != "failed" || !reflect.DeepEqual(got.Checks, want) {
		t.Fatalf("denied scratch creation did not report its actual failed phase: %+v", got)
	}
	assertNoStorageScratch(t, dir)
}

type observedStorageContext struct {
	context.Context
	observe func()
}

func (ctx observedStorageContext) Err() error {
	ctx.observe()
	return ctx.Context.Err()
}

func TestStoreStorageCheckCancellationCleansOwnedScratch(t *testing.T) {
	s, dir := storageCheckFixture(t)
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := observedStorageContext{Context: base, observe: func() {
		if findStorageScratchFile(t, dir, "pending") != "" {
			if !s.mu.TryLock() {
				t.Fatal("storage retained its Store lock during scratch I/O")
			}
			s.mu.Unlock()
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, entry := range entries {
				if !strings.HasPrefix(entry.Name(), ".storage-check-file-") {
					continue
				}
				info, err := entry.Info()
				found = err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0
			}
			if !found {
				t.Fatal("create stage omitted a private top-level file")
			}
			cancel()
		}
	}}
	got := s.checkStorage(ctx)
	want := []storageCheckStage{{"binding", "passed", ""}, {"directory", "passed", ""}, {"create", "passed", ""}, {"cleanup", "passed", ""}}
	if got.Status != "canceled" || !reflect.DeepEqual(got.Checks, want) {
		t.Fatalf("canceled check failed to remove scratch or recorded steps it did not run: %+v", got)
	}
	assertNoStorageScratch(t, dir)
	if got := s.checkStorage(base); got.Status != "canceled" || len(got.Checks) != 0 {
		t.Fatalf("canceled request touched storage: %+v", got)
	}
}

func TestStoreStorageCheckRechecksBindingAfterScratch(t *testing.T) {
	s, dir := storageCheckFixture(t)
	var moved string
	ctx := observedStorageContext{Context: context.Background(), observe: func() {
		if moved == "" && findStorageScratchFile(t, dir, "committed") != "" {
			moved = moveStorageDirFixture(t, dir)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
		}
	}}
	got := s.checkStorage(ctx)
	if moved == "" || got.Status != "failed" || len(got.Checks) != 10 ||
		got.Checks[7] != (storageCheckStage{"cleanup", "passed", ""}) ||
		got.Checks[9] != (storageCheckStage{"verify-binding", "failed", "directory-changed"}) {
		t.Fatalf("replaced location passed final binding or was not safely cleaned: %+v", got)
	}
	assertNoStorageScratch(t, dir)
	assertNoStorageScratch(t, moved)
}

func TestStoreStorageCheckReportsCleanupFailureWithoutRemovingUnknownFiles(t *testing.T) {
	s, dir := storageCheckFixture(t)
	var otherPath string
	ctx := observedStorageContext{Context: context.Background(), observe: func() {
		if otherPath == "" {
			if scratch := findStorageScratchFile(t, dir, "committed"); scratch != "" {
				otherPath = filepath.Join(scratch, "unexpected")
				if err := os.WriteFile(otherPath, []byte("preserve this file"), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}}
	got := s.checkStorage(ctx)
	if otherPath == "" || got.Status != "failed" || len(got.Checks) != 8 ||
		got.Checks[7] != (storageCheckStage{"cleanup", "failed", "scratch-cleanup-failed"}) {
		t.Fatalf("unexpected cleanup result: %+v", got)
	}
	if got, err := os.ReadFile(otherPath); err != nil || string(got) != "preserve this file" {
		t.Fatal("cleanup removed or changed unknown file", err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(otherPath), "committed")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned file survived partial cleanup", err)
	}
}

func TestStoreStorageCheckReadbackRejectsSubstitutedFIFO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "committed")
	if err := os.WriteFile(path, []byte("owned content"), 0600); err != nil {
		t.Fatal(err)
	}
	owned, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+"-owned"); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	scratch := storageScratch{root: root, fileName: "committed", fileInfo: owned}
	done := make(chan string, 1)
	go func() { done <- scratch.readback([]byte("owned content")) }()
	select {
	case code := <-done:
		if code != "scratch-readback-failed" {
			t.Fatalf("substituted FIFO accepted, returned %q", code)
		}
	case <-time.After(3 * time.Second):
		// Release a blocked reader if O_NONBLOCK regresses.
		if fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = syscall.Close(fd)
		}
		t.Fatal("readback blocked on a substituted FIFO")
	}
}

func storageCheckFixture(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func moveStorageDirFixture(t *testing.T, dir string) string {
	t.Helper()
	moved := dir + "-original"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	return moved
}

func assertNoStorageScratch(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".storage-check-") {
			t.Fatal("storage check left scratch material")
		}
	}
}

func findStorageScratchFile(t *testing.T, dir, filename string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".storage-check-") && entry.IsDir() {
			scratch := filepath.Join(dir, entry.Name())
			if _, err := os.Lstat(filepath.Join(scratch, filename)); err == nil {
				return scratch
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
	}
	return ""
}
