// ixp-as112 supervises only a reviewed, dedicated local DNS/BGP instance.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/moedove/ixp-manager/internal/as112ops"
	"github.com/moedove/ixp-manager/internal/routing"
)

const usage = `Usage: ixp-as112 prepare|install|run|status|reconcile|replace|replace-reconcile|retention-plan|retention-apply|retention-reconcile|rebind-plan|rebind-apply|rebind-confirm|unit [flags]
prepare reads private approved artifacts, pins actual native ELF tools and this
host boot/network namespace, and writes a NEW private canonical plan.
install requires the exact plan/active/standby/DNS checksums and an explicit
exclusive-instance acknowledgment; it publishes validated files but starts nothing.
run stays in the foreground. Without --recover-ack it never starts active BGP.
With that acknowledgment, continuous verified DNS health may recover the same
approved active artifact. SIGINT/SIGTERM closes the owner, stopping BGP before DNS.
status reads the persisted journal: it is NOT a live health observation.
reconcile requires an explicit matching plan; it only verifies process absence
and installed bytes before clearing uncertainty. It never replays an operation.
replace requires two exact plans, the old pointer and full new approval. It
updates only an offline same-host instance and leaves it withdrawn.
replace-reconcile explicitly resolves that exact filesystem transaction; it may
finish validated file publication but never starts/stops a service process.
unit writes a NEW review-only systemd unit; it never installs/enables/starts it.
retention-plan reviews proven historical versions without deleting; retention-apply
requires its checksum and explicit approval. retention-reconcile only confirms
attempted entries are absent. A still-present uncertain target requires a NEW
retention-plan --resume review and a newly approved checksum before retrying.
rebind-plan reviews an unchanged installed plan for same-host-reboot or explicit
move. rebind-apply requires exact approval and leaves it withdrawn; rebind-confirm
only reads the exact old/new status. No daemon or old native validator is run.
Only dedicated foreground BIRD 2/3 and FRR are implemented. OpenBGPD privsep and
remote Junos supervision remain unsupported. Use <command> --help for flags.`

type runtimeFactory func() as112ops.Runtime

type statusView struct {
	Event               string                      `json:"event"`
	Source              string                      `json:"source"`
	LiveHealthConfirmed bool                        `json:"liveHealthConfirmed"`
	Status              as112ops.Status             `json:"status"`
	Replacement         *as112ops.ReplacementStatus `json:"replacement,omitempty"`
	Retention           *retentionDisplay           `json:"retention,omitempty"`
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, terminationSignal())
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, nil); err != nil {
		fmt.Fprintln(os.Stderr, "ixp-as112:", err)
		os.Exit(1)
	}
}

func parseFlags(fs *flag.FlagSet, args []string) (bool, error) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, nil
		}
		return false, errors.New("invalid command flags; use --help")
	}
	if fs.NArg() != 0 {
		return false, errors.New("positional arguments are not accepted")
	}
	return false, nil
}

