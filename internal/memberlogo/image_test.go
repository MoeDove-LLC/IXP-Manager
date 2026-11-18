package memberlogo

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func TestNormalizePassiveDecodedImagesAndLimits(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	img.Set(1, 1, color.NRGBA{R: 100, A: 120})
	var p, j bytes.Buffer
	_ = png.Encode(&p, img)
	_ = jpeg.Encode(&j, img, nil)
	for _, data := range [][]byte{p.Bytes(), j.Bytes(), append(append([]byte{}, p.Bytes()...), []byte("<script>active-tail</script>")...)} {
		got, e := Normalize(bytes.NewReader(data))
		if e != nil {
			t.Fatal(e)
		}
		if got.Width != 3 || got.Height != 2 || bytes.Contains(got.PNG, []byte("active-tail")) {
			t.Fatal("unsafe or incorrect normalized image")
		}
		if _, e = png.Decode(bytes.NewReader(got.PNG)); e != nil {
			t.Fatal(e)
		}
	}
	oversize := append([]byte{}, p.Bytes()...)
	binary.BigEndian.PutUint32(oversize[16:20], 4097)
	binary.BigEndian.PutUint32(oversize[29:33], crc32.ChecksumIEEE(oversize[12:29]))
	for name, data := range map[string][]byte{"svg": []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script/></svg>`), "truncated": p.Bytes()[:40], "dimensions": oversize, "empty": nil, "bytes": []byte(strings.Repeat("x", MaxInputBytes+1))} {
		t.Run(name, func(t *testing.T) {
			if _, e := Normalize(bytes.NewReader(data)); e == nil {
				t.Fatal("invalid upload accepted")
			}
		})
	}
}
