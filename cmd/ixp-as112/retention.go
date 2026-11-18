package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/as112ops"
)

type repeatedPlans []string

func (p *repeatedPlans) String() string { return "private historical plan files" }
func (p *repeatedPlans) Set(value string) error {
	if value == "" || len(*p) >= as112ops.MaxRetainedVersions {
		return errors.New("historical plan list must be bounded and nonempty")
	}
	*p = append(*p, value)
	return nil
}

type retentionDisplay struct {
	ReviewChecksum   string                    `json:"reviewChecksum"`
	Phase            string                    `json:"phase"`
	CompletedEntries int                       `json:"completedEntries"`
	TotalEntries     int                       `json:"totalEntries"`
	Pending          *as112ops.RetentionIntent `json:"pending,omitempty"`
}

func retentionSummary(s as112ops.RetentionStatus) *retentionDisplay {
	return &retentionDisplay{s.Review.Checksum, s.Phase, s.Cursor, s.Review.OperationCount(), s.Pending}
}

func retentionCommand(ctx context.Context, mode string, args []string, output io.Writer, factory runtimeFactory) error {
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(output)
	planFile := fs.String("plan", "", "private CURRENT canonical plan; the instance must be offline")
	var outputFile, reviewFile, checksum string
	var history repeatedPlans
	var resume, ack, exclusive bool
	if mode == "retention-plan" {
		fs.Var(&history, "history-plan", "private original historical plan; repeat for each candidate, missing proofs are retained")
		fs.StringVar(&outputFile, "output", "", "NEW private review file containing only metadata and hashes")
		fs.BoolVar(&resume, "resume", false, "read remaining entries of an uncertain cleanup and issue a NEW checksum; does not delete")
	} else {
		fs.StringVar(&reviewFile, "review", "", "private review produced by retention-plan")
		fs.StringVar(&checksum, "checksum", "", "exact reviewed retention checksum")
		fs.BoolVar(&ack, "acknowledge", false, "explicitly approve removal of only the reviewed historical entries")
		fs.BoolVar(&exclusive, "exclusive-instance-acknowledged", false, "acknowledge offline continuous exclusive ownership under the dedicated service UID")
	}
	if done, e := parseFlags(fs, args); done || e != nil {
		return e
	}
	p, e := readPlan(*planFile)
	if e != nil {
		return e
	}
	var review as112ops.RetentionReview
	plans := []as112ops.Plan{}
	if mode == "retention-plan" {
		if outputFile == "" || resume && len(history) != 0 {
			return errors.New("retention-plan requires --output; --resume cannot add historical candidates")
		}
		absolute, e := cleanPath(outputFile)
		if e != nil {
			return e
		}
		relative, e := filepath.Rel(p.Root, absolute)
		if e != nil || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("retention review output must be outside the dedicated instance root")
		}
		for _, file := range history {
			raw, e := readPrivate(file, 40<<20)
			if e != nil {
				return errors.New("private historical plan unavailable")
			}
			old, e := as112ops.DecodeRetentionHistory(raw)
			if e != nil {
				return e
			}
			plans = append(plans, old)
		}
	} else {
		raw, e := readPrivate(reviewFile, as112ops.MaxRetentionReviewBytes)
		if e != nil {
			return errors.New("private retention review unavailable")
		}
		review, e = as112ops.DecodeRetentionReview(raw)
		if e != nil {
			return e
		}
		if !ack || !exclusive || checksum != review.Checksum || review.CurrentPlanChecksum != p.Checksum || review.Root != p.Root {
			return errors.New("retention requires the exact review checksum, matching current plan and both explicit acknowledgments")
		}
	}
	var runtime as112ops.Runtime
	if factory != nil {
		runtime = factory()
	}
	controller, e := as112ops.Open(p, runtime)
	if e != nil {
		return e
	}
	call, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	if mode == "retention-plan" {
		if resume {
			review, e = controller.ReviewRetentionResume(call)
		} else {
			review, e = controller.ReviewRetention(call, plans)
		}
		closeErr := controller.Close()
		if e != nil || closeErr != nil {
			return errors.Join(e, closeErr)
		}
		raw, e := as112ops.EncodeRetentionReview(review)
		if e != nil {
			return e
		}
		if e = writeNewPrivate(outputFile, raw); e != nil {
			return e
		}
		type candidate struct {
			PlanChecksum string `json:"planChecksum"`
			Bytes        int64  `json:"bytes"`
		}
		candidates := []candidate{}
		for _, source := range review.Candidates {
			for _, tree := range review.Inventory {
				if source.PlanChecksum == tree.PlanChecksum {
					candidates = append(candidates, candidate{source.PlanChecksum, tree.Bytes})
				}
			}
		}
		return json.NewEncoder(output).Encode(struct {
			Event               string                        `json:"event"`
			Root                string                        `json:"root"`
			CurrentPlanChecksum string                        `json:"currentPlanChecksum"`
			ReviewChecksum      string                        `json:"reviewChecksum"`
			Candidates          []candidate                   `json:"candidates"`
			Protected           []as112ops.RetentionProtected `json:"protected"`
			RemainingPaths      []string                      `json:"remainingPaths"`
			ResumeOf            string                        `json:"resumeOf,omitempty"`
			Deleted             bool                          `json:"deleted"`
		}{"retention-review", review.Root, review.CurrentPlanChecksum, review.Checksum, candidates, review.Protected, review.RemainingPaths(), review.ResumeOf, false})
	}
	request := as112ops.RetentionRequest{Review: review, ReviewChecksum: checksum, ExclusiveInstanceAcknowledged: exclusive, DeleteAcknowledged: ack}
	var status as112ops.RetentionStatus
	if mode == "retention-apply" {
		status, e = controller.ApplyRetention(call, request)
	} else {
		status, e = controller.ReconcileRetention(call, request)
	}
	closeErr := controller.Close()
	writeErr := json.NewEncoder(output).Encode(struct {
		Event               string            `json:"event"`
		Retention           *retentionDisplay `json:"retention"`
		LiveHealthConfirmed bool              `json:"liveHealthConfirmed"`
	}{mode, retentionSummary(status), false})
	return errors.Join(e, closeErr, writeErr)
}
