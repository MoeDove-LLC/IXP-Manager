package traffic

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const legacyManifestName = "legacy-manifest.json"
const legacyManifestLimit = 8 << 20
const legacyCompressedLimit = 16 << 20

type legacyFile struct {
	Metadata   LegacyMetadata `json:"metadata"`
	FileSHA256 string         `json:"fileSha256"`
}
type legacyManifest struct {
	Version int                   `json:"version"`
	Batches map[string]legacyFile `json:"batches"`
}
type LegacyStore struct {
	mu       sync.RWMutex
	dir      string
	root     *os.Root
	lock     *os.File
	manifest legacyManifest
	closed   bool
	failed   error
}

func OpenLegacy(dir string) (*LegacyStore, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	if info, e := os.Lstat(dir); e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("legacy store requires a real private directory")
	}
	root, e := os.OpenRoot(dir)
	if e != nil {
		return nil, e
	}
	s := &LegacyStore{dir: dir, root: root, manifest: legacyManifest{Version: 1, Batches: map[string]legacyFile{}}}
	fail := func(e error) (*LegacyStore, error) { s.Close(); return nil, e }
	s.lock, e = root.OpenFile(".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if e != nil {
		return fail(e)
	}
	if info, e := s.lock.Stat(); e != nil || !info.Mode().IsRegular() {
		return fail(errors.New("legacy store lock must be regular"))
	}
	if e = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return fail(errors.New("legacy store is in use; offline CLI requires the application to be stopped"))
	}
	if e = root.Mkdir("batches", 0700); e != nil && !os.IsExist(e) {
		return fail(e)
	}
	if info, e := root.Lstat("batches"); e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fail(errors.New("invalid legacy batch directory"))
	}
	raw, e := legacyReadFile(root, legacyManifestName, legacyManifestLimit)
	if e == nil {
		if e = legacyStrictDecode(raw, &s.manifest); e != nil {
			return fail(errors.New("invalid legacy manifest JSON"))
		}
		if e = legacyValidateManifest(s.manifest); e != nil {
			return fail(e)
		}
	} else if os.IsNotExist(e) {
		if e = s.saveManifest(s.manifest); e != nil {
			return fail(e)
		}
	} else {
		return fail(e)
	}
	for id, ref := range s.manifest.Batches {
		if _, e = legacyReadBatch(root, id, ref); e != nil {
			return fail(e)
		}
	}
	if e = s.clean(); e != nil {
		return fail(e)
	}
	return s, nil
}
func legacyReadFile(root *os.Root, name string, limit int64) ([]byte, error) {
	f, e := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("legacy file is not regular or exceeds its limit")
	}
	raw, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(raw)) > limit {
		return nil, errors.New("cannot read bounded legacy file")
	}
	return raw, nil
}
func legacyValidateManifest(m legacyManifest) error {
	if m.Version != 1 || m.Batches == nil || len(m.Batches) > LegacyMaxBatches {
		return errors.New("invalid legacy manifest")
	}
	var size int64
	for id, ref := range m.Batches {
		meta := ref.Metadata
		if id != meta.ID || !legacyHash.MatchString(id) || meta.Checksum != id || !legacyHash.MatchString(meta.InputChecksum) || !legacyHash.MatchString(ref.FileSHA256) || meta.RowCount < 1 || meta.RowCount > LegacyMaxRows || meta.StaffOnlyRows < 0 || meta.StaffOnlyRows > meta.RowCount || meta.ImportedAt.IsZero() || meta.Size <= 0 || meta.Size > legacyCompressedLimit {
			return errors.New("invalid legacy batch reference")
		}
		if _, e := legacySourceValidate(meta.Source); e != nil {
			return e
		}
		size += meta.Size
	}
	if size > LegacyQuotaBytes {
		return errors.New("legacy manifest exceeds512MiB quota")
	}
	return nil
}
func legacyReadBatch(root *os.Root, id string, ref legacyFile) (LegacyBatch, error) {
	var batch LegacyBatch
	if !legacyHash.MatchString(id) {
		return batch, errors.New("invalid legacy batch ID")
	}
	raw, e := legacyReadFile(root, "batches/"+id+".json.gz", legacyCompressedLimit)
	if e != nil {
		return batch, e
	}
	if int64(len(raw)) != ref.Metadata.Size || legacyDigest(raw) != ref.FileSHA256 {
		return batch, errors.New("legacy immutable batch hash mismatch")
	}
	reader, e := gzip.NewReader(bytes.NewReader(raw))
	if e != nil {
		return batch, errors.New("invalid legacy compression")
	}
	decoded, e := io.ReadAll(io.LimitReader(reader, LegacyMaxBatchBytes+1))
	closeErr := reader.Close()
	if e != nil || closeErr != nil || len(decoded) > LegacyMaxBatchBytes {
		return batch, errors.New("legacy compressed batch exceeds its decoded bound or is corrupt")
	}
	if e = legacyStrictDecode(decoded, &batch); e != nil {
		return batch, errors.New("invalid legacy immutable batch JSON")
	}
	if e = ValidateLegacyBatch(batch); e != nil {
		return batch, e
	}
	if batch.ID != id || batch.InputChecksum != ref.Metadata.InputChecksum || len(batch.Rows) != ref.Metadata.RowCount || batch.Format != ref.Metadata.Format || batch.Source != ref.Metadata.Source {
		return batch, errors.New("legacy manifest/content mismatch")
	}
	staff := 0
	for _, r := range batch.Rows {
		if r.StaffOnly {
			staff++
		}
	}
	if staff != ref.Metadata.StaffOnlyRows {
		return batch, errors.New("legacy ownership summary mismatch")
	}
	return batch, nil
}
func legacyTempName() string {
	var raw [16]byte
	if _, e := rand.Read(raw[:]); e != nil {
		return ""
	}
	return ".pending-" + hex.EncodeToString(raw[:])
}
func legacySync(root *os.Root, name string) error {
	d, e := root.Open(name)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func (s *LegacyStore) saveManifest(next legacyManifest) error {
	raw, e := json.Marshal(next)
	if e != nil || len(raw) > legacyManifestLimit {
		return errors.New("legacy manifest exceeds8MiB")
	}
	name := legacyTempName()
	if name == "" {
		return errors.New("cannot allocate private legacy manifest")
	}
	f, e := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer s.root.Remove(name)
	defer f.Close()
	if _, e = f.Write(raw); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = s.root.Rename(name, legacyManifestName); e != nil {
		return e
	}
	if e = legacySync(s.root, "."); e != nil {
		return e
	}
	return nil
}
func (s *LegacyStore) Put(batch LegacyBatch, expectedChecksum string) (LegacyMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var meta LegacyMetadata
	if s.closed {
		return meta, errors.New("legacy store is closed")
	}
	if s.failed != nil {
		return meta, errors.New("legacy store requires restart after I/O failure")
	}
	if expectedChecksum == "" || expectedChecksum != batch.Checksum {
		return meta, errors.New("reviewed legacy checksum does not match the batch")
	}
	if e := ValidateLegacyBatch(batch); e != nil {
		return meta, e
	}
	if ref, ok := s.manifest.Batches[batch.ID]; ok {
		if _, e := legacyReadBatch(s.root, batch.ID, ref); e != nil {
			return meta, e
		}
		return ref.Metadata, nil
	}
	if len(s.manifest.Batches) >= LegacyMaxBatches {
		return meta, errors.New("legacy batch limit2000 reached; explicitly retain/delete reviewed imports")
	}
	raw, e := json.Marshal(batch)
	if e != nil {
		return meta, e
	}
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, e = z.Write(raw); e != nil {
		return meta, e
	}
	if e = z.Close(); e != nil {
		return meta, e
	}
	if compressed.Len() > legacyCompressedLimit {
		return meta, errors.New("legacy compressed batch exceeds16MiB")
	}
	var used int64
	for _, ref := range s.manifest.Batches {
		used += ref.Metadata.Size
	}
	if used+int64(compressed.Len()) > LegacyQuotaBytes {
		return meta, errors.New("legacy archive512MiB quota reached; explicitly retain/delete reviewed imports")
	}
	name := "batches/" + batch.ID + ".json.gz"
	temporary := "batches/" + legacyTempName()
	if strings.HasSuffix(temporary, "/") {
		return meta, errors.New("cannot allocate private legacy file")
	}
	f, e := s.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return meta, e
	}
	defer s.root.Remove(temporary)
	defer f.Close()
	if _, e = f.Write(compressed.Bytes()); e == nil {
		e = f.Sync()
	}
	if e == nil {
		e = f.Close()
	}
	if e != nil {
		s.failed = e
		return meta, e
	}
	if e = s.root.Link(temporary, name); e != nil {
		s.failed = e
		return meta, e
	}
	if e = legacySync(s.root, "batches"); e != nil {
		s.failed = e
		return meta, e
	}
	meta = LegacyMetadata{ID: batch.ID, Checksum: batch.Checksum, InputChecksum: batch.InputChecksum, Format: batch.Format, Source: batch.Source, RowCount: len(batch.Rows), ImportedAt: time.Now().UTC(), Size: int64(compressed.Len())}
	for _, r := range batch.Rows {
		if r.StaffOnly {
			meta.StaffOnlyRows++
		}
		if r.OriginalDay != "" {
			if meta.OriginalDayFrom == "" || r.OriginalDay < meta.OriginalDayFrom {
				meta.OriginalDayFrom = r.OriginalDay
			}
			if r.OriginalDay > meta.OriginalDayTo {
				meta.OriginalDayTo = r.OriginalDay
			}
		}
	}
	next := legacyManifest{Version: 1, Batches: map[string]legacyFile{}}
	for id, ref := range s.manifest.Batches {
		next.Batches[id] = ref
	}
	next.Batches[batch.ID] = legacyFile{Metadata: meta, FileSHA256: legacyDigest(compressed.Bytes())}
	if e = s.saveManifest(next); e != nil {
		s.failed = e
		return LegacyMetadata{}, e
	}
	s.manifest = next
	return meta, nil
}
func (s *LegacyStore) List() (LegacyCatalog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := LegacyCatalog{Batches: []LegacyMetadata{}, MaxBytes: LegacyQuotaBytes, MaxBatches: LegacyMaxBatches}
	if s.closed {
		return out, errors.New("legacy store is closed")
	}
	for _, ref := range s.manifest.Batches {
		out.Batches = append(out.Batches, ref.Metadata)
		out.Bytes += ref.Metadata.Size
	}
	sort.Slice(out.Batches, func(i, j int) bool { return out.Batches[i].ID < out.Batches[j].ID })
	return out, nil
}
func (s *LegacyStore) Get(id string) (LegacyBatch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return LegacyBatch{}, errors.New("legacy store is closed")
	}
	ref, ok := s.manifest.Batches[id]
	if !ok {
		return LegacyBatch{}, os.ErrNotExist
	}
	return legacyReadBatch(s.root, id, ref)
}
func (s *LegacyStore) Query(q LegacyQuery) (LegacyResult, error) {
	out := LegacyResult{SourceKind: "legacy-report", BatchID: q.BatchID, Rows: []LegacyRow{}, Offset: q.Offset, Limit: q.Limit, NoAggregation: true, Notices: []string{"Rows are original independent reports/rate windows. No totals, rates or maxima are added across rows, periods or imports."}}
	if !legacyHash.MatchString(q.BatchID) || q.Offset < 0 || q.Offset > LegacyMaxRows {
		return out, errors.New("legacy query requires a batch ID and bounded offset")
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > 1000 {
		return out, errors.New("legacy query limit must be1..1000")
	}
	out.Limit = q.Limit
	if q.MemberID != "" && (!legacyID.MatchString(q.MemberID) || !legacyHash.MatchString(q.MemberRevision)) {
		return out, errors.New("legacy member query requires the historical entity generation")
	}
	for _, day := range []string{q.OriginalDayFrom, q.OriginalDayTo} {
		if day != "" {
			if _, e := time.Parse("2006-01-02", day); e != nil {
				return out, errors.New("query dates must be original YYYY-MM-DD labels")
			}
		}
	}
	if q.OriginalDayFrom != "" && q.OriginalDayTo != "" && q.OriginalDayTo < q.OriginalDayFrom {
		return out, errors.New("legacy query date range is reversed")
	}
	if q.WindowFrom != nil && q.WindowTo != nil && !q.WindowTo.After(*q.WindowFrom) {
		return out, errors.New("legacy query window is reversed")
	}
	if q.WindowFrom != nil || q.WindowTo != nil {
		out.Notices = append(out.Notices, "Known-window filtering returns whole original reports that overlap the selection; rows with unknown bounds are excluded, and reported totals are not prorated.")
	}
	batch, e := s.Get(q.BatchID)
	if e != nil {
		return out, e
	}
	for _, row := range batch.Rows {
		if q.WindowFrom != nil || q.WindowTo != nil {
			if row.Window.Start == nil || row.Window.End == nil || row.Window.BoundaryQuality == "unknown" {
				continue
			}
			if q.WindowFrom != nil && !row.Window.End.After(*q.WindowFrom) || q.WindowTo != nil && !row.Window.Start.Before(*q.WindowTo) {
				continue
			}
		}
		if q.MemberID != "" && (row.StaffOnly || row.MemberID != q.MemberID || row.MemberRevision != q.MemberRevision) {
			continue
		}
		if q.SourceKind != "" && row.SourceKind != q.SourceKind || q.EntityKind != "" && row.EntityKind != q.EntityKind || q.EntityID != "" && row.EntityID != q.EntityID || q.Period != "" && row.Window.Period != q.Period || q.Category != "" && row.Category != q.Category || q.Family != 0 && row.Family != q.Family {
			continue
		}
		if q.OriginalDayFrom != "" && (row.OriginalDay == "" || row.OriginalDay < q.OriginalDayFrom) || q.OriginalDayTo != "" && (row.OriginalDay == "" || row.OriginalDay > q.OriginalDayTo) {
			continue
		}
		if out.TotalRows >= q.Offset && len(out.Rows) < q.Limit {
			out.Rows = append(out.Rows, row)
		}
		out.TotalRows++
	}
	out.Truncated = q.Offset+len(out.Rows) < out.TotalRows
	return out, nil
}
func (s *LegacyStore) Delete(id, expectedChecksum string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("legacy store is closed")
	}
	if s.failed != nil {
		return errors.New("legacy store requires restart after I/O failure")
	}
	ref, ok := s.manifest.Batches[id]
	if !ok {
		return os.ErrNotExist
	}
	if expectedChecksum == "" || ref.Metadata.Checksum != expectedChecksum {
		return errors.New("legacy retention checksum changed")
	}
	next := legacyManifest{Version: 1, Batches: map[string]legacyFile{}}
	for key, value := range s.manifest.Batches {
		if key != id {
			next.Batches[key] = value
		}
	}
	if e := s.saveManifest(next); e != nil {
		s.failed = e
		return e
	}
	s.manifest = next
	if e := s.root.Remove("batches/" + id + ".json.gz"); e != nil && !os.IsNotExist(e) {
		return e
	}
	return legacySync(s.root, "batches")
}
func (s *LegacyStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var e error
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		e = s.lock.Close()
	}
	if s.root != nil {
		e = errors.Join(e, s.root.Close())
	}
	return e
}
func (s *LegacyStore) clean() error {
	for _, dir := range []string{".", "batches"} {
		handle, e := s.root.Open(dir)
		if e != nil {
			return e
		}
		entries, e := handle.ReadDir(-1)
		handle.Close()
		if e != nil {
			return e
		}
		if len(entries) > LegacyMaxBatches*2+10 {
			return errors.New("legacy directory file count exceeds bound")
		}
		for _, entry := range entries {
			if dir == "." && (entry.Name() == ".lock" || entry.Name() == legacyManifestName || entry.Name() == "batches") {
				continue
			}
			if dir == "batches" && strings.HasSuffix(entry.Name(), ".json.gz") {
				id := strings.TrimSuffix(entry.Name(), ".json.gz")
				if !legacyHash.MatchString(id) {
					return errors.New("unexpected legacy batch path")
				}
				if _, ok := s.manifest.Batches[id]; ok {
					continue
				}
			} else if !strings.HasPrefix(entry.Name(), ".pending-") {
				return errors.New("unexpected legacy store file")
			}
			info, e := entry.Info()
			if e != nil || !info.Mode().IsRegular() {
				return errors.New("unsafe legacy orphan")
			}
			name := entry.Name()
			if dir != "." {
				name = dir + "/" + name
			}
			if e = s.root.Remove(name); e != nil {
				return e
			}
		}
	}
	return nil
}