func run(ctx context.Context, args []string, output io.Writer, factory runtimeFactory) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(output, usage)
		return err
	}
	mode := args[0]
	if mode == "rebind-plan" || mode == "rebind-apply" || mode == "rebind-confirm" {
		return rebindCommand(ctx, mode, args[1:], output, factory)
	}
	if mode == "retention-plan" || mode == "retention-apply" || mode == "retention-reconcile" {
		return retentionCommand(ctx, mode, args[1:], output, factory)
	}
	if mode == "prepare" {
		return prepare(args[1:], output)
	}
	if mode == "unit" {
		return writeUnit(args[1:], output)
	}
	if mode == "replace" || mode == "replace-reconcile" {
		return replacePlan(ctx, mode, args[1:], output, factory)
	}
	if mode != "install" && mode != "run" && mode != "status" && mode != "reconcile" {
		return errors.New("unknown AS112 action; use --help")
	}
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(output)
	if mode == "status" {
		root := fs.String("root", "", "dedicated instance directory; read-only persisted status")
		if done, err := parseFlags(fs, args[1:]); done || err != nil {
			return err
		}
		if *root == "" {
			return errors.New("--root is required")
		}
		status, err := as112ops.ReadStatus(*root)
		if err != nil {
			return errors.New("private persisted status unavailable; no live observation was performed")
		}
		view := statusView{Event: "status", Source: "persisted-journal", Status: status}
		if replacement, err := as112ops.ReadReplacement(*root); err == nil {
			view.Replacement = &replacement
		} else if !os.IsNotExist(err) {
			return errors.New("persisted replacement journal unavailable; inspect before any further action")
		}
		if retention, err := as112ops.ReadRetention(*root); err == nil {
			view.Retention = retentionSummary(retention)
		} else if !os.IsNotExist(err) {
			return errors.New("persisted retention journal unavailable; inspect before further action")
		}
		return json.NewEncoder(output).Encode(view)
	}
	planFile := fs.String("plan", "", "private canonical plan from prepare")
	var planChecksum, activeChecksum, standbyChecksum, dnsChecksum string
	var exclusive, recover, reconcileAck bool
	if mode == "install" || mode == "reconcile" {
		fs.StringVar(&planChecksum, "plan-checksum", "", "exact reviewed plan checksum")
	}
	if mode == "install" {
		fs.StringVar(&activeChecksum, "active-checksum", "", "original approved active file SHA256")
		fs.StringVar(&standbyChecksum, "standby-checksum", "", "reviewed strictly derived standby SHA256")
		fs.StringVar(&dnsChecksum, "dns-checksum", "", "original approved DNS bundle SHA256")
		fs.BoolVar(&exclusive, "exclusive-instance-acknowledged", false, "acknowledge a dedicated instance and exclusive ownership")
	}
	if mode == "run" {
		fs.BoolVar(&recover, "recover-ack", false, "explicitly allow the same approved active artifact after consecutive healthy checks")
	}
	if mode == "reconcile" {
		fs.BoolVar(&reconcileAck, "acknowledge", false, "explicitly request read-only target reconciliation, never operation replay")
	}
	if done, err := parseFlags(fs, args[1:]); done || err != nil {
		return err
	}
	p, err := readPlan(*planFile)
	if err != nil {
		return err
	}
	if mode == "install" && (!exclusive || planChecksum != p.Checksum || activeChecksum != p.ActiveChecksum || standbyChecksum != p.StandbyChecksum || dnsChecksum != p.DNSChecksum) {
		return errors.New("install requires all four exact reviewed checksums and --exclusive-instance-acknowledged")
	}
	if mode == "reconcile" && (!reconcileAck || planChecksum != p.Checksum) {
		return errors.New("reconcile requires --acknowledge and the exact --plan-checksum")
	}
	var notifier *systemdNotifier
	if mode == "run" {
		notifier, err = newSystemdNotifier(os.Getenv, os.Getpid())
		if err != nil {
			return err
		}
	}
	var runtime as112ops.Runtime
	if factory != nil {
		runtime = factory()
	}
	controller, err := as112ops.Open(p, runtime)
	if err != nil {
		return err
	}
	if mode == "run" {
		return runForeground(ctx, controller, p, recover, output, notifier)
	}
	call, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	if mode == "install" {
		_, err = controller.Install(call, as112ops.Approval{PlanChecksum: planChecksum, ActiveChecksum: activeChecksum, StandbyChecksum: standbyChecksum, DNSChecksum: dnsChecksum, ExclusiveInstanceAcknowledged: exclusive})
	} else {
		_, err = controller.Reconcile(call)
	}
	closeErr := controller.Close()
	writeErr := emitStatus(output, mode, "operation-result", controller.Status())
	return errors.Join(err, closeErr, writeErr)
}

func emitStatus(output io.Writer, event, source string, status as112ops.Status) error {
	// Even an in-memory status is a past observation, not a fresh health lease.
	return json.NewEncoder(output).Encode(statusView{Event: event, Source: source, LiveHealthConfirmed: false, Status: status})
}

