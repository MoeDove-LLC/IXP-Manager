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

func mailing(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("mailing command required")
	}
	if args[0] == "mailing-watch" || args[0] == "mailing-watch-init" {
		return mailingWatch(args, out)
	}
	action := args[0]
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "", "owned 0600 source and target configuration")
	planPath := fs.String("plan", "", "owned 0600 reviewed plan")
	outPath := fs.String("out", "", "new private plan output (preview only)")
	journal := fs.String("journal", "", "private durable apply journal")
	checksum := fs.String("checksum", "", "explicit reviewed plan SHA-256")
	exclusive := fs.Bool("exclusive-access-acknowledged", false, "exclusive target membership administration during this apply")
	if e := fs.Parse(args[1:]); e != nil {
		return errors.New("invalid mailing flags; credentials belong in private files")
	}
	if fs.NArg() != 0 || *configPath == "" {
		return errors.New("--config is required")
	}
	if action == "mailing-plan" {
		if *outPath == "" || *planPath != "" || *journal != "" || *checksum != "" || *exclusive {
			return errors.New("mailing-plan requires only --config and --out; it never writes remotely")
		}
	} else if action == "mailing-apply" || action == "mailing-reconcile" || action == "mailing-status" {
		if *planPath == "" || *journal == "" || *outPath != "" {
			return errors.New("--config, --plan and --journal required")
		}
		if action == "mailing-apply" && (!*exclusive || *checksum == "") {
			return errors.New("apply requires --checksum and --exclusive-access-acknowledged")
		}
		if action == "mailing-reconcile" && (*checksum == "" || *exclusive) {
			return errors.New("reconcile requires --checksum and performs reads only")
		}
		if action == "mailing-status" && (*checksum != "" || *exclusive) {
			return errors.New("status reads persisted observations only")
		}
	} else {
		return errors.New("unknown mailing command")
	}
	c, e := mailingsync.LoadConfig(*configPath)
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if action == "mailing-plan" {
		client, e := mailingsync.NewClient(c)
		if e != nil {
			return e
		}
		p, e := client.Preview(ctx)
		if e != nil {
			return e
		}
		raw, e := mailingsync.Encode(p)
		if e != nil {
			return e
		}
		if e = mailingsync.WriteNew(*outPath, raw); e != nil {
			return e
		}
		return json.NewEncoder(out).Encode(p.Summary(nil))
	}
	p, e := mailingsync.LoadPlan(*planPath, c)
	if e != nil {
		return e
	}
	var summary mailingsync.Summary
	if action == "mailing-status" {
		summary, e = mailingsync.Status(p, *journal)
	} else {
		client, err := mailingsync.NewClient(c)
		if err != nil {
			return err
		}
		if action == "mailing-apply" {
			summary, e = client.Apply(ctx, p, *journal, *checksum, *exclusive)
		} else {
			summary, e = client.Reconcile(ctx, p, *journal, *checksum)
		}
	}
	// The journal, not stdout, records each mutation before another can occur.
	if err := json.NewEncoder(out).Encode(struct {
		Source             string              `json:"source"`
		CommandSucceeded   bool                `json:"commandSucceeded"`
		LiveStateConfirmed bool                `json:"liveStateConfirmed"`
		Summary            mailingsync.Summary `json:"summary"`
	}{"persisted-observations", e == nil, false, summary}); e == nil {
		e = err
	}
	return e
}
