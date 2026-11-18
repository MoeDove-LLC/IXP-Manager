package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"time"

	"github.com/moedove/ixp-manager/internal/as112ops"
)

func replacePlan(ctx context.Context, mode string, args []string, output io.Writer, factory runtimeFactory) error {
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(output)
	previousFile := fs.String("previous-plan", "", "private previous canonical plan")
	nextFile := fs.String("plan", "", "private next canonical plan")
	previousChecksum := fs.String("previous-plan-checksum", "", "explicit expected previous plan checksum")
	currentTarget := fs.String("current-target", "", "explicit expected current pointer: versions/PREVIOUS_PLAN_SHA256")
	planChecksum := fs.String("plan-checksum", "", "exact reviewed next plan checksum")
	activeChecksum := fs.String("active-checksum", "", "next original approved active SHA256")
	standbyChecksum := fs.String("standby-checksum", "", "next reviewed derived standby SHA256")
	dnsChecksum := fs.String("dns-checksum", "", "next original approved DNS SHA256")
	exclusive := fs.Bool("exclusive-instance-acknowledged", false, "acknowledge offline exclusive ownership of this dedicated instance")
	if done, err := parseFlags(fs, args); done || err != nil {
		return err
	}
	previous, err := readPlan(*previousFile)
	if err != nil {
		return err
	}
	next, err := readPlan(*nextFile)
	if err != nil {
		return err
	}
	if *previousChecksum != previous.Checksum || *currentTarget != filepath.Join("versions", previous.Checksum) || !*exclusive || *planChecksum != next.Checksum || *activeChecksum != next.ActiveChecksum || *standbyChecksum != next.StandbyChecksum || *dnsChecksum != next.DNSChecksum {
		return errors.New("replacement requires exact previous plan/current pointer, all four next checksums and exclusive acknowledgment")
	}
	var runtime as112ops.Runtime
	if factory != nil {
		runtime = factory()
	}
	controller, err := as112ops.OpenForReplacement(previous, next, runtime)
	if err != nil {
		return err
	}
	request := as112ops.ReplacementRequest{Previous: previous, Next: next, PreviousPlanChecksum: *previousChecksum, CurrentTarget: *currentTarget, Approval: as112ops.Approval{PlanChecksum: *planChecksum, ActiveChecksum: *activeChecksum, StandbyChecksum: *standbyChecksum, DNSChecksum: *dnsChecksum, ExclusiveInstanceAcknowledged: *exclusive}}
	call, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	if mode == "replace" {
		_, err = controller.Replace(call, request)
	} else {
		_, err = controller.ReconcileReplacement(call, request)
	}
	closeErr := controller.Close()
	writeErr := emitStatus(output, mode, "explicit-filesystem-transaction-result", controller.Status())
	return errors.Join(err, closeErr, writeErr)
}
