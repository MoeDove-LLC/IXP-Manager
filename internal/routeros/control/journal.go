package control

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
)

const maxJournalBytes = 128 << 20
const maxRecordBytes = 32 << 20

type beginRecord struct {
	Plan     routeros.Plan       `json:"plan"`
	Known    routeros.KnownState `json:"known"`
	Baseline deviceState         `json:"baseline"`
	Timeout  time.Duration       `json:"timeout"`
	Restore  *restoreLink        `json:"restore,omitempty"`
}
type event struct {
	RemovalBatch    *ruleRemovalBatchState   `json:"removalBatch,omitempty"`
	RemovalPending  *cleanupAttempt          `json:"removalPending,omitempty"`
	PreparedDiscard *preparedDiscardProgress `json:"preparedDiscard,omitempty"`
	Cleanup         *cleanupProgress         `json:"cleanup,omitempty"`
	Batch           *ruleBatchState          `json:"batch,omitempty"`
	BatchID         string                   `json:"batchId,omitempty"`
	Deltas          []stateDelta             `json:"deltas,omitempty"`
	Kind            string                   `json:"kind"`
	At              time.Time                `json:"at"`
	Status          *Status                  `json:"status,omitempty"`
	Begin           *beginRecord             `json:"begin,omitempty"`
	OperationID     string                   `json:"operationId,omitempty"`
	Spec            *mutationSpec            `json:"spec,omitempty"`
	Acknowledged    bool                     `json:"acknowledged,omitempty"`
	ReturnedID      string                   `json:"returnedId,omitempty"`
	Delta           *stateDelta              `json:"delta,omitempty"`
	StateDigest     string                   `json:"stateDigest,omitempty"`
	Code            string                   `json:"code,omitempty"`
}
type frame struct {
	Sequence uint64          `json:"sequence"`
	Previous string          `json:"previous"`
	Event    json.RawMessage `json:"event"`
	MAC      string          `json:"mac"`
}
type journal struct {
	dir      *os.File
	lock     *os.File
	file     *os.File
	key      []byte
	sequence uint64
	previous string
	size     int64
	poisoned bool
	fail     func(string) error // test fault seam, never configurable from input
}

