package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros/api"
)

// This device changes the same actual model inventory as the old per-object
// fixture. Its global-tail branch deliberately never supplies absence facts.
type globalTailDevice struct {
	*removalBatchDevice
	disableGlobal bool
	keepACK       map[int]bool
	unknownAt     int // one-based; zero disables the injected unknown outcome
	unknownEffect bool
	callAbsent    bool
	proofs        []RuleRemovalProofMode
	beforeStep    func(int, *RuleRemovalObservation)
	afterACK      func(int)
}

func (r *globalTailDevice) Binding() Binding {
	b := r.removalBatchDevice.Binding()
	b.RuleRemovalGlobalTailVerified = !r.disableGlobal
	return b
}

func (r *globalTailDevice) ApplyRuleRemovalBatch(ctx context.Context, batch RuleRemovalBatch, recorder RuleRemovalRecorder) error {
	r.proofs = append(r.proofs, batch.ProofMode)
	if batch.ProofMode == RuleRemovalProofPerObject {
		return r.removalBatchDevice.ApplyRuleRemovalBatch(ctx, batch, recorder)
	}
	if batch.ProofMode != RuleRemovalProofGlobalTail || r.disableGlobal {
		return errors.New("fixture proof mode was not negotiated")
	}
	r.batchCalls++
	r.batchIDs = append(r.batchIDs, batch.ID)
	r.inBatch = true
	defer func() {
		r.inBatch = false
		if r.failTail {
			r.tailOutage = true
		}
		if r.beforeReturn != nil {
			r.beforeReturn()
		}
	}()
	if r.waitForContext {
		<-ctx.Done()
		return ctx.Err()
	}
	for i, operation := range batch.Operations {
		if r.prefixLimit > 0 && i >= r.prefixLimit {
			break
		}
		before := r.objectEvidence(batch, operation)
		if r.beforeStep != nil {
			r.beforeStep(i, &before)
		}
		r.late, r.lateObservation, r.lateOperation = recorder, before, operation.OperationID
		if err := recorder.Before(operation.OperationID, before); err != nil {
			return err
		}
		r.batchWrites++
		unknown := r.unknownAt == i+1
		result := api.Result{Acknowledged: true}
		var writeErr error
		if !r.keepACK[i] && (!unknown || r.unknownEffect) {
			result, writeErr = r.reviewDevice.Apply(ctx, operation)
		}
		if unknown {
			writeErr = errors.New("fixture unknown removal outcome")
		}
		if err := recorder.Result(operation.OperationID, result, writeErr); err != nil {
			return err
		}
		if r.afterACK != nil {
			r.afterACK(i)
		}
		if r.callAbsent {
			return recorder.Absent(operation.OperationID, r.objectEvidence(batch, operation))
		}
		if batch.Mode == RuleRemovalCleanup {
			r.cleanupRemaining = r.cleanupRemaining[1:]
		} else {
			r.remaining = r.remaining[1:]
		}
	}
	return nil
}

func globalTailFixture(t *testing.T, mode RuleRemovalMode) (*Supervisor, *globalTailDevice, Config) {
	t.Helper()
	var s *Supervisor
	var original *removalBatchDevice
	var config Config
	if mode == RuleRemovalCleanup {
		s, original, config, _, _ = removalCleanupFixture(t)
	} else {
		s, original, config = removalDiscardFixture(t)
	}
	r := &globalTailDevice{removalBatchDevice: original}
	s.runtime = r
	return s, r, config
}

func globalTailCall(s *Supervisor, mode RuleRemovalMode) (Status, error) {
	if mode == RuleRemovalCleanup {
		return s.Cleanup(context.Background(), s.status.TransactionID, s.status.PlanChecksum)
	}
	return s.DiscardPrepared(context.Background(), discardRequest(s))
}

func globalTailReopen(t *testing.T, s *Supervisor, r *globalTailDevice, config Config, downgrade bool) (*Supervisor, *globalTailDevice) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	fresh := &globalTailDevice{removalBatchDevice: &removalBatchDevice{preparedDiscardDevice: freshDiscardDevice(r.reviewDevice)}, disableGlobal: downgrade}
	next, err := Open(config, fresh)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close() })
	return next, fresh
}

func removalJournalEvents(t *testing.T, config Config) []event {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(config.Dir, "journal.wal"))
	if err != nil {
		t.Fatal(err)
	}
	var events []event
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{10}) {
		var f frame
		var ev event
		if json.Unmarshal(line, &f) != nil || strictJSON(f.Event, &ev) != nil {
			t.Fatal("fixture journal was not decodable")
		}
		events = append(events, ev)
	}
	return events
}

