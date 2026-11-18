package migrate

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// WriteNoteArchive publishes one new private file. The selected real parent
// directory is pinned by a descriptor before any data is written. Link never
// replaces an existing file or symlink; temporary content is cleaned on failure.
func WriteNoteArchive(output string, archive NoteArchive) error {
	if output == "" || output == "-" || strings.HasSuffix(output, string(filepath.Separator)) {
		return errors.New("note source archives require an explicit new output file")
	}
	raw, err := EncodeNoteArchive(archive)
	if err != nil {
		return err
	}
	parentPath := filepath.Dir(output)
	leaf := filepath.Base(output)
	if leaf == "." || leaf == ".." {
		return errors.New("note source archive output must name a new file")
	}
	parent, err := openPatchRoot(parentPath)
	if err != nil {
		return err
	}
	defer parent.Close()
	info, err := parent.Stat()
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 {
		return errors.New("note archive output directory must be owned and not writable by other users")
	}
	root, err := os.OpenRoot(parentPath)
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("note archive output directory changed")
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	temporary := ".note-source-" + hex.EncodeToString(nonce[:])
	f, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	defer f.Close()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = root.Link(temporary, leaf); err != nil {
		return errors.New("note archive output already exists or could not be published")
	}
	if err = root.Remove(temporary); err != nil {
		return err
	}
	return parent.Sync()
}
