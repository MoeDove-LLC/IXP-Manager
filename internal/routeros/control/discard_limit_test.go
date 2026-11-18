package control

import (
	"context"
	"errors"
	"testing"
)

func limitedDiscardRequest(s *Supervisor, limit int) DiscardRequest {
	request := discardRequest(s)
	request.MaxBatches = limit
	return request
}

func TestPreparedDiscardLimitRejectsBeforeAnyIntentOrDeviceCall(t *testing.T) {
	s, r, _ := globalTailFixture(t, RuleRemovalPreparedDiscard)
	sequence := s.log.sequence
	for _, limit := range []int{-1, MaxDiscardRequestBatches + 1} {
		status, err := s.DiscardPrepared(context.Background(), limitedDiscardRequest(s, limit))
		var failure *Failure
		if !errors.As(err, &failure) || failure.Code != "discard-invalid-batch-limit" || status.PreparedDiscard.Phase != PreparedDiscardNone || s.log.sequence != sequence || r.discardBinds+r.batchCalls+r.discardWrites != 0 {
			t.Fatal("invalid limit reached durable/device work")
		}
	}
}

func TestPreparedDiscardBoundedRequestsStopOnlyAtVerifiedBoundary(t *testing.T) {
	for _, mode := range []string{"global-tail", "per-object", "single-fallback"} {
		t.Run(mode, func(t *testing.T) {
			s, r, _ := globalTailFixture(t, RuleRemovalPreparedDiscard)
			r.disableGlobal = mode != "global-tail"
			r.rejectGate = mode == "single-fallback"
			previous, requests := 0, 0
			for {
				beforeUnits := r.batchCalls + r.discardWrites
				status, err := s.DiscardPrepared(context.Background(), limitedDiscardRequest(s, 1))
				if err != nil {
					t.Fatal(err)
				}
				requests++
				if r.batchCalls+r.discardWrites-beforeUnits != 1 || status.PreparedDiscard.Completed <= previous || s.preparedDiscard.Pending != nil || s.removalBatch != nil {
					t.Fatal("request exceeded one verified unit or retained an intent")
				}
				previous = status.PreparedDiscard.Completed
				if status.PreparedDiscard.Phase == PreparedDiscardComplete {
					if previous != status.PreparedDiscard.Total || status.PreparedArtifactsRetained || stateMAC(s.log.key, s.current) != stateMAC(s.log.key, s.begin.Baseline) {
						t.Fatal("Complete without exact final baseline")
					}
					break
				}
				if status.PreparedDiscard.Phase != PreparedDiscardPending || status.PreparedDiscard.LastCode != "discard-awaiting-explicit-continuation" || !status.PreparedArtifactsRetained || !status.RollbackVerified || status.ConfirmationVerified {
					t.Fatal("bounded return mislabeled terminal state")
				}
				if _, err := s.Review(context.Background()); err == nil {
					t.Fatal("partial discard became a planning baseline")
				}
				if requests > 7 {
					t.Fatal("bounded continuation made no progress")
				}
			}
			if requests < 2 || r.batchWrites+r.discardWrites != 7 {
				t.Fatal("fixture did not exercise multiple exact-once requests")
			}
		})
	}
}

func TestPreparedDiscardBoundedFreshRecoveryIsReadOnlyBeforeExplicitContinue(t *testing.T) {
	s, r, config := globalTailFixture(t, RuleRemovalPreparedDiscard)
	status, err := s.DiscardPrepared(context.Background(), limitedDiscardRequest(s, 2))
	if err != nil || status.PreparedDiscard.Phase != PreparedDiscardPending || r.batchCalls != 1 || r.discardWrites != 1 {
		t.Fatal("request-local counter reset across single/batch branches", err)
	}
	completed := status.PreparedDiscard.Completed
	next, fresh := globalTailReopen(t, s, r, config, false)
	recovered, err := next.Recover(context.Background())
	if err != nil || recovered.PreparedDiscard.Completed != completed || recovered.PreparedDiscard.Phase != PreparedDiscardPending || recovered.PreparedDiscard.LastCode != "discard-awaiting-explicit-continuation" || fresh.discardBinds+fresh.batchWrites+fresh.discardWrites != 0 {
		t.Fatal("fresh recovery continued implicitly", err)
	}
	for recovered.PreparedDiscard.Phase != PreparedDiscardComplete {
		prior := fresh.batchCalls + fresh.discardWrites
		recovered, err = next.DiscardPrepared(context.Background(), limitedDiscardRequest(next, 1))
		if err != nil || fresh.batchCalls+fresh.discardWrites-prior != 1 {
			t.Fatal("explicit continuation exceeded limit", err)
		}
	}
	if r.batchWrites+r.discardWrites+fresh.batchWrites+fresh.discardWrites != 7 {
		t.Fatal("fresh continuation repeated an original intent")
	}
}

func TestPreparedDiscardLimitDoesNotConvertUnknownDriftOrWALFailureToNormalStop(t *testing.T) {
	for _, fault := range []string{"unknown", "tail", "boundary-wal", "drift-between-requests"} {
		t.Run(fault, func(t *testing.T) {
			s, r, config := globalTailFixture(t, RuleRemovalPreparedDiscard)
			if fault == "drift-between-requests" {
				if _, err := s.DiscardPrepared(context.Background(), limitedDiscardRequest(s, 1)); err != nil {
					t.Fatal(err)
				}
				r.inv.Tables[0]["comment"] = "changed outside request"
			}
			if fault == "unknown" {
				r.unknownAt = 1
			}
			if fault == "tail" {
				r.failTail = true
			}
			if fault == "boundary-wal" {
				calls := 0
				s.log.fail = func(kind string) error {
					if kind == "discard-status" {
						calls++
						if calls == 2 {
							return errors.New("fixture boundary fsync refused")
						}
					}
					return nil
				}
			}
			status, err := s.DiscardPrepared(context.Background(), limitedDiscardRequest(s, 2))
			if err == nil || status.PreparedDiscard.Phase == PreparedDiscardComplete {
				t.Fatal("failure became a normal bounded completion")
			}
			if fault == "unknown" {
				next, fresh := globalTailReopen(t, s, r, config, false)
				if _, err := next.Recover(context.Background()); err == nil {
					t.Fatal("still-present unknown was accepted")
				}
				if _, err := next.DiscardPrepared(context.Background(), limitedDiscardRequest(next, 1)); err == nil || fresh.batchWrites+fresh.discardWrites != 0 {
					t.Fatal("unknown original intent was retried")
				}
			}
		})
	}
}
