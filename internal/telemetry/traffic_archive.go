package telemetry

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/traffic"
)

const archiveQuota int64 = 2 << 30

// Checkpoints retain at most 768 MiB compressed. The remainder of the hard
// 2-GiB directory quota is reserved for atomic replacement and its old version.
const archiveCheckpointQuota int64 = 768 << 20
const maxArchiveFiles = 20000
const maxArchiveDecoded = 64 << 20
const maxArchiveCompressed = 16 << 20
const maxArchivePending = 60000
const maxArchiveCohorts = 20000
const maxQueryDecoded = 512 << 20

type archiveRef struct {
	File       string    `json:"file"`
	Resolution string    `json:"resolution"`
	Day        string    `json:"day"`
	Shard      int       `json:"shard"`
	Size       int64     `json:"size"`
	Hash       string    `json:"hash"`
	First      time.Time `json:"first,omitempty"`
	Last       time.Time `json:"last,omitempty"`
}
type archivePartition struct {
	Version int           `json:"version"`
	Rows    []traffic.Row `json:"rows"`
}
type archiveCohort struct {
	Key     traffic.Key           `json:"key"`
	First   time.Time             `json:"first"`
	Last    time.Time             `json:"last"`
	Metrics map[string]metricSeen `json:"metrics,omitempty"`
}
type metricSeen struct {
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
}
type archiveState struct {
	Version   int                   `json:"version"`
	StartedAt time.Time             `json:"startedAt"`
	Refs      map[string]archiveRef `json:"refs"`
	Catalog   *archiveRef           `json:"catalog,omitempty"`
}
type archiveRuntime struct {
	pending    map[string]map[string]traffic.Row
	cohorts    map[string]archiveCohort
	count      int
	writeUsage *archiveUsage
}
type archiveUsage struct {
	bytes int64
	files int
}

func archiveDiskUsage(dir string) (archiveUsage, error) {
	entries, e := os.ReadDir(dir)
	if e != nil {
		return archiveUsage{}, e
	}
	usage := archiveUsage{files: len(entries)}
	for _, entry := range entries {
		info, e := entry.Info()
		if e != nil || !info.Mode().IsRegular() {
			return usage, errors.New("unsafe telemetry archive directory entry")
		}
		usage.bytes += info.Size()
	}
	return usage, nil
}

type journalEvent struct {
	Version    int       `json:"version"`
	Sequence   uint64    `json:"sequence"`
	ReceivedAt time.Time `json:"receivedAt"`
	Batch      Batch     `json:"batch"`
}

var archiveName = regexp.MustCompile(`^(?:5m|1h|1d)-[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9]{2}-[0-9a-f]{64}\.json\.gz$|^catalog-[0-9a-f]{64}\.json\.gz$`)

