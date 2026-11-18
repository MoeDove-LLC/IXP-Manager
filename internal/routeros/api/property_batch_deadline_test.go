package api

import (
	"context"
	"errors"
	"testing"
	"time"
)

// This uses the real certificate-verified TLS fixture and real connection
// deadlines. Parent.Err alone cannot classify a getter's shorter deadline.
func TestPropertyPipelineDeadlineMetadataDistinguishesParentAndCommandFromEOF(t *testing.T) {
	for _, mode := range []string{"command", "context", "eof"} {
		t.Run(mode, func(t *testing.T) {
			fixture := mockTLS(t, func(peer *mockPeer) error {
				if err := loginOK(peer); err != nil {
					return err
				}
				requests := make([][]string, 3)
				for i := range requests {
					var err error
					requests[i], err = peer.read()
					if err != nil {
						return err
					}
				}
				if err := peer.response("!done", requestTag(requests[0]), Record{"ret": ""}); err != nil {
					return err
				}
				if mode != "eof" {
					var one [1]byte
					_, _ = peer.conn.Read(one[:]) // wait for the client to close
				}
				return nil
			})
			client := dialFixture(t, fixture)
			parentTimeout := 2 * time.Second
			if mode == "command" {
				client.timeout = 100 * time.Millisecond
			} else if mode == "context" {
				parentTimeout = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), parentTimeout)
			defer cancel()
			values, err := client.ReadProperties(ctx, getterQueries())
			var failure *Error
			// The reader counts sentence attempts: one completed !done, then
			// the next sentence attempt ends in the deadline or EOF.
			if !errors.As(err, &failure) || values != nil || !client.closed.Load() || failure.ReplyRecords != 1 || failure.ReplySentences != 2 || failure.ReplyBytes == 0 {
				t.Fatal("failed getter did not retain safe completion counts")
			}
			if mode == "eof" {
				if failure.Code != CodeTransport || failure.Deadline || failure.DeadlineKind != "none" || ctx.Err() != nil {
					t.Fatal("EOF was misclassified as a deadline")
				}
			} else if !failure.Deadline || failure.DeadlineKind != mode || failure.Code != CodeTransport && failure.Code != CodeCancelled {
				t.Fatal("getter deadline metadata was lost")
			}
			if mode == "command" && ctx.Err() != nil {
				t.Fatal("shorter command deadline incorrectly required parent expiry")
			}
			if _, err := client.ReadProperties(context.Background(), getterQueries()); err == nil {
				t.Fatal("closed partial-result channel was reused")
			}
		})
	}
}