func privateFile(dirfd int, name string, flags int) (*os.File, error) {
	fd, err := syscall.Openat(dirfd, name, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, failure("private-file-open")
	}
	f := os.NewFile(uintptr(fd), name)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, failure("private-file-stat")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		f.Close()
		return nil, failure("private-file-permissions")
	}
	return f, nil
}
func existsAt(dirfd int, name string) (bool, error) {
	fd, err := syscall.Openat(dirfd, name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		return false, nil
	}
	if err != nil {
		return true, failure("private-file-existence")
	}
	syscall.Close(fd)
	return true, nil
}
func strictJSON(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return failure("journal-invalid-json")
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return failure("journal-trailing-data")
	}
	return nil
}
func createPrivate(dirfd int, name string, data []byte) error {
	f, err := privateFile(dirfd, name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := f.Write(data)
	if err != nil || n != len(data) || f.Sync() != nil {
		return failure("private-file-sync")
	}
	return nil
}
func openJournal(directory string, target routeros.Target) (*journal, []event, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, nil, failure("private-directory-required")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, nil, failure("private-directory-create")
	}
	fd, err := syscall.Open(directory, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, failure("private-directory-open")
	}
	j := &journal{dir: os.NewFile(uintptr(fd), directory)}
	fail := func(err error) (*journal, []event, error) { j.close(); return nil, nil, err }
	info, err := j.dir.Stat()
	if err != nil {
		return fail(failure("private-directory-stat"))
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode().Perm() != 0700 || st.Uid != uint32(os.Geteuid()) {
		return fail(failure("private-directory-permissions"))
	}
	j.lock, err = privateFile(fd, "control.lock", syscall.O_RDWR|syscall.O_CREAT)
	if err != nil {
		return fail(err)
	}
	if syscall.Flock(int(j.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return fail(failure("supervisor-already-running"))
	}
	bindingExists, err := existsAt(fd, "target.json")
	if err != nil {
		return fail(err)
	}
	if !bindingExists {
		exists, e := existsAt(fd, "journal.wal")
		if e != nil || exists {
			return fail(failure("target-binding-missing"))
		}
		data, _ := json.Marshal(target)
		if err = createPrivate(fd, "target.json", data); err != nil {
			return fail(err)
		}
		if j.dir.Sync() != nil {
			return fail(failure("private-directory-sync"))
		}
	}
	binding, err := privateFile(fd, "target.json", syscall.O_RDONLY)
	if err != nil {
		return fail(err)
	}
	data, err := io.ReadAll(io.LimitReader(binding, 4097))
	binding.Close()
	if err != nil || len(data) > 4096 {
		return fail(failure("target-binding-invalid"))
	}
	var bound routeros.Target
	if strictJSON(data, &bound) != nil || bound != target {
		return fail(failure("target-binding-mismatch"))
	}
	keyExists, err := existsAt(fd, "journal.key")
	if err != nil {
		return fail(err)
	}
	if !keyExists {
		exists, e := existsAt(fd, "journal.wal")
		if e != nil || exists {
			return fail(failure("journal-key-missing"))
		}
		key := make([]byte, 32)
		if _, e = rand.Read(key); e != nil {
			return fail(failure("journal-key-random"))
		}
		e = createPrivate(fd, "journal.key", key)
		clear(key)
		if e != nil {
			return fail(e)
		}
		if j.dir.Sync() != nil {
			return fail(failure("private-directory-sync"))
		}
	}
	keyFile, err := privateFile(fd, "journal.key", syscall.O_RDONLY)
	if err != nil {
		return fail(err)
	}
	j.key, err = io.ReadAll(io.LimitReader(keyFile, 33))
	keyFile.Close()
	if err != nil || len(j.key) != 32 {
		return fail(failure("journal-key-invalid"))
	}
	j.file, err = privateFile(fd, "journal.wal", syscall.O_RDWR|syscall.O_APPEND|syscall.O_CREAT)
	if err != nil {
		return fail(err)
	}
	if j.dir.Sync() != nil {
		return fail(failure("private-directory-sync"))
	}
	fi, err := j.file.Stat()
	if err != nil || fi.Size() > maxJournalBytes {
		return fail(failure("journal-size-limit"))
	}
	j.size = fi.Size()
	reader := bufio.NewReaderSize(j.file, 64<<10)
	events := []event{}
	for {
		line, err := readBoundedLine(reader, maxRecordBytes)
		if err == io.EOF {
			break
		}
		if err != nil {
			j.poisoned = true
			break
		}
		var f frame
		if strictJSON(line, &f) != nil || f.Sequence != j.sequence+1 || f.Previous != j.previous {
			j.poisoned = true
			break
		}
		want := mac(j.key, "frame", struct {
			Sequence uint64
			Previous string
			Event    json.RawMessage
		}{f.Sequence, f.Previous, f.Event})
		if f.MAC != want {
			j.poisoned = true
			break
		}
		var ev event
		if strictJSON(f.Event, &ev) != nil {
			j.poisoned = true
			break
		}
		j.sequence = f.Sequence
		j.previous = f.MAC
		events = append(events, ev)
		if len(events) > 250000 {
			j.poisoned = true
			break
		}
	}
	return j, events, nil
}
func readBoundedLine(r *bufio.Reader, limit int) ([]byte, error) {
	data := []byte{}
	for {
		part, err := r.ReadSlice(10)
		if len(data)+len(part) > limit {
			return nil, failure("journal-record-limit")
		}
		data = append(data, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF && len(data) > 0 {
			return nil, failure("journal-torn-tail")
		}
		if err != nil {
			return nil, err
		}
		return bytes.TrimSuffix(data, []byte{10}), nil
	}
}
func (j *journal) append(ev event) error {
	if j.poisoned {
		return failure("journal-unwritable")
	}
	if j.fail != nil {
		if err := j.fail(ev.Kind); err != nil {
			j.poisoned = true
			return failure("journal-sync-failed")
		}
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return failure("journal-encode")
	}
	f := frame{Sequence: j.sequence + 1, Previous: j.previous, Event: data}
	f.MAC = mac(j.key, "frame", struct {
		Sequence uint64
		Previous string
		Event    json.RawMessage
	}{f.Sequence, f.Previous, f.Event})
	line, _ := json.Marshal(f)
	line = append(line, 10)
	if len(line) > maxRecordBytes || j.size+int64(len(line)) > maxJournalBytes {
		return failure("journal-size-limit")
	}
	n, err := j.file.Write(line)
	if err != nil || n != len(line) || j.file.Sync() != nil {
		j.poisoned = true
		return failure("journal-sync-failed")
	}
	j.sequence = f.Sequence
	j.previous = f.MAC
	j.size += int64(len(line))
	return nil
}
func (j *journal) close() {
	if j == nil {
		return
	}
	if j.file != nil {
		j.file.Close()
	}
	if j.lock != nil {
		_ = syscall.Flock(int(j.lock.Fd()), syscall.LOCK_UN)
		j.lock.Close()
	}
	if j.dir != nil {
		j.dir.Close()
	}
	clear(j.key)
}
