package control

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
)

func TestStatusDoesNotWaitForBlockedPrepareOrActivation(t *testing.T) {
	for _, phase := range []routeros.Phase{routeros.Prepare, routeros.Activate} {
		t.Run(string(phase), func(t *testing.T) {
			r, request := fixtureRuntime(t)
			entered := make(chan string, 1)
			release := make(chan struct{})
			var once, releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			r.beforeApply = func(m routeros.APIMutation) {
				if m.Phase == phase {
					once.Do(func() { entered <- m.OperationID; <-release })
				}
			}
			s := openTest(t, testConfig(t, r), r)
			completed := make(chan error, 1)
			go func() { _, err := s.Stage(context.Background(), request); completed <- err }()
			var operation string
			select {
			case operation = <-entered:
			case <-time.After(time.Second):
				releaseOnce.Do(func() { close(release) })
				t.Fatal("write fixture not reached")
			}
			read := make(chan Status, 1)
			go func() { read <- s.Status() }()
			select {
			case status := <-read:
				want := Preparing
				if phase == routeros.Activate {
					want = Activating
				}
				if status.Phase != want || status.LastOperationID != operation || status.TransactionID == "" {
					t.Fatalf("durable progress not visible: %+v", status)
				}
				status.LastCode = "caller-mutation"
				if s.Status().LastCode == status.LastCode {
					t.Fatal("published status was mutable by reader")
				}
			case <-time.After(250 * time.Millisecond):
				releaseOnce.Do(func() { close(release) })
				t.Fatal("status waited behind device I/O")
			}
			releaseOnce.Do(func() { close(release) })
			if err := <-completed; err != nil {
				t.Fatal(err)
			}
			if s.Status().Phase != AwaitingConfirmation {
				t.Fatal("final stage state not published")
			}
		})
	}
}
