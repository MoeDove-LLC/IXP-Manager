package as112

import (
	"errors"
	"os"
	"path/filepath"
)

// WriteNewDirectory extracts only an exactly regenerated bundle, creates a new
// private directory, and never replaces an installed configuration or symlink.
func WriteNewDirectory(path string, b Bundle) error {
	if err := Validate(b); err != nil {
		return err
	}
	if path == "" || filepath.Clean(path) != path {
		return errors.New("new AS112 output directory must be a clean path")
	}
	if err := os.Mkdir(path, 0700); err != nil {
		return errors.New("AS112 output directory must not already exist")
	}
	good := false
	defer func() {
		if !good {
			_ = os.RemoveAll(path)
		}
	}()
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.Mkdir("zones", 0700); err != nil {
		return err
	}
	for _, file := range b.Files {
		f, err := root.OpenFile(file.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errors.New("cannot write private AS112 file")
		}
		_, err = f.WriteString(file.Content)
		if err == nil {
			err = f.Sync()
		}
		closed := f.Close()
		if err != nil || closed != nil {
			return errors.New("AS112 file write did not complete")
		}
	}
	good = true
	return nil
}
