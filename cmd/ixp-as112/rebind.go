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

func rebindCommand(ctx context.Context, mode string, args []string, output io.Writer, factory runtimeFactory) error {
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(output)
	planFile := fs.String("plan", "", "unchanged private installed plan, including its original artifact and tool pins")
	var outputFile, reviewFile, checksum, kind string
	var acknowledge, exclusive, isolated bool
	if mode == "rebind-plan" {
		fs.StringVar(&outputFile, "output", "", "NEW private review outside the instance root")
		fs.StringVar(&kind, "mode", "same-host-reboot", "same-host-reboot requires established stable identity; move requires explicit source isolation on apply")
	} else {
		fs.StringVar(&reviewFile, "review", "", "private review from rebind-plan")
		fs.StringVar(&checksum, "checksum", "", "exact reviewed rebind checksum")
		fs.BoolVar(&acknowledge, "acknowledge", false, "explicitly approve this local execution binding; never grants active BGP")
		fs.BoolVar(&exclusive, "exclusive-instance-acknowledged", false, "acknowledge continuous exclusive control of this local instance")
		fs.BoolVar(&isolated, "source-isolated-acknowledged", false, "for move: operator has exclusively isolated the previous source and every clone; this tool cannot verify remote death")
	}
	if done, e := parseFlags(fs, args); done || e != nil {
		return e
	}
	p, e := readPlan(*planFile)
	if e != nil {
		return e
	}
	var review as112ops.RebindReview
	if mode == "rebind-plan" {
		absolute, e := cleanPath(outputFile)
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(p.Root, absolute)
		if e != nil || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("rebind review output must be outside the dedicated instance root")
		}
	} else {
		raw, e := readPrivate(reviewFile, as112ops.MaxRebindReviewBytes)
		if e != nil {
			return errors.New("private rebind review unavailable")
		}
		review, e = as112ops.DecodeRebindReview(raw)
		if e != nil {
			return e
		}
		if !acknowledge || !exclusive || checksum != review.Checksum || review.PlanChecksum != p.Checksum || review.Mode == "move" && !isolated {
			return errors.New("rebind requires exact checksum and explicit acknowledgments; move also requires --source-isolated-acknowledged")
		}
	}
	var runtime as112ops.Runtime
	if factory != nil {
		runtime = factory()
	}
	session, e := as112ops.OpenForRebind(p, runtime)
	if e != nil {
		return e
	}
	defer session.Close()
	call, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	if mode == "rebind-plan" {
		review, e = session.Review(call, kind)
		if e != nil {
			return e
		}
		raw, e := as112ops.EncodeRebindReview(review)
		if e != nil {
			return e
		}
		if e = writeNewPrivate(outputFile, raw); e != nil {
			return e
		}
		return json.NewEncoder(output).Encode(struct {
			Event                  string `json:"event"`
			Mode                   string `json:"mode"`
			PlanChecksum           string `json:"planChecksum"`
			ReviewChecksum         string `json:"reviewChecksum"`
			NativeToolsRevalidated bool   `json:"nativeToolsRevalidated"`
			ActivationPermitted    bool   `json:"activationPermitted"`
		}{Event: mode, Mode: kind, PlanChecksum: p.Checksum, ReviewChecksum: review.Checksum})
	}
	q := as112ops.RebindRequest{Review: review, ReviewChecksum: checksum, ExclusiveInstanceAcknowledged: exclusive, SourceIsolatedAcknowledged: isolated}
	if mode == "rebind-confirm" {
		state, result, e := session.Confirm(call, q)
		if e != nil {
			return e
		}
		return json.NewEncoder(output).Encode(struct {
			Event               string          `json:"event"`
			Result              string          `json:"result"`
			Status              as112ops.Status `json:"status"`
			LiveHealthConfirmed bool            `json:"liveHealthConfirmed"`
		}{Event: mode, Result: result, Status: state})
	}
	state, e := session.Apply(call, q)
	if e != nil {
		return e
	}
	return emitStatus(output, mode, "persisted-binding", state)
}
