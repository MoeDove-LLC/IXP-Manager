// Package telemetry persists observed counters separately from control-plane
// records. Every acknowledged batch is synced to a checksummed append journal.
package telemetry

import (
	"container/heap"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/traffic"
)

const Retention = 24 * time.Hour
const StaleAfter = 120 * time.Second
const MaxPointsPerSeries = 2880
const MaxPoints = 100000
const maxEntities = 4096
const maxRecord = 1 << 20

type PortSample struct {
	Dimensions        traffic.Dimensions   `json:"dimensions,omitempty"`
	Unicast           *traffic.CounterPair `json:"unicast,omitempty"`
	Multicast         *traffic.CounterPair `json:"multicast,omitempty"`
	Broadcast         *traffic.CounterPair `json:"broadcast,omitempty"`
	Errors            *traffic.CounterPair `json:"errors,omitempty"`
	Discards          *traffic.CounterPair `json:"discards,omitempty"`
	OwnershipRevision string               `json:"ownershipRevision,omitempty"`
	MemberID          string               `json:"memberId,omitempty"`
	LinkSpeedMbps     uint64               `json:"linkSpeedMbps,omitempty"`
	PortID            string               `json:"portId"`
	InOctets          string               `json:"inOctets"`
	OutOctets         string               `json:"outOctets"`
	OperStatus        string               `json:"operStatus"`
	CounterBits       int                  `json:"counterBits,omitempty"`
	CounterEpoch      string               `json:"counterEpoch,omitempty"`
}
type SessionSample struct {
	NeighborAddress   string  `json:"neighborAddress,omitempty"`
	NeighborASN       uint32  `json:"neighborAsn,omitempty"`
	OwnershipRevision string  `json:"ownershipRevision,omitempty"`
	MemberID          string  `json:"memberId,omitempty"`
	PeerID            string  `json:"peerId"`
	State             string  `json:"state"`
	ReceivedPrefixes  *uint64 `json:"receivedPrefixes,omitempty"`
}
type Batch struct {
	RouterID       string          `json:"routerId"`
	ObservedAt     time.Time       `json:"observedAt"`
	Ports          []PortSample    `json:"ports"`
	Sessions       []SessionSample `json:"sessions"`
	RouterIdentity *RouterIdentity `json:"routerIdentity,omitempty"`
}
type PortPoint struct {
	PortSample
	RouterID       string    `json:"routerId"`
	ObservedAt     time.Time `json:"observedAt"`
	InBps          *float64  `json:"inBps"`
	OutBps         *float64  `json:"outBps"`
	CounterReset   bool      `json:"counterReset"`
	CounterWrapped bool      `json:"counterWrapped"`
}
type SessionPoint struct {
	SessionSample
	RouterID   string    `json:"routerId"`
	ObservedAt time.Time `json:"observedAt"`
}
type Snapshot struct {
	Ports    map[string]PortPoint
	Sessions map[string]SessionPoint
}
type diskState struct {
	Version  int                       `json:"version"`
	Sequence uint64                    `json:"sequence,omitempty"`
	Archive  archiveState              `json:"archive,omitempty"`
	Ports    map[string][]PortPoint    `json:"ports"`
	Sessions map[string][]SessionPoint `json:"sessions"`
}
type Store struct {
	failed        error
	mu            sync.RWMutex
	dir           string
	lock, journal *os.File
	state         diskState
	records       int
	closed        bool
	archive       archiveRuntime
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("telemetry directory must be a real directory")
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	if info, e := lock.Stat(); e != nil || !info.Mode().IsRegular() {
		lock.Close()
		return nil, errors.New("telemetry lock must be regular")
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("telemetry directory is in use: %w", err)
	}
	s := &Store{dir: dir, lock: lock, state: diskState{Version: 2, Ports: map[string][]PortPoint{}, Sessions: map[string][]SessionPoint{}}}
	fail := func(err error) (*Store, error) { s.Close(); return nil, err }
	raw, err := readBoundedRegular(filepath.Join(dir, "snapshot.json"), 64<<20)
	if err == nil {
		if len(raw) > 64<<20 {
			return fail(errors.New("telemetry snapshot exceeds 64 MiB"))
		}
		if err = json.Unmarshal(raw, &s.state); err != nil {
			return fail(err)
		}
	} else if !os.IsNotExist(err) {
		return fail(err)
	}
	if (s.state.Version != 1 && s.state.Version != 2) || s.state.Ports == nil || s.state.Sessions == nil {
		return fail(errors.New("invalid telemetry snapshot"))
	}
	if err = validateRawSnapshot(s.state); err != nil {
		return fail(err)
	}
	if err = s.initArchive(); err != nil {
		return fail(err)
	}
	journal, err := os.OpenFile(filepath.Join(dir, "journal.bin"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return fail(err)
	}
	if st, e := journal.Stat(); e != nil || !st.Mode().IsRegular() || st.Size() > 256<<20 {
		journal.Close()
		return fail(errors.New("telemetry journal must be regular and at most 256 MiB"))
	}
	s.journal = journal
	directory, err := os.Open(dir)
	if err != nil {
		return fail(err)
	}
	err = directory.Sync()
	directory.Close()
	if err != nil {
		return fail(err)
	}
	if err = s.replay(); err != nil {
		return fail(err)
	}
	if s.state.Version == 1 {
		s.state.Version = 2
		if err = s.compact(); err != nil {
			return fail(err)
		}
	}
	s.prune(time.Now())
	return s, nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var err error
	if s.journal != nil {
		err = s.journal.Close()
	}
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		if e := s.lock.Close(); err == nil {
			err = e
		}
	}
	return err
}
func (s *Store) replay() error {
	var offset int64
	for {
		var header [36]byte
		n, err := io.ReadFull(s.journal, header[:])
		if err == io.EOF && n == 0 {
			break
		}
		if err == io.ErrUnexpectedEOF {
			return s.journal.Truncate(offset)
		}
		if err != nil {
			return err
		}
		size := binary.BigEndian.Uint32(header[:4])
		if size == 0 || size > maxRecord {
			return errors.New("invalid telemetry journal frame length")
		}
		raw := make([]byte, size)
		if _, err = io.ReadFull(s.journal, raw); err == io.EOF || err == io.ErrUnexpectedEOF {
			return s.journal.Truncate(offset)
		} else if err != nil {
			return err
		}
		hash := sha256.Sum256(raw)
		if !equal(hash[:], header[4:]) {
			return errors.New("telemetry journal checksum mismatch")
		}
		var event journalEvent
		if err = json.Unmarshal(raw, &event); err != nil {
			return err
		}
		var batch Batch
		if event.Version == 2 {
			batch = event.Batch
			if err = validateBatch(batch, false); err != nil {
				return fmt.Errorf("invalid telemetry event: %w", err)
			}
			if event.Sequence == 0 || event.ReceivedAt.IsZero() {
				return errors.New("invalid sequenced telemetry event")
			}
			if event.Sequence <= s.state.Sequence {
				offset += 36 + int64(size)
				continue
			}
			if event.Sequence != s.state.Sequence+1 {
				return errors.New("telemetry journal sequence gap")
			}
		} else if event.Version == 0 {
			if s.state.Version == 2 && s.state.Sequence > 0 {
				offset += 36 + int64(size)
				continue
			}
			if err = json.Unmarshal(raw, &batch); err != nil {
				return err
			}
			s.state.Version = 1
		} else {
			return errors.New("unsupported telemetry journal version")
		}
		if err = validateBatch(batch, false); err != nil {
			return fmt.Errorf("invalid telemetry journal: %w", err)
		}
		// A snapshot may already include this journal prefix if a crash happened
		// between snapshot rename and journal truncation. Replay skips old points.
		if event.Version == 2 {
			if s.state.Archive.StartedAt.IsZero() {
				s.state.Archive.StartedAt = event.ReceivedAt
			}
			s.archiveBatch(batch)
			s.state.Sequence = event.Sequence
		}
		s.apply(batch)
		s.records++
		offset += 36 + int64(size)
	}
	_, err := s.journal.Seek(0, io.SeekEnd)
	return err
}
func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func counter(raw string, bits int) (uint64, error) {
	v, err := strconv.ParseUint(raw, 10, bits)
	if err != nil || strconv.FormatUint(v, 10) != raw {
		return 0, errors.New("octet counters must be canonical uint decimal strings")
	}
	return v, nil
}
func sameSession(a, b SessionSample) bool {
	if a.PeerID != b.PeerID || a.MemberID != b.MemberID || a.State != b.State || a.NeighborAddress != b.NeighborAddress || a.NeighborASN != b.NeighborASN || a.OwnershipRevision != b.OwnershipRevision {
		return false
	}
	if a.ReceivedPrefixes == nil || b.ReceivedPrefixes == nil {
		return a.ReceivedPrefixes == nil && b.ReceivedPrefixes == nil
	}
	return *a.ReceivedPrefixes == *b.ReceivedPrefixes
}
func NormalizeNeighborAddress(raw string) (string, error) {
	if len(raw) > 100 {
		return "", errors.New("observed neighbor address exceeds limit")
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil || addr.Zone() != "" || addr.Is4In6() || addr.IsUnspecified() || addr.IsMulticast() {
		return "", errors.New("invalid observed neighbor address")
	}
	return addr.String(), nil
}

// validateSessionSample permits identity-less legacy journal records only when
// strict is false. Newly ingested known states must carry their observed peer.
func ValidateSessionSample(p SessionSample, strict bool) error {
	if p.PeerID == "" || len(p.PeerID) > 100 {
		return errors.New("invalid peer ID")
	}
	switch p.State {
	case "Established", "Idle", "Connect", "Active", "OpenSent", "OpenConfirm", "unknown":
	default:
		return errors.New("invalid BGP session state")
	}
	if p.ReceivedPrefixes != nil && *p.ReceivedPrefixes > math.MaxUint32 {
		return errors.New("receivedPrefixes exceeds uint32")
	}
	if p.NeighborAddress == "" && p.NeighborASN == 0 {
		if strict && p.State != "unknown" {
			return errors.New("known BGP state requires observed neighbor identity")
		}
		return nil
	}
	if p.NeighborAddress == "" || p.NeighborASN == 0 || p.NeighborASN == math.MaxUint32 {
		return errors.New("incomplete or invalid observed neighbor identity")
	}
	_, err := NormalizeNeighborAddress(p.NeighborAddress)
	return err
}
func validateBatch(b Batch, strict bool) error {
	if b.RouterID == "" || len(b.RouterID) > 100 || b.ObservedAt.IsZero() {
		return errors.New("routerId and observedAt are required")
	}
	if len(b.Ports)+len(b.Sessions) == 0 || len(b.Ports)+len(b.Sessions) > 2048 {
		return errors.New("batch must contain 1..2048 observations")
	}
	seen := map[string]bool{}
	for _, p := range b.Ports {
		if err := validateTrafficFields(p); err != nil {
			return err
		}
		if p.PortID == "" || len(p.PortID) > 100 || seen["p"+p.PortID] {
			return errors.New("port IDs must be distinct")
		}
		seen["p"+p.PortID] = true
		bits := p.CounterBits
		if bits == 0 {
			bits = 64
		}
		if bits != 32 && bits != 64 {
			return errors.New("counterBits must be 32 or 64")
		}
		if _, err := counter(p.InOctets, bits); err != nil {
			return err
		}
		if _, err := counter(p.OutOctets, bits); err != nil {
			return err
		}
		if p.OperStatus != "up" && p.OperStatus != "down" && p.OperStatus != "unknown" {
			return errors.New("invalid port operational status")
		}
		if len(p.CounterEpoch) > 128 || strings.ContainsAny(p.CounterEpoch, "\x00\r\n") {
			return errors.New("invalid counter epoch")
		}
	}
	for _, p := range b.Sessions {
		if p.PeerID == "" || len(p.PeerID) > 100 || seen["s"+p.PeerID] {
			return errors.New("peer IDs must be distinct")
		}
		seen["s"+p.PeerID] = true
		if err := ValidateSessionSample(p, strict); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) Ingest(b Batch, now time.Time) error {
	b.ObservedAt = b.ObservedAt.UTC()
	b.Ports = append([]PortSample(nil), b.Ports...)
	for i := range b.Ports {
		b.Ports[i] = copyPortSample(b.Ports[i])
		normalizePortSample(&b.Ports[i])
	}
	b.Sessions = append([]SessionSample(nil), b.Sessions...)
	for i := range b.Sessions {
		b.Sessions[i] = copySessionSample(b.Sessions[i])
	}
	if err := validateBatch(b, true); err != nil {
		return err
	}
	for i := range b.Sessions {
		if b.Sessions[i].NeighborAddress != "" {
			b.Sessions[i].NeighborAddress, _ = NormalizeNeighborAddress(b.Sessions[i].NeighborAddress)
		}
	}
	if b.ObservedAt.After(now.Add(30*time.Second)) || b.ObservedAt.Before(now.Add(-10*time.Minute)) {
		return errors.New("observation must be within 10 minutes past and 30 seconds future")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("telemetry store is closed")
	}
	if s.failed != nil {
		return fmt.Errorf("telemetry journal requires restart after I/O failure: %w", s.failed)
	}
	if s.state.Sequence == math.MaxUint64 {
		return errors.New("telemetry journal sequence exhausted")
	}
	newEntities := 0
	fresh := false
	for _, p := range b.Ports {
		series := s.state.Ports[p.PortID]
		if len(series) == 0 {
			newEntities++
			fresh = true
			continue
		}
		last := series[len(series)-1]
		if b.ObservedAt.Equal(last.ObservedAt) && samePortSample(last.PortSample, p) && last.RouterID == b.RouterID {
			continue
		}
		if b.ObservedAt.Sub(last.ObservedAt) < time.Second {
			return errors.New("port observations must increase by at least one second")
		}
		fresh = true
	}
	for _, p := range b.Sessions {
		series := s.state.Sessions[p.PeerID]
		if len(series) == 0 {
			newEntities++
			fresh = true
			continue
		}
		last := series[len(series)-1]
		if b.ObservedAt.Equal(last.ObservedAt) && sameSession(last.SessionSample, p) && last.RouterID == b.RouterID {
			continue
		}
		if b.ObservedAt.Sub(last.ObservedAt) < time.Second {
			return errors.New("session observations must increase by at least one second")
		}
		fresh = true
	}
	if !fresh {
		return nil
	}
	s.prune(now)
	if len(s.state.Ports)+len(s.state.Sessions)+newEntities > maxEntities {
		return errors.New("telemetry entity capacity reached")
	}
	if s.records >= 128 || s.archive.count >= maxArchivePending {
		if err := s.compact(); err != nil {
			return err
		}
	}
	if err := s.archivePreflight(b); err != nil {
		return err
	}
	event := journalEvent{Version: 2, Sequence: s.state.Sequence + 1, ReceivedAt: now.UTC(), Batch: b}
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if len(raw) > maxRecord {
		return errors.New("telemetry batch exceeds journal limit")
	}
	frame := make([]byte, 36+len(raw))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(raw)))
	hash := sha256.Sum256(raw)
	copy(frame[4:36], hash[:])
	copy(frame[36:], raw)
	offset, err := s.journal.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	n, err := s.journal.Write(frame)
	if err != nil || n != len(frame) {
		_ = s.journal.Truncate(offset)
		if err == nil {
			err = io.ErrShortWrite
		}
		return err
	}
	if err = s.journal.Sync(); err != nil {
		s.failed = err
		return err
	}
	if s.state.Archive.StartedAt.IsZero() {
		s.state.Archive.StartedAt = now.UTC()
	}
	s.archiveBatch(b)
	s.state.Sequence = event.Sequence
	s.apply(b)
	s.prune(now)
	s.records++
	return nil
}
func delta(old, next uint64, bits int) (uint64, bool, bool) {
	if next >= old {
		return next - old, false, false
	}
	max := uint64(math.MaxUint64)
	if bits == 32 {
		max = math.MaxUint32
	}
	if old > max-max/16 && next < max/16 {
		return max - old + 1 + next, false, true
	}
	return 0, true, false
}
func copyPortPoint(p PortPoint) PortPoint {
	p.PortSample = copyPortSample(p.PortSample)
	if p.InBps != nil {
		v := *p.InBps
		p.InBps = &v
	}
	if p.OutBps != nil {
		v := *p.OutBps
		p.OutBps = &v
	}
	return p
}
func copySessionSample(p SessionSample) SessionSample {
	if p.ReceivedPrefixes != nil {
		v := *p.ReceivedPrefixes
		p.ReceivedPrefixes = &v
	}
	return p
}
func (s *Store) apply(b Batch) {
	for _, p := range b.Ports {
		points := s.state.Ports[p.PortID]
		if len(points) > 0 && !b.ObservedAt.After(points[len(points)-1].ObservedAt) {
			continue
		}
		point := PortPoint{PortSample: copyPortSample(p), RouterID: b.RouterID, ObservedAt: b.ObservedAt}
		if len(points) > 0 {
			old := points[len(points)-1]
			seconds := b.ObservedAt.Sub(old.ObservedAt).Seconds()
			bits := p.CounterBits
			if bits == 0 {
				bits = 64
			}
			oldBits := old.CounterBits
			if oldBits == 0 {
				oldBits = 64
			}
			if old.CounterEpoch != p.CounterEpoch || oldBits != bits || old.RouterID != b.RouterID || old.MemberID != p.MemberID || old.LinkSpeedMbps != p.LinkSpeedMbps {
				point.CounterReset = true
			} else {
				a, _ := counter(old.InOctets, bits)
				v, _ := counter(p.InOctets, bits)
				in, resetIn, wrapIn := delta(a, v, bits)
				a, _ = counter(old.OutOctets, bits)
				v, _ = counter(p.OutOctets, bits)
				out, resetOut, wrapOut := delta(a, v, bits)
				point.CounterReset = resetIn || resetOut
				point.CounterWrapped = wrapIn || wrapOut
				unambiguous := bits == 64 || (p.LinkSpeedMbps > 0 && seconds*float64(p.LinkSpeedMbps)*1e6/8 < float64(uint64(math.MaxUint32)+1))
				if !point.CounterReset && unambiguous && seconds > 0 && seconds <= StaleAfter.Seconds() {
					inRate := float64(in) * 8 / seconds
					outRate := float64(out) * 8 / seconds
					point.InBps = &inRate
					point.OutBps = &outRate
				}
			}
		}
		points = append(points, point)
		if len(points) > MaxPointsPerSeries {
			points = append([]PortPoint(nil), points[len(points)-MaxPointsPerSeries:]...)
		}
		s.state.Ports[p.PortID] = points
	}
	for _, p := range b.Sessions {
		points := s.state.Sessions[p.PeerID]
		if len(points) > 0 && !b.ObservedAt.After(points[len(points)-1].ObservedAt) {
			continue
		}
		points = append(points, SessionPoint{SessionSample: copySessionSample(p), RouterID: b.RouterID, ObservedAt: b.ObservedAt})
		if len(points) > MaxPointsPerSeries {
			points = append([]SessionPoint(nil), points[len(points)-MaxPointsPerSeries:]...)
		}
		s.state.Sessions[p.PeerID] = points
	}
}
func (s *Store) compact() (resultErr error) {
	defer func() {
		if resultErr != nil {
			s.failed = resultErr
		}
	}()
	nextArchive, err := s.prepareArchive(time.Now())
	if err != nil {
		return err
	}
	nextState := s.state
	nextState.Version = 2
	nextState.Archive = nextArchive
	raw, err := json.Marshal(nextState)
	if err != nil {
		return err
	}
	if len(raw) > 64<<20 {
		return errors.New("telemetry snapshot exceeds capacity")
	}
	f, err := os.CreateTemp(s.dir, ".snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(s.dir, "snapshot.json")); err != nil {
		return err
	}
	d, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	d.Close()
	if err != nil {
		return err
	}
	s.state = nextState
	s.archive.pending = map[string]map[string]traffic.Row{}
	s.archive.count = 0
	for id, c := range s.archive.cohorts {
		if !c.Last.After(time.Now().Add(-retention(traffic.Day))) {
			delete(s.archive.cohorts, id)
		}
	}
	if err = s.journal.Truncate(0); err != nil {
		return err
	}
	if _, err = s.journal.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err = s.journal.Sync(); err != nil {
		return err
	}
	s.records = 0
	return s.cleanArchive()
}
func (s *Store) Snapshot(now time.Time) Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Snapshot{Ports: map[string]PortPoint{}, Sessions: map[string]SessionPoint{}}
	for id, p := range s.state.Ports {
		if len(p) > 0 && p[len(p)-1].ObservedAt.After(now.Add(-Retention)) {
			out.Ports[id] = copyPortPoint(p[len(p)-1])
		}
	}
	for id, p := range s.state.Sessions {
		if len(p) > 0 && p[len(p)-1].ObservedAt.After(now.Add(-Retention)) {
			last := p[len(p)-1]
			last.SessionSample = copySessionSample(last.SessionSample)
			out.Sessions[id] = last
		}
	}
	return out
}
func (s *Store) Series(id string, from, to time.Time) []PortPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []PortPoint{}
	for _, p := range s.state.Ports[id] {
		if !p.ObservedAt.Before(from) && !p.ObservedAt.After(to) {
			out = append(out, copyPortPoint(p))
		}
	}
	return out
}

