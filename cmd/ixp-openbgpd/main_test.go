package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestOpenBGPDCLIRequiresExplicitActionsAndPrivateArtifact(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"--help"}, &out); err != nil || !strings.Contains(out.String(), "not OpenBGPD-native confirmed commit") {
		t.Fatal(err, out.String())
	}
	for _, args := range [][]string{{"apply"}, {"serve"}, {"stage"}, {"confirm"}, {"cancel"}, {"check", "--file", "missing", "--checksum", strings.Repeat("0", 64)}} {
		if err := run(args, io.Discard); err == nil {
			t.Fatal("incomplete/unsafe command accepted", args)
		}
	}
	path := filepath.Join(t.TempDir(), "artifact")
	os.WriteFile(path, []byte("test"), 0644)
	if _, err := readArtifact(path); err == nil {
		t.Fatal("public file accepted")
	}
	os.Chmod(path, 0600)
	if v, err := readArtifact(path); err != nil || v != "test" {
		t.Fatal(err)
	}
	link := path + "-link"
	os.Symlink(path, link)
	if _, err := readArtifact(link); err == nil {
		t.Fatal("symlink accepted")
	}
	fifo := path + "-fifo"
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readArtifact(fifo); err == nil {
		t.Fatal("FIFO accepted")
	}
}
