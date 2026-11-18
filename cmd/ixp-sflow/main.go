package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/sflow"
)

const usage = `Usage:
 ixp-sflow check-config --config /etc/moedove/sflow.json
 ixp-sflow listen --config /etc/moedove/sflow.json --token-file /etc/moedove/sflow.token
 ixp-sflow listen --config /etc/moedove/sflow.json --token-file /etc/moedove/sflow.token --forward

Only the explicit listen command opens the configured UDP IP:port. Configuration
and API token files must be owned private0600 regular files. Exporters require
exact transport source IP, agent address and sub-agent ID allowlisting. This is
not cryptographic UDP authentication. Only bounded metadata reaches the fixed
HTTPS API; packet headers, IP addresses and user payload are never uploaded.
Member forwarding is disabled unless --forward and an explicit private forwarding
policy are supplied. Every output requires a fresh per-packet manager grant.
Only sanitized protocol headers are relayed; no application payload, SNMP writes,
device configuration or interface capture. UDP writes do not prove delivery.`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	if args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(out, usage)
		return nil
	}
	if args[0] != "check-config" && args[0] != "listen" {
		return errors.New(usage)
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	f.SetOutput(io.Discard)
	path := f.String("config", "", "private fixed configuration")
	tokenPath := f.String("token-file", "", "private bound API token file")
	forward := f.Bool("forward", false, "explicitly enable approved sanitized-header relay under local policy")
	if err := f.Parse(args[1:]); err != nil {
		return errors.New("invalid flags; do not put credentials in command arguments")
	}
	if *path == "" || f.NArg() != 0 {
		return errors.New("explicit private config file required")
	}
	c, err := sflow.ReadConfig(*path)
	if err != nil {
		return err
	}
	if args[0] == "check-config" {
		if *tokenPath != "" || *forward {
			return errors.New("check-config does not use a token")
		}
		fmt.Fprintf(out, "Validated private collector configuration for %d explicit exporters; no socket opened.\n", len(c.Exporters))
		return nil
	}
	if *forward && c.Forwarding == nil {
		return errors.New("--forward requires an explicit private destination and rate policy")
	}
	if *tokenPath == "" {
		return errors.New("listen requires a private scoped API token file")
	}
	token, err := sflow.ReadToken(*tokenPath)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return listen(ctx, c, token, out, *forward)
}

type datagramSocket interface {
	Close() error
	SetReadBuffer(int) error
	SetReadDeadline(time.Time) error
	ReadFromUDPAddrPort([]byte) (int, netip.AddrPort, error)
}
type collectorIO struct {
	fetch          func(context.Context, sflow.Exporter) (sflow.Binding, error)
	upload         func(context.Context, sflow.Batch) error
	open           func(string) (datagramSocket, error)
	wait           func(context.Context, time.Duration) error
	now            func() time.Time
	forwarder      *sflow.Forwarder
	deliveryUpload func(context.Context, sflow.Batch) (*sflow.DeliveryEnvelope, error)
	report         func(context.Context, sflow.DeliveryReport) error
}

func listen(ctx context.Context, c sflow.Config, token string, out io.Writer, forward bool) error {
	client := sflow.NewHTTPClient()
	defer client.CloseIdleConnections()
	ops := collectorIO{
		fetch: func(ctx context.Context, e sflow.Exporter) (sflow.Binding, error) {
			return sflow.FetchBinding(ctx, client, c.Endpoint, token, e)
		},
		upload: func(ctx context.Context, b sflow.Batch) error { return sflow.Upload(ctx, client, c.Endpoint, token, b) },
		open: func(bind string) (datagramSocket, error) {
			address, err := net.ResolveUDPAddr("udp", bind)
			if err != nil {
				return nil, err
			}
			return net.ListenUDP("udp", address)
		},
		wait: func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
		now: time.Now,
	}
	if forward {
		if c.Forwarding == nil {
			return errors.New("explicit local forwarding policy required")
		}
		f, err := sflow.NewForwarder(*c.Forwarding, sflow.SendUDP, time.Now)
		if err != nil {
			return err
		}
		defer f.Close()
		ops.forwarder = f
		ops.deliveryUpload = func(ctx context.Context, b sflow.Batch) (*sflow.DeliveryEnvelope, error) {
			return sflow.UploadWithDeliveries(ctx, client, c.Endpoint, token, b)
		}
		ops.report = func(ctx context.Context, r sflow.DeliveryReport) error {
			return sflow.UploadDeliveryReport(ctx, client, c.Endpoint, token, r)
		}
	}
	return collectLoop(ctx, c, out, ops)
}

