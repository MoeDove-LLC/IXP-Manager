package routing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ValidateBIRD1 accepts only the exact generated subset and the actual BIRD 1
// executable compiled for its family. bird and bird6 report the same version,
// so a real family parse probe is mandatory, including for an empty peer set.
// This does not authorize any live apply/reload adapter.
func ValidateBIRD1(ctx context.Context, binary, config string) error {
	family, e := BIRD1Family(config)
	if e != nil {
		return e
	}
	if binary == "" {
		binary = "bird"
		if family == 6 {
			binary = "bird6"
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	version, e := run(ctx, binary, "--version")
	if e != nil {
		return nativeFailure("BIRD 1 executable unavailable", e)
	}
	match := birdVersionLine.FindStringSubmatch(version)
	if match == nil || match[1] != "1" {
		return errors.New("validation requires actual BIRD 1; installed major version does not match")
	}
	dir, e := os.MkdirTemp("", "moedove-bird1-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	prefix := "192.0.2.0/24"
	other := "2001:db8::/32"
	if family == 6 {
		prefix, other = other, prefix
	}
	for _, probe := range []struct {
		name, prefix string
		wantPass     bool
	}{{"family", prefix, true}, {"other-family", other, false}} {
		text := fmt.Sprintf("router id 192.0.2.112;\nprotocol static { route %s blackhole; }\n", probe.prefix)
		file := filepath.Join(dir, probe.name+".conf")
		if e = os.WriteFile(file, []byte(text), 0600); e != nil {
			return e
		}
		_, e = run(ctx, binary, "-p", "-c", file)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if probe.wantPass && e != nil {
			return nativeFailure("BIRD 1 checker does not match the reviewed address family", e)
		}
		if !probe.wantPass && e == nil {
			return errors.New("BIRD 1 checker unexpectedly accepts both families; separate native bird and bird6 executables required")
		}
	}
	file := filepath.Join(dir, "candidate.conf")
	if e = os.WriteFile(file, []byte(config), 0600); e != nil {
		return e
	}
	if _, e = run(ctx, binary, "-p", "-c", file); e != nil {
		return nativeFailure("BIRD 1 parse rejected configuration", e)
	}
	return nil
}
