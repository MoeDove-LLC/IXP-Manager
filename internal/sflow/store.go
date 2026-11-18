package sflow

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

const maxWALBytes = 4 << 20
const maxSeen = 200000

type seenBatch struct {
	Checksum string    `json:"checksum"`
	At       time.Time `json:"at"`
}
type flowSnapshot struct {
	Version  int                       `json:"version"`
	Sequence uint64                    `json:"sequence"`
	Rows     map[string]Bucket         `json:"rows"`
	Seen     map[string]seenBatch      `json:"seen"`
	Quality  map[string]QualityDay     `json:"quality"`
	Cursors  map[string]exporterCursor `json:"cursors,omitempty"`
}
type flowFrame struct {
	Sequence uint64          `json:"sequence"`
	Checksum string          `json:"checksum"`
	Batch    AttributedBatch `json:"batch"`
}
type flowDiskSnapshot struct {
	State    flowSnapshot `json:"state"`
	Checksum string       `json:"checksum"`
}
type Store struct {
	mu                      sync.RWMutex
	dir                     string
	state                   flowSnapshot
	wal, lock               *os.File
	closed                  bool
	walBytes, snapshotBytes int64
	stop, done              chan struct{}
	maintenanceErr          error
	stateBytes              int64
	exporterHistory         map[string]bool
}