func TestRuleRemovalGlobalTailCommitsOnlyAfterActualFullTail(t *testing.T) {
	for _, mode := range []RuleRemovalMode{RuleRemovalCleanup, RuleRemovalPreparedDiscard} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprint(mode, partial), func(t *testing.T) {
				s, r, config := globalTailFixture(t, mode)
				if partial {
					r.prefixLimit = 1
				}
				checks := 0
				r.afterACK = func(_ int) {
					checks++
					batch := s.removalBatch
					_, index, pending, err := s.removalCursor(mode)
					if err != nil || batch == nil || index != batch.StartIndex || pending != nil || stateMAC(s.log.key, s.current) != batch.BeforeDigest {
						t.Fatal("ACK advanced the formal global cursor")
					}
					for _, step := range batch.Steps {
						if !step.ResultRecorded || !step.Acknowledged || step.AbsentVerified {
							t.Fatal("runtime fabricated an absence or lost its durable result")
						}
					}
				}
				status, err := globalTailCall(s, mode)
				expectedBatchRules := 3
				if partial {
					expectedBatchRules = 2 // the final single rule retains the ordinary path
				}
				if err != nil || s.removalBatch != nil || checks != r.batchWrites || checks != expectedBatchRules || r.fullReadsInBatch != 0 {
					t.Fatal("global tail failed to prove the completed prefix", err)
				}
				if mode == RuleRemovalCleanup && (status.Cleanup.Phase != CleanupComplete || !status.ConfirmationVerified) || mode == RuleRemovalPreparedDiscard && (status.PreparedDiscard.Phase != PreparedDiscardComplete || !status.RollbackVerified) {
					t.Fatal("source's terminal fact was lost")
				}
				starts, intents, results, tails := 0, 0, 0, 0
				for _, ev := range removalJournalEvents(t, config) {
					switch ev.Kind {
					case "removal-batch-start":
						starts++
						if ev.RemovalBatch.ProofMode != RuleRemovalProofGlobalTail {
							t.Fatal("new proof mode was not authenticated in WAL")
						}
					case "removal-batch-intent":
						intents++
					case "removal-batch-result":
						results++
					case "removal-batch-readback":
						tails++
					case "removal-batch-absence":
						t.Fatal("global-tail emitted a per-object absence")
					}
				}
				if starts < 1 || starts != tails || intents != checks || results != checks {
					t.Fatal("durable prefix/result/tail events did not match actual writes")
				}
			})
		}
	}
}

func TestRuleRemovalGlobalTailRequiresDeclaredCapabilityAndActualBeforeEvidence(t *testing.T) {
	for _, mode := range []string{"undeclared", "batch-disabled", "wrong-target", "missing-protected", "foreign-owner", "stale"} {
		t.Run(mode, func(t *testing.T) {
			s, r, config := globalTailFixture(t, RuleRemovalPreparedDiscard)
			r.disableGlobal = mode == "undeclared"
			r.rejectGate = mode == "batch-disabled"
			if mode != "undeclared" && mode != "batch-disabled" {
				r.beforeStep = func(_ int, evidence *RuleRemovalObservation) {
					switch mode {
					case "wrong-target":
						evidence.Attributes["rule"] = "changed-before-proof"
					case "missing-protected":
						evidence.Protected = nil
					case "foreign-owner":
						evidence.Owner.Active = true
					case "stale":
						evidence.ObservedAt = evidence.ObservedAt.Add(-time.Hour)
					}
				}
			}
			_, err := globalTailCall(s, RuleRemovalPreparedDiscard)
			if mode == "undeclared" {
				if err != nil || len(r.proofs) == 0 || r.proofs[0] != RuleRemovalProofPerObject {
					t.Fatal("unsupported runtime selected global-tail")
				}
				absences := 0
				for _, ev := range removalJournalEvents(t, config) {
					if ev.Kind == "removal-batch-absence" {
						absences++
					}
				}
				if absences != 3 {
					t.Fatal("per-object evidence path was relaxed")
				}
			} else if mode == "batch-disabled" {
				if err != nil || len(r.proofs) != 0 || r.batchWrites != 0 || r.discardWrites != 7 {
					t.Fatal("global-tail capability bypassed the base batch gate")
				}
			} else if err == nil || r.batchWrites != 0 {
				t.Fatal("invalid actual Before evidence permitted a write")
			}
		})
	}
}