func collectLoop(ctx context.Context, c sflow.Config, out io.Writer, ops collectorIO) error {
	var conn datagramSocket
	closeSocket := func() {
		if conn != nil {
			_ = conn.Close()
			conn = nil
		}
	}
	defer closeSocket()
	var tracker sflow.Tracker
	buffer := make([]byte, sflow.MaxDatagramBytes+1)
	defer clear(buffer)
	bindings := map[string]bindingClock{}
	nextRefresh := time.Time{}
	window := ops.now()
	received := 0
	reported := false
	backoff := time.Second
	restart := func(cause error) error {
		closeSocket()
		bindings = map[string]bindingClock{}
		nextRefresh = time.Time{}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !sflow.RetryableControl(cause) {
			return errors.New("sFlow control authorization or configuration rejected; listener stopped")
		}
		fmt.Fprintln(out, "sFlow control temporarily unavailable or mapping changed; discarded queued packets and refreshing bindings after backoff.")
		if err := ops.wait(ctx, backoff); err != nil {
			return err
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
		return nil
	}
	for ctx.Err() == nil {
		now := ops.now()
		if conn == nil || !now.Before(nextRefresh) {
			// Never retain a receive queue across a control-plane epoch change.
			closeSocket()
			fresh := map[string]bindingClock{}
			expiry := time.Time{}
			var refreshErr error
			for _, e := range c.Exporters {
				requestedAt := ops.now()
				binding, err := ops.fetch(ctx, e)
				responseAt := ops.now()
				if err != nil {
					refreshErr = err
					break
				}
				if ops.forwarder != nil && !binding.ForwardingAllowed {
					refreshErr = &sflow.ControlError{StatusCode: 403}
					break
				}
				anchored, err := anchorBinding(binding, requestedAt, responseAt)
				if err != nil {
					refreshErr = err
					break
				}
				fresh[e.ID] = anchored
				if expiry.IsZero() || anchored.deadline.Before(expiry) {
					expiry = anchored.deadline
				}
			}
			if refreshErr == nil && !ops.now().Add(time.Second).Before(expiry) {
				refreshErr = &sflow.ControlError{StatusCode: 409, Temporary: true}
			}
			if refreshErr != nil {
				if err := restart(refreshErr); err != nil {
					if ctx.Err() != nil {
						return nil
					}
					return err
				}
				continue
			}
			opened, err := ops.open(c.Bind)
			if err != nil {
				return errors.New("sFlow listener could not bind the explicitly configured address")
			}
			conn = opened
			_ = conn.SetReadBuffer(1 << 20)
			bindings = fresh
			nextRefresh = expiry.Add(-time.Second)
			if !reported {
				if ops.forwarder == nil {
					fmt.Fprintln(out, "sFlow metadata listener active; allowlisting is not UDP authentication; member forwarding disabled.")
				} else {
					fmt.Fprintln(out, "sFlow metadata listener active; sanitized-header relay requires a new packet grant and local destination policy for every delivery.")
				}
				reported = true
			}
		}
		deadline := ops.now().Add(time.Second)
		if nextRefresh.Before(deadline) {
			deadline = nextRefresh
		}
		_ = conn.SetReadDeadline(deadline)
		n, source, err := conn.ReadFromUDPAddrPort(buffer)
		localReceivedAt := ops.now()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if e, ok := err.(net.Error); ok && e.Timeout() {
				continue
			}
			return errors.New("sFlow UDP receive failed")
		}
		if n > sflow.MaxDatagramBytes {
			clear(buffer)
			continue
		}
		if localReceivedAt.Sub(window) >= time.Second {
			window = localReceivedAt
			received = 0
		}
		received++
		if received > c.MaxDatagramsPerSecond {
			clear(buffer[:n])
			continue
		}
		sourceIP := source.Addr().Unmap().String()
		allowed := false
		for _, e := range c.Exporters {
			if e.SourceIP == sourceIP {
				allowed = true
				break
			}
		}
		if !allowed {
			clear(buffer[:n])
			continue
		}
		var captured *sflow.CapturedPacket
		var d sflow.Datagram
		if ops.forwarder != nil {
			captured, err = sflow.CaptureAt(buffer[:n], localReceivedAt)
			if err == nil {
				d = captured.Metadata()
			}
		} else {
			d, err = sflow.Parse(buffer[:n])
		}
		clear(buffer[:n])
		if err != nil {
			continue
		}
		discard := func() {
			if captured != nil {
				captured.Destroy()
			}
		}
		var exporter *sflow.Exporter
		for i := range c.Exporters {
			e := &c.Exporters[i]
			if e.SourceIP == sourceIP && e.AgentAddress == d.AgentAddress && e.SubAgentID == d.SubAgentID {
				exporter = e
				break
			}
		}
		if exporter == nil {
			discard()
			continue
		}
		anchored := bindings[exporter.ID]
		at, valid := anchored.receiptAt(localReceivedAt)
		if !valid {
			discard()
			continue
		}
		binding := anchored.binding
		quality, err := tracker.Observe(exporter.ID, d)
		if err != nil {
			discard()
			continue
		}
		batch := sflow.Batch{Version: 1, ExporterID: exporter.ID, SwitchID: exporter.SwitchID, MappingRevision: binding.MappingRevision, ReceivedAt: at, TransportSource: sourceIP, Datagram: d, Quality: quality}
		var envelope *sflow.DeliveryEnvelope
		requestAt := ops.now()
		if ops.forwarder != nil {
			batch.RequestDeliveries = true
			batch.RequestNonce = captured.Nonce()
			batch.DatagramDigest = captured.Digest()
			if ops.deliveryUpload == nil {
				discard()
				return errors.New("forwarding upload adapter unavailable")
			}
			envelope, err = ops.deliveryUpload(ctx, batch)
		} else {
			err = ops.upload(ctx, batch)
		}
		responseAt := ops.now()
		if err != nil {
			discard()
			// batch is deliberately not queued/retried. The server may already
			// have committed it; subsequent packets carry the newly read lease.
			if err = restart(err); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			continue
		}
		if ops.forwarder != nil {
			report, forwardErr := ops.forwarder.Forward(ctx, captured, batch, envelope, requestAt, responseAt, anchored.uncertainty)
			if forwardErr != nil {
				fmt.Fprintln(out, "Per-packet delivery grant rejected; no stale packet will be retried.")
			}
			if forwardErr == nil && len(report.Receipts) > 0 && ops.report != nil {
				if err := ops.report(ctx, report); err != nil {
					fmt.Fprintln(out, "Delivery report unavailable; UDP packets are not retried and remote receipt remains unconfirmed.")
				}
			}
		}
		discard()
		backoff = time.Second
	}
	return nil
}
