package telemetry

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// ListFiles validates a quiescent telemetry directory without acquiring a writer
// lock, replaying, truncating, repairing or deleting anything. Returned names are
// relative to dir and include only files required by its published checkpoint.
// Backups must hold the telemetry writer lock (normally by closing the service).
func ListFiles(dir string) ([]string, error) {
	if st, e := os.Lstat(dir); e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("telemetry directory must be a real directory")
	}
	files := []string{}
	state := diskState{Version: 1, Ports: map[string][]PortPoint{}, Sessions: map[string][]SessionPoint{}}
	raw, e := readBoundedRegular(filepath.Join(dir, "snapshot.json"), 64<<20)
	if e == nil {
		if e = json.Unmarshal(raw, &state); e != nil {
			return nil, errors.New("invalid telemetry snapshot JSON")
		}
		if (state.Version != 1 && state.Version != 2) || state.Ports == nil || state.Sessions == nil {
			return nil, errors.New("invalid telemetry snapshot")
		}
		if e = validateRawSnapshot(state); e != nil {
			return nil, e
		}
		files = append(files, "snapshot.json")
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if e = validateArchiveManifest(state.Archive); e != nil {
		return nil, e
	}
	if len(state.Archive.Refs) > 0 || state.Archive.Catalog != nil {
		if st, e := os.Lstat(filepath.Join(dir, "archive")); e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("archive directory must be a real directory")
		}
	}
	for key, ref := range state.Archive.Refs {
		if key != partitionKey(ref.Resolution, ref.Day, ref.Shard) {
			return nil, errors.New("invalid telemetry archive reference")
		}
		raw, e := readArchive(dir, ref)
		if e != nil {
			return nil, e
		}
		var partition archivePartition
		if e = json.Unmarshal(raw, &partition); e != nil || partition.Version != 1 {
			return nil, errors.New("invalid telemetry archive partition")
		}
		if e = validatePartition(partition, ref); e != nil {
			return nil, e
		}
		files = append(files, filepath.ToSlash(filepath.Join("archive", ref.File)))
	}
	if state.Archive.Catalog != nil {
		raw, e := readArchive(dir, *state.Archive.Catalog)
		if e != nil {
			return nil, e
		}
		var catalog map[string]archiveCohort
		if e = json.Unmarshal(raw, &catalog); e != nil || catalog == nil || len(catalog) > maxArchiveCohorts {
			return nil, errors.New("invalid telemetry archive catalog")
		}
		if e = validateCatalog(catalog); e != nil {
			return nil, e
		}
		files = append(files, filepath.ToSlash(filepath.Join("archive", state.Archive.Catalog.File)))
	}
	journal, e := readBoundedRegular(filepath.Join(dir, "journal.bin"), 256<<20)
	if e == nil {
		if e = validateJournal(journal, state.Sequence); e != nil {
			return nil, e
		}
		files = append(files, "journal.bin")
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	sort.Strings(files)
	return files, nil
}
func ValidateFiles(dir string) error { _, e := ListFiles(dir); return e }
func readBoundedRegular(path string, limit int64) ([]byte, error) {
	f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() > limit {
		return nil, errors.New("telemetry file is not regular or exceeds its bound")
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, errors.New("cannot read bounded telemetry file")
	}
	return b, nil
}
func validateJournal(raw []byte, sequence uint64) error {
	r := bytes.NewReader(raw)
	for {
		var header [36]byte
		n, e := io.ReadFull(r, header[:])
		if e == io.EOF && n == 0 {
			return nil
		}
		if e == io.ErrUnexpectedEOF {
			return nil
		}
		if e != nil {
			return e
		}
		size := binary.BigEndian.Uint32(header[:4])
		if size == 0 || size > maxRecord {
			return errors.New("invalid telemetry journal frame length")
		}
		body := make([]byte, size)
		if _, e = io.ReadFull(r, body); e == io.ErrUnexpectedEOF || e == io.EOF {
			return nil
		} else if e != nil {
			return e
		}
		sum := sha256.Sum256(body)
		if !equal(sum[:], header[4:]) {
			return errors.New("telemetry journal checksum mismatch")
		}
		var event journalEvent
		if e = json.Unmarshal(body, &event); e != nil {
			return e
		}
		var batch Batch
		if event.Version == 2 {
			batch = event.Batch
			if event.Sequence == 0 || event.ReceivedAt.IsZero() {
				return errors.New("invalid telemetry journal event")
			}
			if event.Sequence > sequence {
				if event.Sequence != sequence+1 {
					return errors.New("telemetry journal sequence gap")
				}
				sequence = event.Sequence
			}
		} else if event.Version == 0 {
			if e = json.Unmarshal(body, &batch); e != nil {
				return e
			}
		} else {
			return errors.New("unsupported telemetry journal version")
		}
		if e = validateBatch(batch, false); e != nil {
			return fmt.Errorf("invalid telemetry journal: %w", e)
		}
	}
}

func validateRawSnapshot(state diskState) error {
	if len(state.Ports)+len(state.Sessions) > maxEntities {
		return errors.New("telemetry snapshot entity limit exceeded")
	}
	count := 0
	for id, points := range state.Ports {
		if len(points) > MaxPointsPerSeries {
			return errors.New("telemetry raw series limit exceeded")
		}
		count += len(points)
		for i, p := range points {
			if p.PortID != id || i > 0 && p.ObservedAt.Sub(points[i-1].ObservedAt) < time.Second {
				return errors.New("invalid telemetry raw port chronology")
			}
			if e := validateBatch(Batch{RouterID: p.RouterID, ObservedAt: p.ObservedAt, Ports: []PortSample{p.PortSample}}, false); e != nil {
				return e
			}
			if p.InBps != nil && *p.InBps < 0 || p.OutBps != nil && *p.OutBps < 0 {
				return errors.New("negative stored counter rate")
			}
		}
	}
	for id, points := range state.Sessions {
		if len(points) > MaxPointsPerSeries {
			return errors.New("telemetry raw series limit exceeded")
		}
		count += len(points)
		for i, p := range points {
			if p.PeerID != id || i > 0 && p.ObservedAt.Sub(points[i-1].ObservedAt) < time.Second {
				return errors.New("invalid telemetry raw session chronology")
			}
			if e := validateBatch(Batch{RouterID: p.RouterID, ObservedAt: p.ObservedAt, Sessions: []SessionSample{p.SessionSample}}, false); e != nil {
				return e
			}
		}
	}
	if count > MaxPoints {
		return errors.New("telemetry snapshot point limit exceeded")
	}
	return nil
}