// Re-signing is limited to test-owned WAL data and deliberately models an
// authentic older writer. No key or private event content is logged.
func rewriteRemovalProofJournal(t *testing.T, config Config, replacement json.RawMessage, sign bool) {
	t.Helper()
	path := filepath.Join(config.Dir, "journal.wal")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(config.Dir, "journal.key"))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	var output []byte
	previous, changed := "", false
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{10}) {
		var f frame
		var object map[string]json.RawMessage
		if json.Unmarshal(line, &f) != nil || json.Unmarshal(f.Event, &object) != nil {
			t.Fatal("fixture WAL invalid")
		}
		var kind string
		_ = json.Unmarshal(object["kind"], &kind)
		if kind == "removal-batch-start" {
			var batch map[string]json.RawMessage
			if json.Unmarshal(object["removalBatch"], &batch) != nil {
				t.Fatal("fixture batch invalid")
			}
			if replacement == nil {
				delete(batch, "proofMode")
			} else {
				batch["proofMode"] = replacement
			}
			object["removalBatch"], _ = json.Marshal(batch)
			f.Event, _ = json.Marshal(object)
			changed = true
		}
		if sign {
			f.Previous = previous
			f.MAC = mac(key, "frame", struct {
				Sequence uint64
				Previous string
				Event    json.RawMessage
			}{f.Sequence, f.Previous, f.Event})
		}
		previous = f.MAC
		line, _ = json.Marshal(f)
		output = append(output, append(line, 10)...)
	}
	if !changed || os.WriteFile(path, output, 0600) != nil {
		t.Fatal("fixture did not rewrite its batch mode")
	}
}

