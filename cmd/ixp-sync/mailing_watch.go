package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/mailingsync"
)

func mailingWatch(args []string, out io.Writer) error {
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	config := fs.String("config", "", "fixed private source and target configuration")
	dir := fs.String("state-dir", "", "private durable automatic worker history")
	plan := fs.String("plan", "", "first manually reviewed plan (initial authorization only)")
	checksum := fs.String("checksum", "", "reviewed plan checksum, or policy checksum for resume/prune")
	automatic := fs.Bool("enable-automatic-sync", false, "authorize future explicit source intentions")
	exclusive := fs.Bool("exclusive-access-acknowledged", false, "exclusive target administration for the entire worker lifetime")
	maximum := fs.Int("max-changes", 0, "explicit maximum membership changes per round")
	interval := fs.Int("interval-seconds", 0, "explicit poll interval, 5..86400 seconds")
	once := fs.Bool("once", false, "one observation or application round")
	reconcile := fs.Bool("reconcile-only", false, "read back active round without remote writes or clearing halt")
	resume := fs.Bool("resume", false, "explicitly clear a resolved halt without remote writes")
	status := fs.Bool("status", false, "read persisted local worker status")
	keep := fs.Int("prune-keep", -1, "explicitly remove old closed rounds, keeping at least this many")
	if err := fs.Parse(args[1:]); err != nil {
		return errors.New("invalid watch flags; credentials belong only in private files")
	}
	if fs.NArg() != 0 || *config == "" || *dir == "" {
		return errors.New("watch requires --config and --state-dir")
	}
	init := args[0] == "mailing-watch-init"
	modes := 0
	for _, selected := range []bool{*once, *reconcile, *resume, *status, *keep != -1} {
		if selected {
			modes++
		}
	}
	if init {
		if modes != 0 || *plan == "" || *checksum == "" || !*automatic || !*exclusive || *maximum < 1 || *maximum > 1000 || *interval < 5 || *interval > 86400 {
			return errors.New("watch-init requires reviewed --plan/--checksum, --enable-automatic-sync, --exclusive-access-acknowledged, --max-changes 1..1000 and --interval-seconds 5..86400")
		}
	} else {
		if args[0] != "mailing-watch" || modes > 1 || *plan != "" || *automatic || *exclusive || *maximum != 0 || *interval != 0 || ((*resume || *keep != -1) != (*checksum != "")) || *keep != -1 && (*keep < 1 || *keep > 10000) {
			return errors.New("watch runtime uses its saved policy; choose only one of --once, --status, --reconcile-only, --resume or --prune-keep; resume/prune require --checksum POLICY_SHA256")
		}
	}
	c, err := mailingsync.LoadConfig(*config)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if init {
		p, err := mailingsync.LoadPlan(*plan, c)
		if err != nil {
			return err
		}
		bounded, cancel := context.WithTimeout(ctx, 15*time.Minute)
		defer cancel()
		policy, err := mailingsync.AuthorizeWatch(bounded, c, p, *dir, *checksum, mailingsync.WatchOptions{Automatic: *automatic, Exclusive: *exclusive, MaxChanges: *maximum, IntervalSeconds: *interval})
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(policy)
	}
	w, err := mailingsync.OpenWatch(c, *dir)
	if err != nil {
		return err
	}
	defer w.Close()
	emit := func(s mailingsync.WatchStatus, operationErr error) error {
		if err := json.NewEncoder(out).Encode(struct {
			Source             string                  `json:"source"`
			CommandSucceeded   bool                    `json:"commandSucceeded"`
			LiveStateConfirmed bool                    `json:"liveStateConfirmed"`
			Status             mailingsync.WatchStatus `json:"status"`
		}{"persisted-watch-state", operationErr == nil, false, s}); err != nil {
			return err
		}
		return operationErr
	}
	if *status {
		return emit(w.Status(), nil)
	}
	if *keep != -1 {
		s, err := w.Prune(*keep, *checksum)
		return emit(s, err)
	}
	for {
		bounded, cancel := context.WithTimeout(ctx, 15*time.Minute)
		var s mailingsync.WatchStatus
		switch {
		case *reconcile:
			s, err = w.Reconcile(bounded)
		case *resume:
			s, err = w.Resume(bounded, *checksum)
		default:
			s, err = w.Tick(bounded)
		}
		cancel()
		if err = emit(s, err); err != nil {
			return err
		}
		if *once || *resume || *reconcile {
			return nil
		}
		timer := time.NewTimer(time.Duration(w.Policy().IntervalSeconds) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
