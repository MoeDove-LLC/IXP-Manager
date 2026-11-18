package routing

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Native diagnostics can quote old running secrets, malformed lines, truncated
// values or vendor-escaped fragments. They are not safely handled by replacing
// only the current password. Keep raw output internal and return stage/code only.
func nativeFailure(stage string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s timed out: %w", stage, context.DeadlineExceeded)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s cancelled: %w", stage, context.Canceled)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("%s (exit status %d); raw diagnostics withheld", stage, exit.ExitCode())
	}
	return fmt.Errorf("%s; native diagnostic details withheld", stage)
}
func rejectRedactedArtifact(config string) error {
	if strings.Contains(config, RedactedBGPPassword) {
		return errors.New("redacted previews cannot be validated or applied; use the authorized full artifact download")
	}
	return nil
}