func TestRuleRemovalGlobalTailJournalProofIsClosedAuthenticatedAndLegacyCompatible(t *testing.T) {
	for _, mode := range []string{"legacy-missing", "legacy-empty", "unknown", "null", "signed-incompatible", "unsigned-substitution"} {
		t.Run(mode, func(t *testing.T) {
			s, r, config := removalDiscardFixture(t)
			r.failTail = true
			if _, err := s.DiscardPrepared(context.Background(), discardRequest(s)); err == nil || s.removalBatch == nil {
				t.Fatal("legacy fixture did not retain its authenticated batch")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			var replacement json.RawMessage
			switch mode {
			case "legacy-empty":
				replacement = json.RawMessage(`""`)
			case "unknown":
				replacement = json.RawMessage(`"future-proof"`)
			case "null":
				replacement = json.RawMessage(`null`)
			case "signed-incompatible", "unsigned-substitution":
				replacement = json.RawMessage(`"global-tail"`)
			}
			rewriteRemovalProofJournal(t, config, replacement, mode != "unsigned-substitution")
			fresh := &globalTailDevice{removalBatchDevice: &removalBatchDevice{preparedDiscardDevice: freshDiscardDevice(r.reviewDevice)}, disableGlobal: true}
			next, err := Open(config, fresh)
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close()
			legacy := mode == "legacy-missing" || mode == "legacy-empty"
			if legacy {
				if next.log.poisoned || next.removalBatch == nil || next.removalBatch.ProofMode != RuleRemovalProofPerObject {
					t.Fatal("authentic legacy journal was not replayed as per-object")
				}
				if _, err = next.Recover(context.Background()); err != nil {
					t.Fatal("legacy actual tail could not recover", err)
				}
			} else if !next.log.poisoned {
				t.Fatal("invalid or unauthenticated proof mode was accepted")
			}
			if fresh.batchWrites+fresh.discardWrites+fresh.discardBinds != 0 {
				t.Fatal("journal replay sent a deletion")
			}
		})
	}
}

func TestRuleRemovalGlobalTailReplayCannotConvertACKIntoPendingOrAbsent(t *testing.T) {
	s, r, _ := globalTailFixture(t, RuleRemovalPreparedDiscard)
	r.keepACK = map[int]bool{2: true}
	if _, err := globalTailCall(s, RuleRemovalPreparedDiscard); err == nil || s.removalBatch == nil {
		t.Fatal("fixture did not preserve unresolved ACK")
	}
	b := s.removalBatch
	before := stateMAC(s.log.key, s.current)
	sequence := s.log.sequence
	last := b.Steps[len(b.Steps)-1]
	if err := s.replayRuleRemovalBatch(event{Kind: "removal-batch-absence", BatchID: b.ID, OperationID: last.OperationID, Code: "actual-object-absence"}); err == nil {
		t.Fatal("global-tail journal accepted fabricated per-object absence")
	}
	next := cloneState(s.current)
	deltas := []stateDelta{}
	for _, step := range b.Steps[:len(b.Steps)-1] {
		delta := stateDelta{Key: objectKey(step.Spec.Resource, step.Spec.ID), Remove: true}
		deltas = append(deltas, delta)
		applyDelta(&next, delta)
	}
	digest := stateMAC(s.log.key, next)
	pending := &cleanupAttempt{OperationID: last.OperationID, Spec: last.Spec, BeforeDigest: digest, ResultRecorded: true, Acknowledged: true}
	if err := s.replayRuleRemovalBatch(event{Kind: "removal-batch-readback", BatchID: b.ID, Deltas: deltas, RemovalPending: pending, StateDigest: digest}); err == nil || stateMAC(s.log.key, s.current) != before || s.removalBatch != b || s.preparedDiscard.Pending != nil || s.log.sequence != sequence {
		t.Fatal("replay silently committed a partial ACK prefix")
	}
}

func TestRuleRemovalGlobalTailRetainsEveryIntentWhenAnACKTargetRemains(t *testing.T) {
	for _, mode := range []RuleRemovalMode{RuleRemovalCleanup, RuleRemovalPreparedDiscard} {
		for _, index := range []int{0, 2} {
			t.Run(fmt.Sprint(mode, index), func(t *testing.T) {
				s, r, config := globalTailFixture(t, mode)
				r.keepACK = map[int]bool{index: true}
				if _, err := globalTailCall(s, mode); err == nil || r.batchWrites != 3 || s.removalBatch == nil || !s.removalNeedsReopen {
					t.Fatal("an ACK was mistaken for complete removal")
				}
				_, cursor, pending, _ := s.removalCursor(mode)
				if cursor != s.removalBatch.StartIndex || pending != nil || stateMAC(s.log.key, s.current) != s.removalBatch.BeforeDigest {
					t.Fatal("ACK still present silently committed a shorter prefix")
				}
				if _, err := globalTailCall(s, mode); err == nil || r.batchWrites != 3 {
					t.Fatal("an unresolved ACK target was resent")
				}
				next, fresh := globalTailReopen(t, s, r, config, false)
				if _, err := next.Recover(context.Background()); err == nil || next.removalBatch == nil || fresh.batchWrites+fresh.cleanupWrites+fresh.discardWrites != 0 {
					t.Fatal("reopen adopted or rewrote an unresolved ACK object")
				}
				if _, err := globalTailCall(next, mode); err == nil || fresh.batchWrites+fresh.cleanupWrites+fresh.discardWrites != 0 {
					t.Fatal("reopened controller resent an original intent")
				}
			})
		}
	}
}

func TestRuleRemovalGlobalTailLastUnknownRemainsPendingWithoutReplay(t *testing.T) {
	for _, mode := range []RuleRemovalMode{RuleRemovalCleanup, RuleRemovalPreparedDiscard} {
		for _, applied := range []bool{false, true} {
			t.Run(fmt.Sprint(mode, applied), func(t *testing.T) {
				s, r, config := globalTailFixture(t, mode)
				r.unknownAt, r.unknownEffect = 2, applied
				if _, err := globalTailCall(s, mode); err == nil || r.batchWrites != 2 || !s.removalNeedsReopen {
					t.Fatal("unknown Result allowed another intent")
				}
				_, cursor, pending, _ := s.removalCursor(mode)
				if applied && (cursor != 2 || pending != nil) || !applied && (cursor != 1 || pending == nil || pending.Acknowledged || !pending.ResultRecorded) {
					t.Fatal("actual tail did not distinguish last unknown presence")
				}
				next, fresh := globalTailReopen(t, s, r, config, true)
				_, err := next.Recover(context.Background())
				if fresh.batchWrites+fresh.cleanupWrites+fresh.discardWrites != 0 || fresh.cleanupBinds+fresh.discardBinds != 0 {
					t.Fatal("read-only recovery wrote or bound a bridge")
				}
				if !applied {
					if err == nil {
						t.Fatal("still-present unknown was treated as absent")
					}
					if _, err := globalTailCall(next, mode); err == nil || fresh.batchWrites+fresh.cleanupWrites+fresh.discardWrites != 0 {
						t.Fatal("pending unknown was sent again")
					}
					return
				}
				if err != nil {
					t.Fatal("actual gone prefix could not recover", err)
				}
				if _, err := globalTailCall(next, mode); err != nil {
					t.Fatal("remaining suffix could not use the legacy-capability runtime", err)
				}
				if total := r.batchWrites + r.cleanupWrites + r.discardWrites + fresh.batchWrites + fresh.cleanupWrites + fresh.discardWrites; total != map[RuleRemovalMode]int{RuleRemovalCleanup: 3, RuleRemovalPreparedDiscard: 7}[mode] {
					t.Fatal("recovery repeated or omitted a removal")
				}
			})
		}
	}
}

func TestRuleRemovalGlobalTailWALFailuresAndUnavailableTailKeepSourceCursor(t *testing.T) {
	for _, fault := range []string{"tail", "removal-batch-intent", "removal-batch-result", "removal-batch-readback"} {
		t.Run(fault, func(t *testing.T) {
			s, r, config := globalTailFixture(t, RuleRemovalPreparedDiscard)
			r.failTail = fault == "tail"
			if fault != "tail" {
				s.log.fail = func(kind string) error {
					if kind == fault {
						return errors.New("fixture durable write refused")
					}
					return nil
				}
			}
			if _, err := globalTailCall(s, RuleRemovalPreparedDiscard); err == nil || s.removalBatch == nil || s.preparedDiscard.Index != s.removalBatch.StartIndex || stateMAC(s.log.key, s.current) != s.removalBatch.BeforeDigest {
				t.Fatal("receipt-only state escaped the failed tail boundary")
			}
			if fault == "removal-batch-intent" && r.batchWrites != 0 || fault == "removal-batch-result" && r.batchWrites != 1 {
				t.Fatal("native removal crossed a missing durable boundary")
			}
			next, fresh := globalTailReopen(t, s, r, config, false)
			if _, err := next.Recover(context.Background()); err != nil || fresh.batchWrites+fresh.discardWrites != 0 {
				t.Fatal("durable history could not be reconciled read-only", err)
			}
			if _, err := globalTailCall(next, RuleRemovalPreparedDiscard); err != nil || r.batchWrites+r.discardWrites+fresh.batchWrites+fresh.discardWrites != 7 {
				t.Fatal("recovered actual prefix was replayed", err)
			}
		})
	}
}

func TestRuleRemovalGlobalTailRejectsAbsentAndProtectsClosedRecorder(t *testing.T) {
	s, r, _ := globalTailFixture(t, RuleRemovalPreparedDiscard)
	r.callAbsent = true
	if _, err := globalTailCall(s, RuleRemovalPreparedDiscard); err == nil || r.batchWrites != 1 || !s.removalNeedsReopen {
		t.Fatal("global-tail accepted an Absent callback")
	}
	sequence := s.log.sequence
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 50 {
				if r.late.Before(r.lateOperation, r.lateObservation) == nil || r.late.Result(r.lateOperation, api.Result{Acknowledged: true}, nil) == nil || r.late.Absent(r.lateOperation, r.lateObservation) == nil {
					t.Error("late global-tail callback accepted")
				}
			}
		}()
	}
	s.mu.Lock()
	for range 100 {
		s.removalBatch = &ruleRemovalBatchState{ProofMode: RuleRemovalProofPerObject}
		s.removalBatch = nil
	}
	s.mu.Unlock()
	workers.Wait()
	if s.log.sequence != sequence {
		t.Fatal("closed callback appended a journal event")
	}
}

