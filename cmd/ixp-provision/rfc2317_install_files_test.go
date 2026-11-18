package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func lockedRFC2317Fixture(t *testing.T, parent, child string) *rfc2317InstallFiles {
	t.Helper()
	files, err := lockRFC2317Files(parent, child)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(files.Close)
	return files
}

func TestRFC2317InstallFilesPublishOwnedPair(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new child"
		if existing {
			name = "existing child"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newRFC2317TargetFixture(t, existing)
			if err := os.Chmod(fixture.parent, 0640); err != nil {
				t.Fatal(err)
			}
			if existing {
				if err := os.Chmod(fixture.child, 0644); err != nil {
					t.Fatal(err)
				}
			}
			files := lockedRFC2317Fixture(t, fixture.parent, fixture.child)
			if len(files.dirs) != 1 {
				t.Fatal("same directory did not use one inode lock")
			}
			for _, target := range []struct {
				path           string
				before         []byte
				candidate      string
				wantPermission uint32
			}{
				{fixture.child, fixture.childBytes, fixture.bundle.Files[0].Content, 0600},
				{fixture.parent, fixture.parentBytes, fixture.bundle.Files[1].Content, 0640},
			} {
				baseline, err := files.Read(target.path)
				if err != nil || baseline.present != (target.before != nil) || !bytes.Equal(baseline.bytes, target.before) {
					t.Fatal("initial target did not match fixture", err)
				}
				if target.path == fixture.child && existing {
					target.wantPermission = 0644
				}
				if err := files.Install(target.path, baseline, []byte(target.candidate)); err != nil {
					t.Fatal(err)
				}
				observed, err := files.Read(target.path)
				if err != nil || !observed.present || sameRFC2317Inode(observed.file, baseline.file) || observed.file.Nlink != 1 ||
					observed.file.Uid != uint32(os.Geteuid()) || observed.file.Mode&0777 != target.wantPermission ||
					(baseline.present && observed.file.Gid != baseline.file.Gid) || string(observed.bytes) != target.candidate {
					t.Fatal("published file did not match ownership, bytes, mode and inode", err)
				}
			}
			entries, err := os.ReadDir(fixture.dir)
			if err != nil || len(entries) != 3 {
				t.Fatal("temporary staging names leaked into the target directory", err)
			}
		})
	}
}

