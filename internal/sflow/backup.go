package sflow

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"
)

// ErrTornWALTail means all complete WAL records were valid but the final write
// lacks its newline commit boundary. Inspection never truncates those bytes.
var ErrTornWALTail = errors.New("sFlow WAL has an incomplete final frame; read-only inspection did not repair it")

func openBackupDir(dir string) (*os.File, error) {
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), dir)
	info, err := f.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("sFlow backup directory must be private and non-symlinked")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		f.Close()
		return nil, errors.New("sFlow backup directory has unexpected owner")
	}
	return f, nil
}
func openBackupFile(dir *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(dir.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		f.Close()
		return nil, errors.New("sFlow backup file must be a private regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 || int(stat.Uid) != os.Geteuid() {
		f.Close()
		return nil, errors.New("sFlow backup file has unexpected ownership or links")
	}
	return f, nil
}
func listBackupFiles(dir *os.File) ([]string, error) {
	entries, err := dir.ReadDir(1025)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > 1024 {
		return nil, errors.New("sFlow directory entry limit exceeded")
	}
	files := []string{}
	var total int64
	for _, entry := range entries {
		name := entry.Name()
		if name != ".lock" && name != "flows.snapshot.json" && name != "flows.wal" && !strings.HasPrefix(name, ".snapshot-") {
			return nil, errors.New("unrecognized sFlow storage file; refusing incomplete backup")
		}
		f, err := openBackupFile(dir, name)
		if err != nil {
			return nil, err
		}
		info, err := f.Stat()
		f.Close()
		if err != nil {
			return nil, err
		}
		if info.Size() > MaxStoreBytes-total {
			return nil, errors.New("sFlow storage directory exceeds byte quota")
		}
		total += info.Size()
		if name == "flows.snapshot.json" || name == "flows.wal" {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	return files, nil
}

// ListFiles enumerates only persisted data relative to dir. Missing directories
// are legacy empty stores. No lock, file, directory or maintenance loop is made.
func ListFiles(dir string) ([]string, error) {
	f, err := openBackupDir(dir)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return listBackupFiles(f)
}
func readBackupFile(dir *os.File, name string, limit int64) ([]byte, error) {
	f, err := openBackupFile(dir, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || before.Size() > limit {
		return nil, errors.New("sFlow backup file exceeds bound")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("sFlow backup read exceeds bound")
	}
	after, err := f.Stat()
	if err != nil || after.Size() != before.Size() || after.ModTime() != before.ModTime() || int64(len(b)) != after.Size() {
		return nil, errors.New("sFlow source changed during read-only backup validation")
	}
	return b, nil
}
func validateBackupState(s flowSnapshot) error {
	if s.Version != 1 || s.Rows == nil || s.Seen == nil || s.Quality == nil || len(s.Rows)+len(s.Quality) > MaxAggregateRows || len(s.Seen) > maxSeen || len(s.Cursors) > maxExporters {
		return errors.New("invalid bounded sFlow snapshot state")
	}
	if s.Sequence == 0 && (len(s.Rows)+len(s.Seen)+len(s.Quality)+len(s.Cursors) > 0) {
		return errors.New("sFlow data lacks a committed WAL sequence")
	}
	for key, row := range s.Rows {
		if key != bucketKey(row) || validateBucket(row) != nil {
			return errors.New("sFlow snapshot row identity or ACL integrity failed")
		}
	}
	for key, q := range s.Quality {
		if key != qualityKey(q) || validateQuality(q) != nil {
			return errors.New("sFlow quality snapshot integrity failed")
		}
	}
	for key, seen := range s.Seen {
		if !identifier.MatchString(key) || !validDigest(seen.Checksum) || seen.At.IsZero() {
			return errors.New("sFlow deduplication snapshot integrity failed")
		}
	}
	for key, c := range s.Cursors {
		if key != c.ExporterID || !validCursor(c) || c.Head.ReceivedAt.After(c.LastReceivedAt) || c.Pending != nil && c.Pending.ReceivedAt.After(c.LastReceivedAt) || c.LastDatagram != nil && c.LastDatagram.ReceivedAt.After(c.LastReceivedAt) {
			return errors.New("sFlow cursor snapshot integrity failed")
		}
	}
	return nil
}

// ValidateFiles inspects original bytes without Open, pruning, compaction,
// truncation, locking, or background activity. Replay is in private memory only.
// The caller must exclude active writers before enumeration/copying.
func ValidateFiles(dir string) error {
	f, err := openBackupDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	files, err := listBackupFiles(f)
	if err != nil {
		return err
	}
	s := flowSnapshot{Version: 1, Rows: map[string]Bucket{}, Seen: map[string]seenBatch{}, Quality: map[string]QualityDay{}, Cursors: map[string]exporterCursor{}}
	if containsID(files, "flows.snapshot.json") {
		raw, err := readBackupFile(f, "flows.snapshot.json", MaxStoreBytes)
		if err != nil {
			return err
		}
		var disk flowDiskSnapshot
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&disk) != nil || decoder.Decode(new(any)) != io.EOF || !validDigest(disk.Checksum) || disk.Checksum != digest(disk.State) {
			return errors.New("sFlow snapshot checksum failed")
		}
		s = disk.State
		if err = validateBackupState(s); err != nil {
			return err
		}
	}
	if !containsID(files, "flows.wal") {
		return nil
	}
	raw, err := readBackupFile(f, "flows.wal", maxWALBytes+(1<<20))
	if err != nil {
		return err
	}
	reader := bufio.NewReader(bytes.NewReader(raw))
	last := uint64(0)
	torn := false
	snapshotSequence, snapshotSeen := s.Sequence, s.Seen
	snapshotCursors := make(map[string]exporterCursor, len(s.Cursors))
	for id, c := range s.Cursors {
		snapshotCursors[id] = c
	}
	committedLast := map[string]AttributedBatch{}
	pruned := false
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 1<<20 {
			return errors.New("sFlow WAL frame exceeds bound")
		}
		if err == io.EOF {
			torn = len(line) > 0
			break
		}
		if err != nil {
			return err
		}
		var frame flowFrame
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&frame) != nil || decoder.Decode(new(any)) != io.EOF || frame.Sequence == 0 || frame.Checksum != digest(frame.Batch) || validateAttributed(frame.Batch) != nil {
			return errors.New("sFlow complete WAL frame integrity failed")
		}
		if last != 0 && frame.Sequence != last+1 {
			return errors.New("sFlow WAL sequence discontinuity")
		}
		last = frame.Sequence
		if frame.Sequence <= snapshotSequence {
			if seen, ok := snapshotSeen[frame.Batch.ID]; ok && seen.Checksum != frame.Checksum {
				return errors.New("snapshot and committed WAL disagree on batch identity")
			}
			committedLast[frame.Batch.ExporterID] = frame.Batch
			continue
		}
		if frame.Sequence != s.Sequence+1 {
			return errors.New("sFlow WAL does not follow its snapshot")
		}
		if !pruned {
			prune(&s, time.Now().UTC())
			pruned = true
		}
		s, err = apply(s, frame.Batch, time.Now().UTC())
		if err != nil {
			return err
		}
		s.Sequence = frame.Sequence
		if err = validateBackupState(s); err != nil {
			return err
		}
	}
	for id, b := range committedLast {
		if b.DatagramDigest == "" {
			continue
		}
		c, ok := snapshotCursors[id]
		if !ok {
			return errors.New("snapshot lost a committed exporter cursor")
		}
		if p := c.LastDatagram; p != nil && (p.Sequence != b.Sequence || p.UptimeMS != b.UptimeMS || p.Digest != b.DatagramDigest || !p.ReceivedAt.Equal(b.ReceivedAt)) {
			return errors.New("snapshot cursor and last committed WAL datagram disagree")
		}
	}
	if torn {
		return ErrTornWALTail
	}
	return nil
}
