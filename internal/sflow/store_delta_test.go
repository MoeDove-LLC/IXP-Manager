package sflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func uintString(n uint64) string { return strconv.FormatUint(n, 10) }

func TestDeltaWALWriteAndSyncFailuresLeaveMemoryAtomic(t *testing.T) {
	for _, mode := range []string{"write", "sync"} {
		t.Run(mode, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "sflow")
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Ingest(testAttributed("atomic-seed")); err != nil {
				t.Fatal(err)
			}
			before := digest(s.state)
			sequence := s.state.Sequence
			bytesBefore := s.stateBytes
			if mode == "write" {
				s.wal.Close()
			} else {
				original := s.wal
				r, w, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				s.wal = w
				original.Close()
			}
			if err = s.Ingest(testAttributed("not-durable")); err == nil {
				t.Fatal("faulted WAL reported committed")
			}
			if digest(s.state) != before || s.state.Sequence != sequence || s.stateBytes != bytesBefore {
				t.Fatal("WAL failure partially changed buckets/dedup/cursor/quality")
			}
			_ = s.Close()
			recovered, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			rows, err := recovered.Query(queryNow("member-a"))
			if err != nil || len(rows.Items) != 1 || rows.Items[0].SampleCount != "1" {
				t.Fatal("failed mutation survived recovery", rows, err)
			}
		})
	}
}

func TestDeltaWorkingSetDoesNotGrowWithUnrelatedHistory(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "sflow"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Ingest(testAttributed("growth-seed")); err != nil {
		t.Fatal(err)
	}
	small := s.state
	large := flowSnapshot{Version: small.Version, Sequence: small.Sequence, Rows: map[string]Bucket{}, Quality: small.Quality, Cursors: small.Cursors, Seen: map[string]seenBatch{}}
	for k, v := range small.Rows {
		large.Rows[k] = v
	}
	for k, v := range small.Seen {
		large.Seen[k] = v
	}
	var template Bucket
	for _, row := range small.Rows {
		template = row
		break
	}
	for i := 0; i < 2000; i++ {
		row := template
		row.SourceConnectionID = "unrelated-" + uintString(uint64(i))
		large.Rows[bucketKey(row)] = row
		large.Seen["unrelated-"+uintString(uint64(i))] = seenBatch{Checksum: digest(i), At: time.Now().UTC()}
	}
	b := testAttributed("affected-only")
	now := time.Now().UTC()
	measure := func(state flowSnapshot) float64 {
		return testing.AllocsPerRun(3, func() {
			m, err := prepareMutation(state, b, now, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(m.Rows) != 2 || len(m.Quality) != 1 || len(m.Cursors) != 1 || m.Seen == nil {
				t.Fatal("mutation copied unrelated records")
			}
		})
	}
	smallAllocs, largeAllocs := measure(small), measure(large)
	if largeAllocs > smallAllocs+10 {
		t.Fatalf("hot preparation allocations scale with database: small=%g large=%g", smallAllocs, largeAllocs)
	}
}

func TestDeltaByteAccountingRemainsConservativeAndQuotaRefusalAtomic(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "sflow"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 20; i++ {
		b := testAttributed("account-" + uintString(uint64(i)))
		b.Flows[0].SourceConnectionID = "connection-" + uintString(uint64(i%3))
		if err = s.Ingest(b); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(flowDiskSnapshot{s.state, digest(s.state)})
		if int64(len(raw)) > s.stateBytes {
			t.Fatal("incremental accounting underestimates replacement snapshot")
		}
	}
	beforeBytes := s.stateBytes
	if err = s.rebuildAccountingLocked(); err != nil || s.stateBytes != beforeBytes {
		t.Fatal("incremental and full maintenance accounting disagree", err, beforeBytes, s.stateBytes)
	}
	before := digest(s.state)
	wal := s.walBytes
	s.stateBytes = MaxStoreBytes
	if err = s.Ingest(testAttributed("quota-refused")); err == nil {
		t.Fatal("byte quota bypassed")
	}
	if digest(s.state) != before || s.walBytes != wal {
		t.Fatal("quota refusal partially committed")
	}
	s.stateBytes = beforeBytes
}

func TestDeltaReplayWithCommittedPrefixAndUnappliedSuffix(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sflow")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Ingest(testAttributed("prefix")); err != nil {
		t.Fatal(err)
	}
	prefix, _ := os.ReadFile(filepath.Join(dir, "flows.wal"))
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Ingest(testAttributed("suffix")); err != nil {
		t.Fatal(err)
	}
	suffix, _ := os.ReadFile(filepath.Join(dir, "flows.wal"))
	crashStore(s)
	if err = os.WriteFile(filepath.Join(dir, "flows.wal"), append(prefix, suffix...), 0600); err != nil {
		t.Fatal(err)
	}
	if err = ValidateFiles(dir); err != nil {
		t.Fatal("read-only replay changed the frozen snapshot cursor comparison", err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	result, err := s.Query(queryNow("member-a"))
	if err != nil || len(result.Items) != 1 || result.Items[0].SampleCount != "2" {
		t.Fatal("delta replay counted prefix twice or lost suffix", result, err)
	}
}