func emptyArchive() archiveState { return archiveState{Version: 1, Refs: map[string]archiveRef{}} }
func validateArchiveManifest(a archiveState) error {
	if a.Version == 0 && len(a.Refs) == 0 && a.Catalog == nil {
		return nil
	}
	if a.Version != 1 || a.Refs == nil || len(a.Refs) > maxArchiveFiles {
		return errors.New("invalid telemetry archive manifest")
	}
	if len(a.Refs) > 0 && (a.Catalog == nil || a.StartedAt.IsZero()) {
		return errors.New("telemetry archive attribution catalog is missing")
	}
	var total int64
	for _, r := range a.Refs {
		if !validRef(r) {
			return errors.New("invalid telemetry archive reference")
		}
		total += r.Size
	}
	if total > archiveCheckpointQuota {
		return errors.New("telemetry archive checkpoint exceeds quota")
	}
	return nil
}
func (s *Store) initArchive() error {
	if s.state.Archive.Version == 0 {
		s.state.Archive = emptyArchive()
	}
	if e := validateArchiveManifest(s.state.Archive); e != nil {
		return e
	}
	dir := filepath.Join(s.dir, "archive")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	st, e := os.Lstat(dir)
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid telemetry archive directory")
	}
	s.archive = archiveRuntime{pending: map[string]map[string]traffic.Row{}, cohorts: map[string]archiveCohort{}}
	for key, ref := range s.state.Archive.Refs {
		if key != partitionKey(ref.Resolution, ref.Day, ref.Shard) || !validRef(ref) {
			return errors.New("invalid telemetry archive reference")
		}
		raw, e := readArchive(s.dir, ref)
		if e != nil {
			return e
		}
		var p archivePartition
		if e = json.Unmarshal(raw, &p); e != nil || p.Version != 1 {
			return errors.New("invalid telemetry archive partition")
		}
		if e = validatePartition(p, ref); e != nil {
			return e
		}
	}
	if s.state.Archive.Catalog != nil {
		raw, e := readArchive(s.dir, *s.state.Archive.Catalog)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(raw, &s.archive.cohorts); e != nil || s.archive.cohorts == nil || len(s.archive.cohorts) > maxArchiveCohorts {
			return errors.New("invalid telemetry archive catalog")
		}
		if e = validateCatalog(s.archive.cohorts); e != nil {
			return e
		}
		normalized := map[string]archiveCohort{}
		for _, c := range s.archive.cohorts {
			c.Key.CounterEpoch = ""
			id := cohortKey(c.Key)
			old, ok := normalized[id]
			if ok {
				if c.First.Before(old.First) {
					old.First = c.First
				}
				if c.Last.After(old.Last) {
					old.Last = c.Last
				}
				if old.Metrics == nil {
					old.Metrics = map[string]metricSeen{}
				}
				for metric, seen := range c.Metrics {
					v, found := old.Metrics[metric]
					if !found || seen.First.Before(v.First) {
						v.First = seen.First
					}
					if !found || seen.Last.After(v.Last) {
						v.Last = seen.Last
					}
					old.Metrics[metric] = v
				}
				c = old
			}
			normalized[id] = c
		}
		s.archive.cohorts = normalized
	}
	return s.cleanArchive()
}
func validRef(r archiveRef) bool {
	return archiveName.MatchString(r.File) && len(r.Hash) == 64 && strings.Contains(r.File, r.Hash) && r.Size > 0 && r.Size <= maxArchiveCompressed
}
func validateCatalog(catalog map[string]archiveCohort) error {
	for id, c := range catalog {
		if (id != cohortKey(c.Key) && id != intervalKey(c.Key)) || c.Key.PortID == "" || c.Key.SourceID == "" || c.First.IsZero() || c.Last.Before(c.First) || len(c.Key.Dimensions.ConnectionIDs) > 256 {
			return errors.New("invalid telemetry archive catalog attribution")
		}
		if len(c.Metrics) > 6 {
			return errors.New("invalid telemetry catalog metrics")
		}
		for metric, seen := range c.Metrics {
			switch metric {
			case traffic.Octets, traffic.Unicast, traffic.Multicast, traffic.Broadcast, traffic.Errors, traffic.Discards:
			default:
				return errors.New("invalid telemetry catalog metric")
			}
			if seen.First.IsZero() || seen.Last.Before(seen.First) || seen.First.Before(c.First) || seen.Last.After(c.Last) {
				return errors.New("invalid telemetry metric chronology")
			}
		}
	}
	return nil
}
func readArchive(dir string, ref archiveRef) ([]byte, error) {
	if !validRef(ref) {
		return nil, errors.New("invalid telemetry archive file reference")
	}
	root, e := os.OpenRoot(filepath.Join(dir, "archive"))
	if e != nil {
		return nil, e
	}
	defer root.Close()
	f, e := root.OpenFile(ref.File, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() != ref.Size {
		return nil, errors.New("telemetry archive file metadata mismatch")
	}
	raw, e := io.ReadAll(io.LimitReader(f, maxArchiveCompressed+1))
	if e != nil {
		return nil, e
	}
	h := sha256.Sum256(raw)
	if hex.EncodeToString(h[:]) != ref.Hash {
		return nil, errors.New("telemetry archive checksum mismatch")
	}
	z, e := gzip.NewReader(bytes.NewReader(raw))
	if e != nil {
		return nil, errors.New("invalid telemetry archive compression")
	}
	decoded, e := io.ReadAll(io.LimitReader(z, maxArchiveDecoded+1))
	ce := z.Close()
	if e != nil || ce != nil || len(decoded) > maxArchiveDecoded {
		return nil, errors.New("telemetry archive exceeds decoding limit or is corrupt")
	}
	return decoded, nil
}
func (s *Store) writeArchive(prefix string, v any) (archiveRef, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return archiveRef{}, e
	}
	if len(raw) > maxArchiveDecoded {
		return archiveRef{}, errors.New("telemetry archive partition exceeds 64 MiB")
	}
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	if _, e = z.Write(raw); e != nil {
		return archiveRef{}, e
	}
	if e = z.Close(); e != nil {
		return archiveRef{}, e
	}
	if buf.Len() > maxArchiveCompressed {
		return archiveRef{}, errors.New("telemetry compressed partition exceeds 16 MiB")
	}
	sum := sha256.Sum256(buf.Bytes())
	hash := hex.EncodeToString(sum[:])
	name := prefix + "-" + hash + ".json.gz"
	dir := filepath.Join(s.dir, "archive")
	ref := archiveRef{File: name, Hash: hash, Size: int64(buf.Len())}
	if _, e = os.Lstat(filepath.Join(dir, name)); e == nil {
		_, e = readArchive(s.dir, ref)
		return ref, e
	} else if !os.IsNotExist(e) {
		return archiveRef{}, e
	}
	usage := s.archive.writeUsage
	if usage == nil {
		v, e := archiveDiskUsage(dir)
		if e != nil {
			return archiveRef{}, e
		}
		usage = &v
	}
	if usage.files+2 >= maxArchiveFiles*2 {
		return archiveRef{}, errors.New("telemetry archive directory file limit reached")
	}
	if usage.bytes+int64(buf.Len()) > archiveQuota {
		return archiveRef{}, errors.New("telemetry archive hard 2-GiB disk quota reached")
	}
	tmp, e := os.CreateTemp(dir, ".pending-*")
	if e != nil {
		return archiveRef{}, e
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, e = tmp.Write(buf.Bytes()); e != nil {
		return archiveRef{}, e
	}
	if e = tmp.Sync(); e != nil {
		return archiveRef{}, e
	}
	if e = tmp.Close(); e != nil {
		return archiveRef{}, e
	}
	dest := filepath.Join(dir, name)
	if e = os.Link(tmp.Name(), dest); e != nil {
		if !os.IsExist(e) {
			return archiveRef{}, e
		}
	} else {
		usage.files++
		usage.bytes += ref.Size
	}
	if _, e = readArchive(s.dir, ref); e != nil {
		return archiveRef{}, e
	}
	return ref, nil
}
func shards(res string) int {
	if res == traffic.FiveMinutes {
		return 64
	}
	if res == traffic.Hour {
		return 16
	}
	return 64
}
func partitionDate(res string, at time.Time) time.Time {
	at = at.UTC()
	if res == traffic.Day {
		return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return at.Truncate(24 * time.Hour)
}
func partitionEnd(res string, at time.Time) time.Time {
	at = partitionDate(res, at)
	if res == traffic.Day {
		return at.AddDate(0, 1, 0)
	}
	return at.Add(24 * time.Hour)
}
func partitionKey(res, day string, shard int) string {
	return fmt.Sprintf("%s-%s-%02d", res, day, shard)
}
func rowPartition(r traffic.Row) string {
	h := sha256.Sum256([]byte(r.Key.PortID))
	return partitionKey(r.Resolution, partitionDate(r.Resolution, r.Start).Format("2006-01-02"), int(h[0])%shards(r.Resolution))
}
func intervalKey(k traffic.Key) string { k.Metric = ""; return traffic.KeyID(k) }
func cohortKey(k traffic.Key) string   { k.CounterEpoch = ""; return intervalKey(k) }
func portMetrics(p PortSample) map[string]traffic.CounterPair {
	m := map[string]traffic.CounterPair{traffic.Octets: {In: p.InOctets, Out: p.OutOctets, Bits: p.CounterBits}}
	for k, pair := range map[string]*traffic.CounterPair{traffic.Unicast: p.Unicast, traffic.Multicast: p.Multicast, traffic.Broadcast: p.Broadcast, traffic.Errors: p.Errors, traffic.Discards: p.Discards} {
		if pair != nil {
			m[k] = *pair
		}
	}
	return m
}
func trafficKey(p PortSample, source, metric string) traffic.Key {
	return traffic.Key{PortID: p.PortID, MemberID: p.MemberID, SourceID: source, OwnershipRevision: p.OwnershipRevision, CounterEpoch: p.CounterEpoch, Dimensions: p.Dimensions, Metric: metric}
}
func (s *Store) archiveBatch(b Batch) {
	for _, p := range b.Ports {
		points := s.state.Ports[p.PortID]
		if len(points) > 0 && !b.ObservedAt.After(points[len(points)-1].ObservedAt) {
			continue
		}
		k := trafficKey(p, b.RouterID, "")
		ck := cohortKey(k)
		c, ok := s.archive.cohorts[ck]
		if !ok {
			k.CounterEpoch = ""
			c = archiveCohort{Key: k, First: b.ObservedAt}
		}
		c.Last = b.ObservedAt
		if c.Metrics == nil {
			c.Metrics = map[string]metricSeen{}
		}
		for metric := range portMetrics(p) {
			seen, ok := c.Metrics[metric]
			if !ok {
				seen.First = b.ObservedAt
			}
			seen.Last = b.ObservedAt
			c.Metrics[metric] = seen
		}
		s.archive.cohorts[ck] = c
		old := PortPoint{PortSample: p, RouterID: b.RouterID, ObservedAt: b.ObservedAt}
		hasOld := len(points) > 0
		if hasOld {
			old = points[len(points)-1]
		}
		same := hasOld && intervalKey(trafficKey(old.PortSample, old.RouterID, "")) == intervalKey(trafficKey(p, b.RouterID, "")) && old.LinkSpeedMbps == p.LinkSpeedMbps
		previous := portMetrics(old.PortSample)
		for metric, next := range portMetrics(p) {
			key := trafficKey(p, b.RouterID, metric)
			before := previous[metric]
			for _, row := range traffic.Interval(key, old.ObservedAt, b.ObservedAt, before, next, p.LinkSpeedMbps, same) {
				part := rowPartition(row)
				if s.archive.pending[part] == nil {
					s.archive.pending[part] = map[string]traffic.Row{}
				}
				id := traffic.RowID(row)
				existing, ok := s.archive.pending[part][id]
				if ok {
					traffic.Merge(&existing, row)
				} else {
					existing = traffic.CloneRow(row)
					s.archive.count++
				}
				s.archive.pending[part][id] = existing
			}
		}
	}
}
func (s *Store) archivePreflight(b Batch) error {
	newIDs := map[string]bool{}
	missingBaselines := map[string]bool{}
	for _, p := range b.Ports {
		if len(s.state.Ports[p.PortID]) == 0 {
			missingBaselines[p.PortID] = true
		}
		k := cohortKey(trafficKey(p, b.RouterID, ""))
		if _, ok := s.archive.cohorts[k]; !ok {
			newIDs[k] = true
		}
	}
	for _, c := range s.archive.cohorts {
		if missingBaselines[c.Key.PortID] && b.ObservedAt.Sub(c.Last) < time.Second {
			return errors.New("port observation predates its retained historical watermark")
		}
	}
	if len(s.archive.cohorts)+len(newIDs) > maxArchiveCohorts {
		return errors.New("telemetry historical attribution capacity reached (20000 cohorts)")
	}
	return nil
}
func retention(res string) time.Duration {
	switch res {
	case traffic.FiveMinutes:
		return 90 * 24 * time.Hour
	case traffic.Hour:
		return 400 * 24 * time.Hour
	default:
		return 3653 * 24 * time.Hour
	}
}
func (s *Store) prepareArchive(now time.Time) (archiveState, error) {
	next := archiveState{Version: 1, StartedAt: s.state.Archive.StartedAt, Refs: map[string]archiveRef{}}
	usage, e := archiveDiskUsage(filepath.Join(s.dir, "archive"))
	if e != nil {
		return next, e
	}
	s.archive.writeUsage = &usage
	defer func() { s.archive.writeUsage = nil }()
	for id, r := range s.state.Archive.Refs {
		day, e := time.Parse("2006-01-02", r.Day)
		if e != nil {
			return next, e
		}
		if partitionEnd(r.Resolution, day).After(now.Add(-retention(r.Resolution))) {
			next.Refs[id] = r
		}
	}
	keys := []string{}
	for key := range s.archive.pending {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		merged := map[string]traffic.Row{}
		if ref, ok := next.Refs[key]; ok {
			raw, e := readArchive(s.dir, ref)
			if e != nil {
				return next, e
			}
			var p archivePartition
			if e = json.Unmarshal(raw, &p); e != nil || p.Version != 1 {
				return next, errors.New("invalid archive partition")
			}
			for _, r := range p.Rows {
				merged[traffic.RowID(r)] = r
			}
		}
		for id, r := range s.archive.pending[key] {
			existing, ok := merged[id]
			if ok {
				traffic.Merge(&existing, r)
			} else {
				existing = r
			}
			merged[id] = existing
		}
		rows := make([]traffic.Row, 0, len(merged))
		for _, r := range merged {
			rows = append(rows, r)
		}
		traffic.SortRows(rows)
		if len(rows) == 0 {
			continue
		}
		res := rows[0].Resolution
		day := partitionDate(res, rows[0].Start).Format("2006-01-02")
		if !partitionEnd(res, rows[0].Start).After(now.Add(-retention(res))) {
			continue
		}
		ref, e := s.writeArchive(key, archivePartition{Version: 1, Rows: rows})
		if e != nil {
			return next, e
		}
		ref.Resolution = res
		ref.Day = day
		for _, r := range rows {
			if ref.First.IsZero() || r.Start.Before(ref.First) {
				ref.First = r.Start
			}
			if ref.Last.IsZero() || r.Start.After(ref.Last) {
				ref.Last = r.Start
			}
		}
		h := sha256.Sum256([]byte(rows[0].Key.PortID))
		ref.Shard = int(h[0]) % shards(res)
		next.Refs[key] = ref
	}
	// Quota pressure removes oldest fine-grained partitions first. Coarser tiers
	// survive independently and queries report their actual retained start.
	refs := []archiveRef{}
	var size int64
	for _, r := range next.Refs {
		refs = append(refs, r)
		size += r.Size
	}
	sort.Slice(refs, func(i, j int) bool {
		a, b := refs[i], refs[j]
		if traffic.Duration(a.Resolution) != traffic.Duration(b.Resolution) {
			return traffic.Duration(a.Resolution) < traffic.Duration(b.Resolution)
		}
		return a.Day < b.Day
	})
	for _, r := range refs {
		if size <= archiveCheckpointQuota && len(next.Refs) < maxArchiveFiles-1 {
			break
		}
		// Remove a complete UTC day, not selected shards within that day. This
		// gives every physical source the same visible tier retention boundary.
		for id, old := range next.Refs {
			if old.Resolution == r.Resolution && old.Day == r.Day {
				delete(next.Refs, id)
				size -= old.Size
			}
		}
	}
	catalog := map[string]archiveCohort{}
	for id, c := range s.archive.cohorts {
		if c.Last.After(now.Add(-retention(traffic.Day))) {
			catalog[id] = c
		}
	}
	ref, e := s.writeArchive("catalog", catalog)
	if e != nil {
		return next, e
	}
	next.Catalog = &ref
	d, e := os.Open(filepath.Join(s.dir, "archive"))
	if e != nil {
		return next, e
	}
	e = d.Sync()
	d.Close()
	return next, e
}
func (s *Store) cleanArchive() error {
	keep := map[string]bool{}
	for _, r := range s.state.Archive.Refs {
		keep[r.File] = true
	}
	if s.state.Archive.Catalog != nil {
		keep[s.state.Archive.Catalog.File] = true
	}
	dir := filepath.Join(s.dir, "archive")
	entries, e := os.ReadDir(dir)
	if e != nil {
		return e
	}
	if len(entries) > maxArchiveFiles*2 {
		return errors.New("telemetry archive directory exceeds file limit")
	}
	for _, entry := range entries {
		if keep[entry.Name()] {
			continue
		}
		if !archiveName.MatchString(entry.Name()) && !strings.HasPrefix(entry.Name(), ".pending-") {
			return errors.New("unexpected telemetry archive file")
		}
		info, e := entry.Info()
		if e != nil || !info.Mode().IsRegular() {
			return errors.New("unsafe telemetry archive orphan")
		}
		if e = os.Remove(filepath.Join(dir, entry.Name())); e != nil {
			return e
		}
	}
	return nil
}

func (s *Store) QueryTraffic(q traffic.Query, now time.Time) (traffic.Result, error) {
	if e := traffic.ValidateQuery(q); e != nil {
		return traffic.Result{}, e
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return traffic.Result{}, errors.New("telemetry store is closed")
	}
	rows := []traffic.Row{}
	decoded := 0
	var first, last *time.Time
	var boundary *time.Time
	considerBoundary := func(at time.Time) {
		if !at.IsZero() && (boundary == nil || at.Before(*boundary)) {
			v := at
			boundary = &v
		}
	}
	add := func(r traffic.Row) error {
		if !r.Start.Add(traffic.Duration(r.Resolution)).After(now.Add(-retention(r.Resolution))) {
			return nil
		}
		if !traffic.Matches(r.Key, q) {
			return nil
		}
		if first == nil || r.Start.Before(*first) {
			v := r.Start
			first = &v
		}
		end := r.Start.Add(traffic.Duration(r.Resolution))
		if last == nil || end.After(*last) {
			last = &end
		}
		if !r.Start.Before(q.From) && r.Start.Before(q.To) {
			rows = append(rows, r)
			if len(rows) > 500000 {
				return errors.New("traffic query exceeds 500000 records; narrow scope or time range")
			}
		}
		return nil
	}
	for _, ref := range s.state.Archive.Refs {
		if ref.Resolution != q.Resolution {
			continue
		}
		if ref.Last.Add(traffic.Duration(q.Resolution)).After(now.Add(-retention(q.Resolution))) {
			considerBoundary(ref.First)
		}
		day, e := time.Parse("2006-01-02", ref.Day)
		if e != nil {
			return traffic.Result{}, e
		}
		if q.Scope == "port" {
			h := sha256.Sum256([]byte(q.ID))
			if int(h[0])%shards(q.Resolution) != ref.Shard {
				continue
			}
		}
		if !day.Before(q.To) || !partitionEnd(q.Resolution, day).After(q.From) || !partitionEnd(q.Resolution, day).After(now.Add(-retention(q.Resolution))) {
			continue
		}
		raw, e := readArchive(s.dir, ref)
		if e != nil {
			return traffic.Result{}, e
		}
		decoded += len(raw)
		if decoded > maxQueryDecoded {
			return traffic.Result{}, errors.New("traffic query decoding budget exceeded; narrow scope or time range")
		}
		var p archivePartition
		if e = json.Unmarshal(raw, &p); e != nil || p.Version != 1 {
			return traffic.Result{}, errors.New("invalid traffic partition")
		}
		if e = validatePartition(p, ref); e != nil {
			return traffic.Result{}, e
		}
		for _, r := range p.Rows {
			if e = add(r); e != nil {
				return traffic.Result{}, e
			}
		}
	}
	for _, part := range s.archive.pending {
		for _, r := range part {
			if r.Resolution == q.Resolution {
				if r.Start.Add(traffic.Duration(r.Resolution)).After(now.Add(-retention(r.Resolution))) {
					considerBoundary(r.Start)
				}
				if e := add(traffic.CloneRow(r)); e != nil {
					return traffic.Result{}, e
				}
			}
		}
	}
	// Pending rows add to the same UTC bins already in immutable partitions.
	out, e := traffic.Aggregate(rows, q)
	if e != nil {
		return out, e
	}
	out.RetainedFrom = first
	out.LatestAt = last
	// Sample timestamps come from the frozen source catalog, never from the end
	// of an hour/day bucket (which could falsely suggest a future observation).
	var observed, attributionStart *time.Time
	for _, c := range s.archive.cohorts {
		k := c.Key
		k.Metric = q.Metric
		if traffic.Matches(k, q) {
			seen, ok := c.Metrics[q.Metric]
			if !ok && q.Metric == traffic.Octets {
				seen = metricSeen{First: c.First, Last: c.Last}
				ok = true
			}
			if !ok {
				continue
			}
			if observed == nil || seen.Last.After(*observed) {
				v := seen.Last
				observed = &v
			}
			if attributionStart == nil || seen.First.Before(*attributionStart) {
				v := seen.First.Truncate(traffic.Duration(q.Resolution))
				attributionStart = &v
			}
		}
	}
	out.LatestAt = observed
	if attributionStart != nil && boundary != nil {
		v := *boundary
		if attributionStart.After(v) {
			v = *attributionStart
		}
		cutoff := now.Add(-retention(q.Resolution)).Truncate(traffic.Duration(q.Resolution))
		if cutoff.After(v) {
			v = cutoff
		}
		out.RetainedFrom = &v
	}
	if !s.state.Archive.StartedAt.IsZero() {
		v := s.state.Archive.StartedAt
		out.ArchiveStartedAt = &v
	}
	out.RetentionLimited = out.RetainedFrom == nil || q.From.Before(*out.RetainedFrom)
	if out.RetentionLimited {
		out.Notices = append(out.Notices, "Requested history includes time before available counter-derived archive coverage.")
	}
	return out, nil
}
func (s *Store) TrafficTargets(q traffic.TargetQuery, now time.Time) (traffic.Catalog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := traffic.Catalog{Targets: []traffic.Target{}}
	if s.closed {
		return out, errors.New("telemetry store is closed")
	}
	if q.MemberID != "" && q.MemberRevision == "" {
		return out, errors.New("member history requires its frozen member revision")
	}
	switch q.Scope {
	case "", "port", "member", "switch", "rack", "location", "infrastructure", "core-bundle", "connection":
	default:
		return out, errors.New("unsupported traffic catalog scope")
	}
	entries := map[string]traffic.Target{}
	cohortIDs := make([]string, 0, len(s.archive.cohorts))
	for id := range s.archive.cohorts {
		cohortIDs = append(cohortIDs, id)
	}
	sort.Strings(cohortIDs)
	for _, cohortID := range cohortIDs {
		c := s.archive.cohorts[cohortID]
		k := c.Key
		if q.MemberID != "" && (k.MemberID != q.MemberID || k.Dimensions.MemberRevision != q.MemberRevision) {
			continue
		}
		if q.MemberRevision != "" && k.Dimensions.MemberRevision != q.MemberRevision {
			continue
		}
		if !c.Last.After(now.Add(-retention(traffic.Day))) {
			continue
		}
		scopes := map[string][]string{"port": {k.PortID}, "member": {k.MemberID}, "switch": {k.Dimensions.SwitchID}, "rack": {k.Dimensions.RackID}, "location": {k.Dimensions.LocationID}, "infrastructure": {k.Dimensions.InfrastructureID}, "core-bundle": {k.Dimensions.CoreBundleID}, "connection": k.Dimensions.ConnectionIDs}
		for scope, ids := range scopes {
			if q.Scope != "" && q.Scope != scope {
				continue
			}
			for _, id := range ids {
				if id == "" {
					continue
				}
				side := ""
				if scope == "core-bundle" {
					side = k.Dimensions.CoreSide
				}
				key := scope + "/" + id + "/" + k.MemberID + "/" + k.Dimensions.MemberRevision + "/" + side
				v, ok := entries[key]
				if !ok {
					if len(entries) >= 10000 {
						out.Truncated = true
						continue
					}
					v = traffic.Target{Scope: scope, ID: id, MemberID: k.MemberID, MemberRevision: k.Dimensions.MemberRevision, CoreSide: side, FirstSeen: c.First, LastSeen: c.Last, PortIDs: []string{}, SourceIDs: []string{}, StaffOnly: k.Dimensions.MemberRevision == ""}
				}
				if c.First.Before(v.FirstSeen) {
					v.FirstSeen = c.First
				}
				if c.Last.After(v.LastSeen) {
					v.LastSeen = c.Last
				}
				v.PortIDs = uniqueAppend(v.PortIDs, k.PortID)
				v.SourceIDs = uniqueAppend(v.SourceIDs, k.SourceID)
				entries[key] = v
			}
		}
	}
	keys := []string{}
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 10000 {
		keys = keys[:10000]
		out.Truncated = true
	}
	for _, key := range keys {
		v := entries[key]
		sort.Strings(v.PortIDs)
		sort.Strings(v.SourceIDs)
		out.Targets = append(out.Targets, v)
	}
	return out, nil
}
func uniqueAppend(a []string, v string) []string {
	for _, x := range a {
		if x == v {
			return a
		}
	}
	return append(a, v)
}

func validatePartition(p archivePartition, ref archiveRef) error {
	if len(p.Rows) == 0 || len(p.Rows) > 500000 {
		return errors.New("telemetry partition record limit exceeded")
	}
	seen := map[string]bool{}
	for _, r := range p.Rows {
		if r.Resolution != ref.Resolution || partitionDate(r.Resolution, r.Start).Format("2006-01-02") != ref.Day || rowPartition(r) != partitionKey(ref.Resolution, ref.Day, ref.Shard) || r.Start.Truncate(traffic.Duration(r.Resolution)) != r.Start {
			return errors.New("telemetry partition contains misplaced rows")
		}
		id := traffic.RowID(r)
		if seen[id] {
			return errors.New("duplicate telemetry archive row")
		}
		seen[id] = true
		if e := traffic.ValidateRow(r); e != nil {
			return e
		}
	}
	return nil
}