// LegacyListFiles validates without opening a writer, repairing, or cleaning.
// The caller must quiesce writers to copy a coherent backup checkpoint.
func LegacyListFiles(dir string) ([]string, error) {
	if info, e := os.Lstat(dir); os.IsNotExist(e) {
		return []string{}, nil
	} else if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("legacy backup requires a real directory")
	}
	root, e := os.OpenRoot(dir)
	if e != nil {
		return nil, e
	}
	defer root.Close()
	raw, e := legacyReadFile(root, legacyManifestName, legacyManifestLimit)
	if os.IsNotExist(e) {
		return []string{}, nil
	}
	if e != nil {
		return nil, e
	}
	var manifest legacyManifest
	if e = legacyStrictDecode(raw, &manifest); e != nil {
		return nil, e
	}
	if e = legacyValidateManifest(manifest); e != nil {
		return nil, e
	}
	if info, e := root.Lstat("batches"); e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("legacy batch directory is invalid")
	}
	files := []string{legacyManifestName}
	for id, ref := range manifest.Batches {
		if _, e = legacyReadBatch(root, id, ref); e != nil {
			return nil, e
		}
		files = append(files, "batches/"+id+".json.gz")
	}
	sort.Strings(files)
	return files, nil
}
func LegacyValidateFiles(dir string) error { _, e := LegacyListFiles(dir); return e }