func TestRuleRemovalGlobalTailFullProofRejectsProtectedCredentialAndOrderDrift(t *testing.T) {
	for _, drift := range []string{"credential", "protected", "external", "order"} {
		t.Run(drift, func(t *testing.T) {
			old, model, config, _, _, _ := reviewManagedRollback(t)
			old.Close()
			r := &globalTailDevice{removalBatchDevice: &removalBatchDevice{preparedDiscardDevice: freshDiscardDevice(model)}}
			s, err := Open(config, r)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			r.beforeReturn = func() {
				switch drift {
				case "credential":
					for id, value := range r.secrets {
						if value == "review-original-A" {
							r.secrets[id] = "review-replacement-B"
							return
						}
					}
					t.Fatal("credential drift fixture had no original credential")
				case "protected":
					r.inv.Instances[0]["router-id"] = "192.0.2.99"
				case "external":
					r.inv.FilterRules = append(r.inv.FilterRules, api.Record{".id": "*FFFFF", "chain": "foreign", "comment": "foreign", "rule": "reject;", "disabled": "false", "inactive": "false"})
				case "order":
					for i, first := range r.inv.FilterRules {
						for j := i + 1; j < len(r.inv.FilterRules); j++ {
							if first["chain"] == r.inv.FilterRules[j]["chain"] {
								r.inv.FilterRules[i], r.inv.FilterRules[j] = r.inv.FilterRules[j], first
								return
							}
						}
					}
					t.Fatal("order drift fixture had no multi-rule retained chain")
				}
			}
			if _, err := s.DiscardPrepared(context.Background(), discardRequest(s)); err == nil || s.removalBatch == nil || s.preparedDiscard.Index != s.removalBatch.StartIndex || stateMAC(s.log.key, s.current) != s.removalBatch.BeforeDigest {
				t.Fatal("tail adopted changed complete state", drift, err)
			}
		})
	}
}