func TestRFC2317InstallFilesStateHasPrivateFixedCAS(t *testing.T) {
	fixture := newRFC2317TargetFixture(t, false)
	files := lockedRFC2317Fixture(t, fixture.parent, fixture.child)
	wantPath := filepath.Join(fixture.dir, ".moedove-rfc2317-"+checksum("parent.zone")[:32]+".json")
	if files.state.path != wantPath {
		t.Fatal("state name was not derived from the parent leaf")
	}
	if _, err := files.Read(wantPath); err == nil {
		t.Fatal("zone access accepted the state path")
	}
	state, err := files.ReadState()
	if err != nil || state.present {
		t.Fatal("new state was not absent", err)
	}
	if err := files.Install(fixture.child, state, []byte("other absent path")); err == nil {
		t.Fatal("absence of another leaf was accepted as the child baseline")
	}
	if err := files.WriteState(state, []byte(`{"phase":"start"}`)); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteState(state, []byte(`{"phase":"stale"}`)); err == nil {
		t.Fatal("old absent expectation replaced state")
	}
	state, err = files.ReadState()
	if err != nil || !state.present || state.file.Mode&0777 != 0600 || string(state.bytes) != `{"phase":"start"}` {
		t.Fatal("initial private state did not read back", err)
	}
	if err := files.WriteState(state, make([]byte, maxRFC2317InstallStateBytes+1)); err == nil {
		t.Fatal("oversized state was published")
	}
	if err := files.WriteState(state, []byte(`{"phase":"next"}`)); err != nil {
		t.Fatal(err)
	}
	observed, err := files.ReadState()
	if err != nil || string(observed.bytes) != `{"phase":"next"}` || sameRFC2317Inode(state.file, observed.file) {
		t.Fatal("state replacement did not read back", err)
	}
	if err := os.Chmod(wantPath, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := files.ReadState(); err == nil {
		t.Fatal("state mode was accepted without private permissions")
	}
}

func TestRFC2317InstallFilesRejectStaleAndMissingBaselines(t *testing.T) {
	fixture := newRFC2317TargetFixture(t, false)
	files := lockedRFC2317Fixture(t, fixture.parent, fixture.child)
	parent, err := files.Read(fixture.parent)
	if err != nil {
		t.Fatal(err)
	}
	child, err := files.Read(fixture.child)
	if err != nil || child.present {
		t.Fatal("child was not initially absent", err)
	}
	writeRFC2317TestFile(t, fixture.child, []byte("new occupant"))
	if err := files.Install(fixture.child, child, []byte("replacement")); err == nil {
		t.Fatal("an expected-absent child was overwritten")
	}
	got, err := os.ReadFile(fixture.child)
	if err != nil || string(got) != "new occupant" {
		t.Fatal("existing child did not survive stale baseline", err)
	}
	if err := os.Rename(fixture.parent, fixture.parent+".old"); err != nil {
		t.Fatal(err)
	}
	writeRFC2317TestFile(t, fixture.parent, fixture.parentBytes)
	if err := files.Install(fixture.parent, parent, []byte("replacement")); err == nil {
		t.Fatal("same bytes in a new inode erased the captured identity")
	}
	if err := os.Remove(fixture.parent); err != nil {
		t.Fatal(err)
	}
	missing, err := files.Read(fixture.parent)
	if err != nil || missing.present {
		t.Fatal("missing parent leaf was not distinguished", err)
	}
	if err := files.Install(fixture.parent, missing, []byte("bootstrap parent")); err == nil {
		t.Fatal("missing parent was created")
	}
	if _, err := os.Stat(fixture.parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing parent path unexpectedly exists", err)
	}
}

func TestRFC2317InstallFilesKernelLinkDoesNotClobber(t *testing.T) {
	fixture := newRFC2317TargetFixture(t, true)
	files := lockedRFC2317Fixture(t, fixture.parent, fixture.child)
	dir := int(files.child.dir.file.Fd())
	stage, err := syscall.Openat(dir, ".", rfc2317Tmpfile|syscall.O_WRONLY|syscall.O_CLOEXEC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(stage)
	if err := linkRFC2317Stage(dir, stage, files.child.name); !errors.Is(err, syscall.EEXIST) {
		t.Fatal("kernel did not refuse the occupied leaf", err)
	}
	got, err := files.Read(fixture.child)
	if err != nil || !bytes.Equal(got.bytes, fixture.childBytes) || got.file.Nlink != 1 {
		t.Fatal("occupied child changed during the rejected link", err)
	}
}

func TestRFC2317InstallFilesRejectUnsafeLeavesAndJournals(t *testing.T) {
	for _, test := range []struct {
		name   string
		create func(*testing.T, targetFixture) error
	}{
		{"symlink", func(t *testing.T, f targetFixture) error { return os.Symlink("missing", f.child) }},
		{"fifo", func(t *testing.T, f targetFixture) error { return syscall.Mkfifo(f.child, 0600) }},
		{"hard link", func(t *testing.T, f targetFixture) error { return os.Link(f.parent, f.child) }},
		{"dependent journal", func(t *testing.T, f targetFixture) error { return os.Symlink("missing", f.child+".jnl") }},
		{"group writable", func(t *testing.T, f targetFixture) error {
			writeRFC2317TestFile(t, f.child, []byte("other writer"))
			return os.Chmod(f.child, 0660)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRFC2317TargetFixture(t, false)
			files := lockedRFC2317Fixture(t, fixture.parent, fixture.child)
			absent, err := files.Read(fixture.child)
			if err != nil || absent.present {
				t.Fatal("initial child was not absent", err)
			}
			if err := test.create(t, fixture); err != nil {
				t.Fatal(err)
			}
			if _, err := files.Read(fixture.child); err == nil {
				t.Fatal("unsafe target was treated as an existing or absent safe baseline")
			}
			if err := files.Install(fixture.child, absent, []byte("overwrite")); err == nil {
				t.Fatal("unsafe target was replaced")
			}
		})
	}
}

func TestRFC2317InstallFilesDirectoryIdentityAndPathRules(t *testing.T) {
	base := t.TempDir()
	targetDir := filepath.Join(base, "zones")
	if err := os.Mkdir(targetDir, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(targetDir, alias); err != nil {
		t.Fatal(err)
	}
	parent, child := filepath.Join(targetDir, "parent.zone"), filepath.Join(targetDir, "child.zone")
	for _, other := range []string{parent, parent + ".jnl", filepath.Join(alias, "child.zone"), filepath.Join(base, "missing", "child.zone")} {
		if files, err := lockRFC2317Files(parent, other); err == nil {
			files.Close()
			t.Fatal("invalid directory path or conflicting target accepted")
		}
	}
	if err := os.Chmod(targetDir, 0770); err != nil {
		t.Fatal(err)
	}
	if files, err := lockRFC2317Files(parent, child); err == nil {
		files.Close()
		t.Fatal("group-writable directory accepted")
	}
	if err := os.Chmod(targetDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeRFC2317TestFile(t, parent, []byte("same baseline bytes"))
	files := lockedRFC2317Fixture(t, parent, child)
	baseline, err := files.Read(parent)
	if err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(base, "old-zones")
	if err := os.Rename(targetDir, oldDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(targetDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeRFC2317TestFile(t, parent, baseline.bytes)
	if _, err := files.Read(parent); err == nil {
		t.Fatal("redirection to a replacement directory was missed")
	}
	if err := files.Install(parent, baseline, []byte("changed")); err == nil {
		t.Fatal("publication proceeded after directory redirection")
	}
	for _, path := range []string{parent, filepath.Join(oldDir, "parent.zone")} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, baseline.bytes) {
			t.Fatal("publication modified the renamed or replacement directory", err)
		}
	}
}

func TestRFC2317InstallFilesLocksOverlappingAndReorderedTargets(t *testing.T) {
	first, second, third := t.TempDir(), t.TempDir(), t.TempDir()
	parent, child := filepath.Join(first, "parent.zone"), filepath.Join(second, "child.zone")
	start := make(chan struct{})
	type result struct {
		files *rfc2317InstallFiles
		err   error
	}
	results := make(chan result, 2)
	for _, paths := range [][2]string{{parent, child}, {child, parent}} {
		go func() {
			<-start
			files, err := lockRFC2317Files(paths[0], paths[1])
			results <- result{files, err}
		}()
	}
	close(start)
	var holder *rfc2317InstallFiles
	busy := 0
	for range 2 {
		select {
		case got := <-results:
			if got.err != nil {
				if !strings.Contains(got.err.Error(), "directory lock") {
					t.Fatal(got.err)
				}
				busy++
				continue
			}
			t.Cleanup(got.files.Close)
			holder = got.files
		case <-time.After(5 * time.Second):
			t.Fatal("reordered acquisitions blocked")
		}
	}
	if busy != 1 || holder == nil {
		t.Fatal("reordered acquisitions did not choose exactly one claimant")
	}
	if files, err := lockRFC2317Files(filepath.Join(third, "other-parent.zone"), filepath.Join(second, "other-child.zone")); err == nil {
		files.Close()
		t.Fatal("different paths and state name bypassed the shared directory lock")
	}
	holder.Close()
	lockedRFC2317Fixture(t, child, parent)
}

func TestRFC2317InstallFilesDirectoryLockAcrossProcesses(t *testing.T) {
	const modeVar = "MOEDOVE_TEST_RFC2317_LOCK_PROCESS"
	const parentVar = "MOEDOVE_TEST_RFC2317_LOCK_PARENT"
	const childVar = "MOEDOVE_TEST_RFC2317_LOCK_CHILD"
	if mode := os.Getenv(modeVar); mode != "" {
		files, err := lockRFC2317Files(os.Getenv(parentVar), os.Getenv(childVar))
		if files != nil {
			files.Close()
		}
		if mode == "busy" && (err == nil || !strings.Contains(err.Error(), "directory lock")) || mode == "free" && err != nil {
			t.Fatalf("another process observed the wrong lock state for %s: %v", mode, err)
		}
		return
	}
	first, second := t.TempDir(), t.TempDir()
	files := lockedRFC2317Fixture(t, filepath.Join(first, "first-parent.zone"), filepath.Join(second, "first-child.zone"))
	for _, mode := range []string{"busy", "free"} {
		if mode == "free" {
			files.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run", "^TestRFC2317InstallFilesDirectoryLockAcrossProcesses$")
		cmd.Env = append(os.Environ(), modeVar+"="+mode,
			parentVar+"="+filepath.Join(second, "other-parent.zone"), childVar+"="+filepath.Join(first, "other-child.zone"))
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("separate process did not respect the %s directory lock: %v\n%s", mode, err, output)
		}
	}
}
