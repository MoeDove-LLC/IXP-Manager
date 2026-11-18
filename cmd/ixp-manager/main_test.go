package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestManagerBindFailureDoesNotInitializeData(t *testing.T) {
	const childListen = "MOEDOVE_TEST_OCCUPIED_MANAGER_LISTENER"
	const childData = "MOEDOVE_TEST_OCCUPIED_MANAGER_DATA"
	if addr := os.Getenv(childListen); addr != "" {
		os.Args = []string{os.Args[0], "-listen", addr, "-data", os.Getenv(childData)}
		main()
		return
	}
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	data := filepath.Join(t.TempDir(), "new-data")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagerBindFailureDoesNotInitializeData$")
	child.Env = append(os.Environ(), childListen+"="+occupied.Addr().String(), childData+"="+data)
	output, err := child.CombinedOutput()
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != 1 || !bytes.Contains(output, []byte("listen tcp")) {
		t.Fatalf("manager did not refuse the occupied listener: %v (%s)", err, output)
	}
	if _, err := os.Lstat(data); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bind failure touched the storage directory: %v", err)
	}
}