type oldest struct {
	id   string
	port bool
	at   time.Time
}
type oldestHeap []oldest

func (h oldestHeap) Len() int           { return len(h) }
func (h oldestHeap) Less(i, j int) bool { return h[i].at.Before(h[j].at) }
func (h oldestHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *oldestHeap) Push(v any)        { *h = append(*h, v.(oldest)) }
func (h *oldestHeap) Pop() any          { v := (*h)[len(*h)-1]; *h = (*h)[:len(*h)-1]; return v }
func (s *Store) prune(now time.Time) {
	count := 0
	h := oldestHeap{}
	for id, p := range s.state.Ports {
		i := 0
		for i < len(p) && p[i].ObservedAt.Before(now.Add(-Retention)) {
			i++
		}
		if i == len(p) {
			delete(s.state.Ports, id)
			continue
		}
		if i > 0 {
			p = append([]PortPoint(nil), p[i:]...)
			s.state.Ports[id] = p
		}
		count += len(p)
		h = append(h, oldest{id: id, port: true, at: p[0].ObservedAt})
	}
	for id, p := range s.state.Sessions {
		i := 0
		for i < len(p) && p[i].ObservedAt.Before(now.Add(-Retention)) {
			i++
		}
		if i == len(p) {
			delete(s.state.Sessions, id)
			continue
		}
		if i > 0 {
			p = append([]SessionPoint(nil), p[i:]...)
			s.state.Sessions[id] = p
		}
		count += len(p)
		h = append(h, oldest{id: id, at: p[0].ObservedAt})
	}
	heap.Init(&h)
	for count > MaxPoints && h.Len() > 0 {
		v := heap.Pop(&h).(oldest)
		count--
		if v.port {
			p := s.state.Ports[v.id][1:]
			if len(p) == 0 {
				delete(s.state.Ports, v.id)
			} else {
				s.state.Ports[v.id] = p
				v.at = p[0].ObservedAt
				heap.Push(&h, v)
			}
		} else {
			p := s.state.Sessions[v.id][1:]
			if len(p) == 0 {
				delete(s.state.Sessions, v.id)
			} else {
				s.state.Sessions[v.id] = p
				v.at = p[0].ObservedAt
				heap.Push(&h, v)
			}
		}
	}
}
