package migrate

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func standardDocstoreBundleLimits() patchBundleLimits {
	return patchBundleLimits{manifest: DocstoreManifestLimit, file: DocstoreFileLimit, unpacked: DocstoreBundleLimit, compressed: DocstoreCompressedLimit, entries: DocstoreEntryLimit}
}

// ReadDocstoreBundle accepts only the exact manifest-declared regular entries.
// It checks cumulative decompressed bytes, entry count, each digest and size,
// gzip footer/trailing streams and tar padding before returning a usable DTO.
// consume receives provisional content: callers must discard it on ANY error
// and must not commit imported state until this function succeeds.
func ReadDocstoreBundle(input io.Reader, consume func(DocstoreFileSource, io.Reader) error) (*DocstoreBundle, error) {
	return readDocstoreBundle(input, consume, standardDocstoreBundleLimits())
}
func readDocstoreBundle(input io.Reader, consume func(DocstoreFileSource, io.Reader) error, limits patchBundleLimits) (*DocstoreBundle, error) {
	compressed := &io.LimitedReader{R: input, N: limits.compressed + 1}
	buffer := bufio.NewReader(compressed)
	gz, err := gzip.NewReader(buffer)
	if err != nil {
		return nil, errors.New("invalid gzip docstore bundle")
	}
	defer gz.Close()
	gz.Multistream(false)
	limited := &io.LimitedReader{R: gz, N: limits.unpacked + 1}
	h, err := readPatchTarHeader(limited)
	if err != nil || h.Name != "manifest.json" || h.Size < 1 || h.Size > limits.manifest {
		return nil, errors.New("first bundle entry must be a bounded regular manifest.json")
	}
	raw, err := io.ReadAll(io.LimitReader(limited, h.Size))
	if err != nil || int64(len(raw)) != h.Size {
		return nil, errors.New("could not read complete docstore manifest")
	}
	if err = readPatchTarPadding(limited, h.Size); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var bundle DocstoreBundle
	if err = decoder.Decode(&bundle); err != nil {
		return nil, fmt.Errorf("invalid docstore manifest: %w", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("one docstore manifest required")
	}
	if err = validateDocstoreBundle(&bundle, true); err != nil {
		return nil, err
	}
	if len(bundle.Docstore.Files)+1 > limits.entries {
		return nil, errors.New("docstore bundle exceeds entry limit")
	}
	expected := map[string]DocstoreFileSource{}
	for _, f := range bundle.Docstore.Files {
		if f.Size > limits.file {
			return nil, errors.New("docstore file exceeds file limit")
		}
		expected[f.Entry] = f
	}
	seen := map[string]bool{"manifest.json": true}
	count := 1
	for {
		h, err = readPatchTarHeader(limited)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		count++
		if count > limits.entries || seen[h.Name] {
			return nil, errors.New("unsafe or duplicate docstore bundle entry")
		}
		f, ok := expected[h.Name]
		if !ok || h.Size != f.Size || h.Size > limits.file {
			return nil, errors.New("unexpected or mismatched docstore bundle entry")
		}
		seen[h.Name] = true
		hash := sha256.New()
		reader := &patchCounterReader{Reader: io.TeeReader(io.LimitReader(limited, h.Size), hash)}
		if consume == nil {
			_, err = io.Copy(io.Discard, reader)
		} else {
			err = consume(f, reader)
		}
		if err != nil {
			return nil, err
		}
		if reader.count != f.Size || hex.EncodeToString(hash.Sum(nil)) != f.Checksum {
			return nil, fmt.Errorf("attachment %s content integrity mismatch", f.ID)
		}
		if err = readPatchTarPadding(limited, h.Size); err != nil {
			return nil, err
		}
	}
	if len(seen) != len(expected)+1 {
		return nil, errors.New("docstore bundle is missing declared documents")
	}
	padding := make([]byte, 32768)
	for {
		n, e := limited.Read(padding)
		for _, v := range padding[:n] {
			if v != 0 {
				return nil, errors.New("unexpected data after docstore tar")
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, errors.New("invalid docstore gzip footer")
		}
		if limited.N <= 0 {
			return nil, errors.New("docstore bundle exceeds cumulative unpacked limit")
		}
	}
	if limited.N <= 0 {
		return nil, errors.New("docstore bundle exceeds cumulative unpacked limit")
	}
	if _, err = buffer.ReadByte(); err != io.EOF {
		return nil, errors.New("trailing or concatenated docstore bundle data")
	}
	if compressed.N <= 0 {
		return nil, errors.New("docstore bundle exceeds compressed size limit")
	}
	return &bundle, nil
}
