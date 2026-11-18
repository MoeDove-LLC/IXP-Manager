package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/routing"
)

func runBIRD1(mode, file string, opts routing.BIRD1ApplyOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if mode == "recover" {
		if e := routing.RecoverBIRD1(ctx, opts); e != nil {
			return e
		}
		fmt.Println("BIRD1 exact previous disk and runtime configuration verified restored; no daemon was started.")
		return nil
	}
	if mode != "apply" {
		return errors.New("BIRD1 supports fetch/check/apply/recover only; no FRR watchdog command is reused")
	}
	if file == "" {
		return errors.New("BIRD1 apply requires a private reviewed --file")
	}
	input, e := os.OpenFile(file, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return errors.New("BIRD1 candidate artifact unavailable")
	}
	defer input.Close()
	info, e := input.Stat()
	if e != nil {
		return e
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || info.Size() > 16<<20 {
		return errors.New("BIRD1 candidate must be an owned single-link 0600 regular file")
	}
	raw, e := io.ReadAll(io.LimitReader(input, (16<<20)+1))
	if e != nil || len(raw) > 16<<20 {
		return errors.New("BIRD1 candidate read exceeded its bound")
	}
	if e = routing.ApplyBIRD1(ctx, string(raw), opts); e != nil {
		return e
	}
	fmt.Println("BIRD1 dedicated configuration confirmed with exact disk/runtime readback; BGP convergence is not implied.")
	return nil
}