func runForeground(ctx context.Context, controller *as112ops.Controller, plan as112ops.Plan, recover bool, output io.Writer, notifier *systemdNotifier) (result error) {
	defer func() {
		// Cleanup precedes final output, so a blocked output pipe cannot retain
		// advertisements after shutdown. No writes to stdout occur while active.
		result = errors.Join(result, controller.Close())
		result = errors.Join(result, notifier.stopped())
		result = errors.Join(result, emitStatus(output, "owner-stopped", "persisted-after-close", controller.Status()))
	}()
	if err := emitStatus(output, "owner-starting", "persisted-journal", controller.Status()); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Duration(plan.HealthIntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if _, err := controller.Tick(ctx, recover); err != nil {
			return err // unknown/failed operations are never replayed by this loop
		}
		if ctx.Err() != nil {
			return nil
		}
		// Readiness means this owner completed a safe iteration (possibly withdrawn),
		// not that DNS is healthy or any route is globally visible. A notification
		// error exits through owned cleanup; no background goroutine renews a hang.
		if err := notifier.progress(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func prepare(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("prepare", flag.ContinueOnError)
	fs.SetOutput(output)
	root := fs.String("root", "", "new dedicated absolute instance directory")
	platform := fs.String("platform", "", "explicit bird2, bird3 or frr; other runtime adapters are unsupported")
	activeFile := fs.String("active-file", "", "private original approved BGP artifact")
	activeSHA := fs.String("active-checksum", "", "actual approved BGP file SHA256")
	dnsFile := fs.String("dns-file", "", "private original approved AS112 DNS bundle")
	dnsSHA := fs.String("dns-checksum", "", "actual approved DNS bundle SHA256")
	planFile := fs.String("plan", "", "new private plan file; never overwrites")
	routerBinary := fs.String("router-binary", "", "absolute matching native BIRD or FRR executable")
	pdns := fs.String("pdns-server", "", "absolute PowerDNS Authoritative executable")
	named := fs.String("named-checkzone", "", "absolute BIND zone checker executable")
	interval := fs.Int("health-interval", 5, "health polling interval, 1..30 seconds")
	healthy := fs.Int("recovery-healthy-checks", 2, "consecutive successful health checks before recovery, 2..10")
	if done, err := parseFlags(fs, args); done || err != nil {
		return err
	}
	if *platform != "bird2" && *platform != "bird3" && *platform != "frr" {
		return errors.New("AS112 supervision supports only BIRD 2/3 and FRR; OpenBGPD/Junos need separate verified adapters")
	}
	if *planFile == "" || !hashString(*activeSHA) || !hashString(*dnsSHA) {
		return errors.New("prepare requires --plan and both actual approved artifact checksums")
	}
	active, err := readPrivate(*activeFile, 16<<20)
	if err != nil {
		return errors.New("active artifact must be a private regular file")
	}
	dns, err := readPrivate(*dnsFile, 1<<20)
	if err != nil {
		return errors.New("DNS artifact must be a private regular file")
	}
	if routing.Checksum(string(active)) != *activeSHA || routing.Checksum(string(dns)) != *dnsSHA {
		return errors.New("approved artifact checksum mismatch; no plan written")
	}
	tools := as112ops.Tools{}
	for _, item := range []struct {
		path string
		pin  *as112ops.BinaryPin
	}{{*routerBinary, &tools.Router}, {*pdns, &tools.PDNS}, {*named, &tools.NamedCheckzone}} {
		*item.pin, err = as112ops.PinBinary(item.path)
		if err != nil {
			return errors.New("native executable cannot be pinned; use an absolute trusted native ELF path")
		}
	}
	host, err := as112ops.CaptureTarget()
	if err != nil {
		return errors.New("actual host boot/network namespace identity unavailable")
	}
	p, err := as112ops.BuildPlan(as112ops.PlanInput{Root: *root, Platform: *platform, ActiveConfig: string(active), DNSBundle: string(dns), Tools: tools, Host: host, HealthIntervalSeconds: *interval, RecoveryHealthyChecks: *healthy})
	if err != nil {
		return err
	}
	raw, err := as112ops.EncodePlan(p)
	if err != nil {
		return err
	}
	if err = writeNewPrivate(*planFile, raw); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Prepared        bool   `json:"prepared"`
		Installed       bool   `json:"installed"`
		HealthExecuted  bool   `json:"healthExecuted"`
		RouterID        string `json:"routerId"`
		PlanChecksum    string `json:"planChecksum"`
		ActiveChecksum  string `json:"activeChecksum"`
		StandbyChecksum string `json:"standbyChecksum"`
		DNSChecksum     string `json:"dnsChecksum"`
	}{true, false, false, p.RouterID, p.Checksum, p.ActiveChecksum, p.StandbyChecksum, p.DNSChecksum})
}