func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func storeFile(path string, flags int) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		f.Close()
		return nil, errors.New("sFlow store file must be private regular0600")
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || sys.Nlink != 1 || int(sys.Uid) != os.Geteuid() {
		f.Close()
		return nil, errors.New("sFlow store file has unsafe owner or links")
	}
	return f, nil
}
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("sFlow data directory must be private and non-symlinked")
	}
	s := &Store{dir: dir, state: flowSnapshot{Version: 1, Rows: map[string]Bucket{}, Seen: map[string]seenBatch{}, Quality: map[string]QualityDay{}, Cursors: map[string]exporterCursor{}}, stop: make(chan struct{}), done: make(chan struct{})}
	s.lock, err = storeFile(filepath.Join(dir, ".lock"), syscall.O_RDWR|syscall.O_CREAT)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		s.lock.Close()
		return nil, errors.New("sFlow store already in use")
	}
	failed := true
	defer func() {
		if failed {
			if s.wal != nil {
				s.wal.Close()
			}
			s.lock.Close()
		}
	}()
	if f, e := storeFile(filepath.Join(dir, "flows.snapshot.json"), syscall.O_RDONLY); e == nil {
		raw, e := io.ReadAll(io.LimitReader(f, MaxStoreBytes+1))
		f.Close()
		if e != nil || len(raw) > MaxStoreBytes {
			return nil, errors.New("sFlow snapshot exceeds quota")
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		var disk flowDiskSnapshot
		if dec.Decode(&disk) != nil || dec.Decode(new(any)) != io.EOF || disk.Checksum != digest(disk.State) {
			return nil, errors.New("sFlow snapshot checksum failed")
		}
		s.state = disk.State
		if s.state.Cursors == nil {
			s.state.Cursors = map[string]exporterCursor{}
		}
		if len(s.state.Cursors) > maxExporters {
			return nil, errors.New("sFlow durable exporter cursor quota exceeded")
		}
		for id, c := range s.state.Cursors {
			if id != c.ExporterID || !validCursor(c) {
				return nil, errors.New("invalid durable sFlow exporter cursor")
			}
		}
		if s.state.Version != 1 || s.state.Rows == nil || s.state.Seen == nil || s.state.Quality == nil || len(s.state.Rows)+len(s.state.Quality) > MaxAggregateRows || len(s.state.Seen) > maxSeen {
			return nil, errors.New("invalid sFlow snapshot")
		}
		s.snapshotBytes = int64(len(raw))
		for key, row := range s.state.Rows {
			if key != bucketKey(row) || validateBucket(row) != nil {
				return nil, errors.New("sFlow snapshot row integrity failed")
			}
		}
		for key, q := range s.state.Quality {
			if key != qualityKey(q) || validateQuality(q) != nil {
				return nil, errors.New("invalid sFlow quality snapshot")
			}
		}
		for id, v := range s.state.Seen {
			if !identifier.MatchString(id) || len(v.Checksum) != 64 || v.At.IsZero() {
				return nil, errors.New("invalid sFlow deduplication snapshot")
			}
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	s.wal, err = storeFile(filepath.Join(dir, "flows.wal"), syscall.O_RDWR|syscall.O_CREAT)
	if err != nil {
		return nil, err
	}
	info, _ := s.wal.Stat()
	if info.Size() > maxWALBytes+(1<<20) {
		return nil, errors.New("sFlow WAL exceeds quota")
	}
	reader := bufio.NewReader(io.LimitReader(s.wal, maxWALBytes+(1<<20)+1))
	offset := int64(0)
	lastSequence := uint64(0)
	prune(&s.state, time.Now().UTC())
	for {
		line, e := reader.ReadBytes('\n')
		if len(line) > 1<<20 {
			return nil, errors.New("sFlow WAL record exceeds bound")
		}
		if e == io.EOF {
			if len(line) > 0 {
				if err = s.wal.Truncate(offset); err != nil {
					return nil, err
				}
			}
			break
		}
		if e != nil {
			return nil, e
		}
		offset += int64(len(line))
		var frame flowFrame
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if dec.Decode(&frame) != nil || dec.Decode(new(any)) != io.EOF || frame.Sequence == 0 || frame.Checksum != digest(frame.Batch) || validateAttributed(frame.Batch) != nil {
			return nil, errors.New("sFlow WAL integrity failed")
		}
		if lastSequence != 0 && frame.Sequence != lastSequence+1 {
			return nil, errors.New("sFlow WAL sequence discontinuity")
		}
		lastSequence = frame.Sequence
		if frame.Sequence <= s.state.Sequence {
			continue
		}
		if frame.Sequence != s.state.Sequence+1 {
			return nil, errors.New("sFlow WAL does not follow snapshot")
		}
		next, err := apply(s.state, frame.Batch, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		next.Sequence = frame.Sequence
		s.state = next
	}
	s.walBytes = offset
	if _, err = s.wal.Seek(0, io.SeekEnd); err != nil {
		return nil, err
	}
	prune(&s.state, time.Now().UTC())
	if err = s.rebuildAccountingLocked(); err != nil {
		return nil, err
	}
	if err = s.compactLocked(); err != nil {
		return nil, err
	}
	failed = false
	go s.maintain()
	return s, nil
}
func validateAttributed(b AttributedBatch) error {
	if !identifier.MatchString(b.ID) || !identifier.MatchString(b.ExporterID) || !identifier.MatchString(b.SwitchID) || (b.InfrastructureID != "" && !identifier.MatchString(b.InfrastructureID)) || !revisionPattern.MatchString(b.MappingRevision) || b.ReceivedAt.IsZero() || len(b.Flows) > MaxSamples || b.Quality.UDPAuthenticated {
		return errors.New("invalid attributed sFlow batch")
	}
	if b.DatagramDigest != "" && !validDigest(b.DatagramDigest) {
		return errors.New("invalid safe datagram digest")
	}
	var count uint64
	for _, f := range b.Flows {
		if f.SampleCount == 0 || f.SampleCount > MaxSamples || count > MaxSamples-f.SampleCount || f.Family != 0 && f.Family != 4 && f.Family != 6 {
			return errors.New("invalid flow sample count or family")
		}
		count += f.SampleCount
		for _, id := range []string{f.VLANID, f.SourceMemberID, f.DestinationMemberID, f.SourceConnectionID, f.DestinationConnectionID} {
			if id != "" && !identifier.MatchString(id) {
				return errors.New("invalid attributed flow reference")
			}
		}
		for _, revision := range []string{f.SourceMemberRevision, f.DestinationMemberRevision} {
			if revision != "" && !validDigest(revision) {
				return errors.New("invalid sampled member entity revision")
			}
		}
		if f.SourceMemberID == "" && f.SourceMemberRevision != "" || f.DestinationMemberID == "" && f.DestinationMemberRevision != "" {
			return errors.New("member entity revision without member ID")
		}
		if f.UnattributedReason == "" {
			if f.VLANID == "" || f.SourceMemberID == "" || f.DestinationMemberID == "" || f.SourceConnectionID == "" || f.DestinationConnectionID == "" || f.Family == 0 || !decimal(f.EstimatedBytes) || !decimal(f.EstimatedPackets) {
				return errors.New("attributed flow requires complete authoritative references and estimates")
			}
		} else if !identifier.MatchString(f.UnattributedReason) {
			return errors.New("invalid unattributed reason")
		}
		for _, v := range []string{f.EstimatedBytes, f.EstimatedPackets} {
			if v != "" && !decimal(v) {
				return errors.New("invalid sampled estimate")
			}
		}
	}
	return nil
}
func addDecimal(a, b string) string {
	if a == "" || b == "" {
		return ""
	}
	x, _ := new(big.Int).SetString(a, 10)
	y, _ := new(big.Int).SetString(b, 10)
	return x.Add(x, y).String()
}
func bucketKey(b Bucket) string {
	key := []any{b.Resolution, b.Start, b.ExporterID, b.SwitchID, b.InfrastructureID, b.VLANID, b.MappingRevision, b.Family, b.SourceMemberID, b.DestinationMemberID, b.SourceConnectionID, b.DestinationConnectionID, b.UnattributedReason}
	if b.SourceMemberRevision != "" || b.DestinationMemberRevision != "" {
		key = append(key, "member-entities-v1", b.SourceMemberRevision, b.DestinationMemberRevision)
	}
	return digest(key)
}
func validTotal(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > 80 {
		return false
	}
	n, ok := new(big.Int).SetString(s, 10)
	return ok && n.Sign() >= 0 && n.String() == s
}
func validateBucket(b Bucket) error {
	if b.Resolution != "5m" && b.Resolution != "1d" || b.Start.IsZero() || b.FirstReceivedAt.IsZero() || b.LastReceivedAt.Before(b.FirstReceivedAt) || !validTotal(b.EstimatedBytes) || !validTotal(b.EstimatedPackets) || !validTotal(b.SampleCount) || b.SampleCount == "" || !validTotal(b.TCP179Samples) || b.TCP179Samples == "" {
		return errors.New("invalid sFlow bucket")
	}
	step := 5 * time.Minute
	if b.Resolution == "1d" {
		step = 24 * time.Hour
	}
	if !b.Start.Equal(b.Start.UTC().Truncate(step)) || b.FirstReceivedAt.Before(b.Start) || !b.LastReceivedAt.Before(b.Start.Add(step)) || b.SampleCount == "0" || !identifier.MatchString(b.ExporterID) || !identifier.MatchString(b.SwitchID) || !revisionPattern.MatchString(b.MappingRevision) || (b.Family != 0 && b.Family != 4 && b.Family != 6) {
		return errors.New("invalid sFlow bucket scope or time")
	}
	for _, id := range []string{b.InfrastructureID, b.VLANID, b.SourceMemberID, b.DestinationMemberID, b.SourceConnectionID, b.DestinationConnectionID} {
		if id != "" && !identifier.MatchString(id) {
			return errors.New("invalid sFlow bucket reference")
		}
	}
	for _, revision := range []string{b.SourceMemberRevision, b.DestinationMemberRevision} {
		if revision != "" && !validDigest(revision) {
			return errors.New("invalid aggregate member entity revision")
		}
	}
	if b.SourceMemberID == "" && b.SourceMemberRevision != "" || b.DestinationMemberID == "" && b.DestinationMemberRevision != "" {
		return errors.New("aggregate member revision without member")
	}
	if b.UnattributedReason == "" {
		if b.VLANID == "" || b.SourceMemberID == "" || b.DestinationMemberID == "" || b.SourceConnectionID == "" || b.DestinationConnectionID == "" || b.Family == 0 || b.EstimatedBytes == "" || b.EstimatedPackets == "" {
			return errors.New("incomplete attributed aggregate")
		}
	} else if !identifier.MatchString(b.UnattributedReason) {
		return errors.New("invalid unattributed aggregate reason")
	}
	count, _ := new(big.Int).SetString(b.SampleCount, 10)
	activity, _ := new(big.Int).SetString(b.TCP179Samples, 10)
	if activity.Cmp(count) > 0 {
		return errors.New("TCP179 activity samples exceed sampled packet count")
	}
	if b.LastTCP179At != nil && (b.TCP179Samples == "0" || b.LastTCP179At.Before(b.FirstReceivedAt) || b.LastTCP179At.After(b.LastReceivedAt)) {
		return errors.New("TCP179 activity time contradicts bucket evidence")
	}
	return nil
}
func prune(s *flowSnapshot, now time.Time) {
	for key, row := range s.Rows {
		retention := FiveMinuteRetention
		if row.Resolution == "1d" {
			retention = DailyRetention
		}
		if row.Start.Before(now.Add(-retention).Truncate(map[string]time.Duration{"5m": 5 * time.Minute, "1d": 24 * time.Hour}[row.Resolution])) {
			delete(s.Rows, key)
		}
	}
	for key, q := range s.Quality {
		if q.Start.Before(now.Add(-DailyRetention).UTC().Truncate(24 * time.Hour)) {
			delete(s.Quality, key)
		}
	}
	for id, entry := range s.Seen {
		if entry.At.Before(now.Add(-10 * time.Minute)) {
			delete(s.Seen, id)
		}
	}
}
func qualityKey(q QualityDay) string { return digest([]any{q.Start, q.ExporterID, q.SwitchID}) }
func validateQuality(q QualityDay) error {
	if q.Start.IsZero() || !q.Start.Equal(q.Start.UTC().Truncate(24*time.Hour)) || !identifier.MatchString(q.ExporterID) || !identifier.MatchString(q.SwitchID) {
		return errors.New("invalid quality identity")
	}
	for _, v := range []string{q.Datagrams, q.MissingDatagrams, q.DuplicateDatagrams, q.UnorderedDatagrams, q.RestartSignals, q.SampleSequenceGaps, q.CounterSamples} {
		if v == "" || !validTotal(v) {
			return errors.New("invalid quality counter")
		}
	}
	return nil
}

// apply is used only for replay into a private in-memory state. It prepares the
// same bounded mutation as live ingestion and commits only after validation.
func apply(previous flowSnapshot, b AttributedBatch, now time.Time) (flowSnapshot, error) {
	legacy := false
	if _, exists := previous.Cursors[b.ExporterID]; !exists && b.DatagramDigest != "" {
		legacy = hasLegacyExporter(previous, b.ExporterID)
	}
	mutation, err := prepareMutation(previous, b, now, legacy)
	if err != nil {
		return previous, err
	}
	commitMutation(&previous, mutation)
	return previous, nil
}
func (s *Store) Ingest(b AttributedBatch) error {
	_, err := s.IngestWithResult(b)
	return err
}
func (s *Store) IngestWithResult(b AttributedBatch) (Classification, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result Classification
	err := s.ingestLocked(b, &result)
	return result, err
}
func (s *Store) ingestLocked(b AttributedBatch, result *Classification) error {
	if s.closed || s.maintenanceErr != nil {
		return errors.New("sFlow store is closed or needs recovery")
	}
	if err := validateAttributed(b); err != nil {
		return err
	}
	if !validDigest(b.DatagramDigest) {
		return errors.New("new sFlow ingestion requires a safe datagram digest")
	}
	now := time.Now().UTC()
	if b.ReceivedAt.Before(now.Add(-5*time.Minute)) || b.ReceivedAt.After(now.Add(30*time.Second)) {
		return errors.New("sFlow ingest receipt timestamp outside allowed window")
	}
	if old, ok := s.state.Seen[b.ID]; ok {
		if old.Checksum == digest(b) {
			*result = Classification{SequenceState: "duplicate"}
			return nil
		}
		return errors.New("sFlow batch ID conflict")
	}
	mutation, err := prepareMutation(s.state, b, now, s.exporterHistory[b.ExporterID])
	if err != nil {
		return err
	}
	*result = mutation.Classification
	if s.state.Sequence == ^uint64(0) {
		return errors.New("sFlow WAL sequence exhausted")
	}
	nextSequence := s.state.Sequence + 1
	frame := flowFrame{nextSequence, mutation.Seen.Checksum, b}
	raw, err := json.Marshal(frame)
	if err != nil {
		return errors.New("sFlow WAL encoding failed")
	}
	raw = append(raw, '\n')
	if len(raw) > 1<<20 {
		return errors.New("sFlow WAL record limit exceeded")
	}
	if s.stateBytes+mutation.ByteChange+s.snapshotBytes+s.walBytes+int64(len(raw)) > MaxStoreBytes {
		return errors.New("sFlow storage byte quota exceeded")
	}
	previousSize := s.walBytes
	if _, err = s.wal.Write(raw); err == nil {
		err = s.wal.Sync()
	}
	if err != nil {
		if e := s.wal.Truncate(previousSize); e != nil {
			s.maintenanceErr = e
		} else if e := s.wal.Sync(); e != nil {
			s.maintenanceErr = e
		}
		_, _ = s.wal.Seek(0, io.SeekEnd)
		return errors.New("sFlow durable ingest failed")
	}
	s.walBytes += int64(len(raw))
	commitMutation(&s.state, mutation)
	s.state.Sequence = nextSequence
	s.stateBytes += mutation.ByteChange
	s.exporterHistory[b.ExporterID] = true
	if s.walBytes >= maxWALBytes {
		s.maintenanceErr = s.compactLocked()
		if s.maintenanceErr != nil {
			return errors.New("sFlow ingested durably but compaction requires recovery")
		}
	}
	return nil
}
func (s *Store) compactLocked() error {
	raw, err := json.Marshal(flowDiskSnapshot{s.state, digest(s.state)})
	if err != nil || int64(len(raw))+s.snapshotBytes+s.walBytes > MaxStoreBytes {
		return errors.New("sFlow snapshot exceeds storage quota including atomic replacement")
	}
	f, err := os.CreateTemp(s.dir, ".snapshot-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(s.dir, "flows.snapshot.json")); err != nil {
		return err
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return err
	}
	s.snapshotBytes = int64(len(raw))
	if err = s.wal.Truncate(0); err != nil {
		return err
	}
	if _, err = s.wal.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err = s.wal.Sync(); err != nil {
		return err
	}
	s.walBytes = 0
	return nil
}
func (s *Store) maintain() {
	defer close(s.done)
	timer := time.NewTicker(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-timer.C:
			s.mu.Lock()
			if !s.closed && s.maintenanceErr == nil {
				prune(&s.state, now.UTC())
				s.maintenanceErr = s.rebuildAccountingLocked()
				if s.maintenanceErr == nil {
					s.maintenanceErr = s.compactLocked()
				}
			}
			s.mu.Unlock()
		}
	}
}
func (s *Store) Query(q Query) (Result, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Result{SourceKind: "sampled", Items: []Bucket{}, Resolution: q.Resolution}
	if s.closed {
		return out, errors.New("sFlow store is closed")
	}
	retention := FiveMinuteRetention
	if q.Resolution == "1d" {
		retention = DailyRetention
	} else if q.Resolution != "5m" {
		return out, errors.New("sFlow resolution must be 5m or 1d")
	}
	out.RetentionSeconds = int64(retention.Seconds())
	if q.From.IsZero() || q.To.IsZero() || q.From.After(q.To) || q.To.After(time.Now().Add(30*time.Second)) || q.From.Before(time.Now().Add(-retention-time.Minute)) || q.Limit < 1 || q.Limit > 10000 {
		return out, errors.New("sFlow query range or row limit invalid")
	}
	if q.Family != 0 && q.Family != 4 && q.Family != 6 {
		return out, errors.New("invalid sFlow query family")
	}
	if q.MemberID != "" && (!identifier.MatchString(q.MemberID) || !validDigest(q.MemberRevision)) || q.MemberID == "" && q.MemberRevision != "" {
		return out, errors.New("member history requires its current entity revision")
	}
	if len(q.SwitchIDs) > maxExporters {
		return out, errors.New("sFlow authorized switch set exceeds bound")
	}
	authorized := map[string]bool{}
	for _, id := range q.SwitchIDs {
		if !identifier.MatchString(id) || authorized[id] {
			return out, errors.New("invalid authorized switch set")
		}
		authorized[id] = true
	}
	step := 5 * time.Minute
	if q.Resolution == "1d" {
		step = 24 * time.Hour
	}
	out.From = q.From.UTC().Truncate(step)
	out.To = q.To.UTC().Truncate(step)
	if !out.To.Equal(q.To) {
		out.To = out.To.Add(step)
	}
	for _, b := range s.state.Rows {
		if b.Resolution != q.Resolution || len(authorized) > 0 && !authorized[b.SwitchID] || q.ExporterID != "" && q.ExporterID != b.ExporterID || q.SwitchID != "" && q.SwitchID != b.SwitchID || q.VLANID != "" && q.VLANID != b.VLANID || q.Family != 0 && q.Family != b.Family || q.MemberID != "" && (b.UnattributedReason != "" || !((q.MemberID == b.SourceMemberID && q.MemberRevision == b.SourceMemberRevision) || (q.MemberID == b.DestinationMemberID && q.MemberRevision == b.DestinationMemberRevision))) {
			continue
		}
		if out.OldestRetainedAt == nil || b.Start.Before(*out.OldestRetainedAt) {
			at := b.Start
			out.OldestRetainedAt = &at
		}
		if out.NewestRetainedAt == nil || b.Start.After(*out.NewestRetainedAt) {
			at := b.Start
			out.NewestRetainedAt = &at
		}
		if b.Start.Before(out.From) || !b.Start.Before(out.To) {
			continue
		}
		if b.LastTCP179At != nil {
			at := *b.LastTCP179At
			b.LastTCP179At = &at
		}
		out.Items = append(out.Items, b)
	}
	sort.Slice(out.Items, func(i, j int) bool {
		if out.Items[i].Start.Equal(out.Items[j].Start) {
			return bucketKey(out.Items[i]) < bucketKey(out.Items[j])
		}
		return out.Items[i].Start.Before(out.Items[j].Start)
	})
	if len(out.Items) > q.Limit {
		out.Items = out.Items[:q.Limit]
		out.Truncated = true
	}
	if q.MemberID == "" && q.VLANID == "" && q.Family == 0 {
		for _, v := range s.state.Quality {
			if len(authorized) > 0 && !authorized[v.SwitchID] || v.Start.Before(q.From.UTC().Truncate(24*time.Hour)) || v.Start.After(q.To) || q.ExporterID != "" && q.ExporterID != v.ExporterID || q.SwitchID != "" && q.SwitchID != v.SwitchID {
				continue
			}
			out.Quality = append(out.Quality, v)
		}
		sort.Slice(out.Quality, func(i, j int) bool {
			if out.Quality[i].Start.Equal(out.Quality[j].Start) {
				return qualityKey(out.Quality[i]) < qualityKey(out.Quality[j])
			}
			return out.Quality[i].Start.Before(out.Quality[j].Start)
		})
		if len(out.Quality) > q.Limit {
			out.Quality = out.Quality[:q.Limit]
			out.Truncated = true
		}
	}
	return out, nil
}
func (s *Store) Usage() Usage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Usage{Rows: len(s.state.Rows) + len(s.state.Quality), Bytes: s.snapshotBytes + s.walBytes, MaxRows: MaxAggregateRows, MaxBytes: MaxStoreBytes, FiveMinuteRetentionSeconds: int64(FiveMinuteRetention.Seconds()), DailyRetentionSeconds: int64(DailyRetention.Seconds())}
}
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stop)
	err := errors.Join(s.maintenanceErr, s.compactLocked())
	err = errors.Join(err, s.wal.Close(), s.lock.Close())
	s.mu.Unlock()
	<-s.done
	return err
}
