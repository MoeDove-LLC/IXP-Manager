// Package memberlogo converts untrusted uploads into bounded, passive PNGs.
package memberlogo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
)

const (
	MaxInputBytes  = 5 << 20
	MaxOutputBytes = 5 << 20
	MaxDimension   = 4096
	MaxPixels      = 8_000_000
)

type Image struct {
	PNG           []byte
	Width, Height int
	Checksum      string
}

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > MaxOutputBytes-b.Len() {
		return 0, errors.New("normalized PNG exceeds 5 MiB")
	}
	return b.Buffer.Write(p)
}

func Normalize(reader io.Reader) (Image, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxInputBytes+1))
	if err != nil {
		return Image{}, err
	}
	if len(data) == 0 || len(data) > MaxInputBytes {
		return Image{}, errors.New("logo upload must be between 1 byte and 5 MiB")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		return Image{}, errors.New("a valid PNG or JPEG image is required; SVG and other formats are not accepted")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxDimension || cfg.Height > MaxDimension || int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return Image{}, errors.New("logo exceeds 4096 pixels per side or 8 million pixels")
	}
	img, decodedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil || decodedFormat != format {
		return Image{}, errors.New("logo pixels could not be decoded")
	}
	if img.Bounds().Dx() != cfg.Width || img.Bounds().Dy() != cfg.Height {
		return Image{}, errors.New("logo dimensions changed during decoding")
	}
	var out boundedBuffer
	if err := png.Encode(&out, img); err != nil {
		return Image{}, err
	}
	sum := sha256.Sum256(out.Bytes())
	return Image{PNG: out.Bytes(), Width: cfg.Width, Height: cfg.Height, Checksum: hex.EncodeToString(sum[:])}, nil
}
