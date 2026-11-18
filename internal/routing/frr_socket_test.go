package routing

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFRRWatchdogUnixSocketFlow(t *testing.T) {
	h := newFRRTestHost(t)
	socket := filepath.Join(filepath.Dir(h.opts.Target), "run", "agent.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- ServeFRRWatchdog(ctx, h.opts, socket) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		select {
		case err := <-finished:
			t.Fatalf("watchdog exited before listening: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("watchdog did not listen")
		}
		time.Sleep(10 * time.Millisecond)
	}
	info, err := os.Stat(socket)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("Unix control socket is not private")
	}
	status, err := RequestFRR(context.Background(), socket, "status", nil)
	if err != nil || status.Status != "idle" {
		t.Fatal(err)
	}
	state, err := RequestFRR(context.Background(), socket, "stage", h.request())
	if err != nil || state.Status != "pending" {
		t.Fatal(err)
	}
	_, err = RequestFRR(context.Background(), socket, "confirm", FRRDecision{ID: "wrong", Checksum: state.Checksum})
	if err == nil {
		t.Fatal("wrong transaction confirmed over socket")
	}
	// Stopping the supervised process gracefully restores the staged revision.
	cancel()
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	assertFile(t, h.opts.Target, h.old)
	assertFile(t, h.live, h.old)
}
