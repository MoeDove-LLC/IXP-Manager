package migrate

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func logoSourceFixture(t *testing.T) (string, LogoSource, []byte, string) {
	t.Helper()
	var data bytes.Buffer
	if e := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 4, 3))); e != nil {
		t.Fatal(e)
	}
	sum := sha1.Sum(data.Bytes())
	key := hex.EncodeToString(sum[:]) + ".png"
	root := t.TempDir()
	file := filepath.Join(root, "logos", key[:1], key[1:2], key)
	if e := os.MkdirAll(filepath.Dir(file), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(file, data.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	return root, LogoSource{ID: "1", MemberID: "legacy-members-1", StoredName: key, OriginalName: "../../display-only.png", UploadedBy: "source-name"}, data.Bytes(), file
}
func TestLogoSourceExplicitRootAndPassiveBundle(t *testing.T) {
	root, source, data, _ := logoSourceFixture(t)
	img, e := ReadLogoSource(root, source)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := EncodeLogoBundle([]LogoItem{{Source: source, PNG: img.PNG}})
	if e != nil {
		t.Fatal(e)
	}
	b, e := ReadLogoBundle(bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	if len(b.Items) != 1 || b.Items[0].Width != 4 || b.Items[0].Height != 3 || !bytes.Equal(data, b.Items[0].PNG) {
		t.Fatal("image/source normalization lost")
	}
	if _, e = ReadLogoBundle(strings.NewReader(string(raw) + ` {}`)); e == nil {
		t.Fatal("trailing bundle accepted")
	}
	if _, e = EncodeLogoBundle([]LogoItem{{Source: source, PNG: data}, {Source: source, PNG: data}}); e == nil {
		t.Fatal("ambiguous multiple current logos accepted")
	}
	bad := source
	bad.StoredName = "../../secret"
	if _, e = ReadLogoSource(root, bad); e == nil {
		t.Fatal("path key accepted")
	}
}
func TestLogoSourceRejectsLinksTamperingAndActiveImages(t *testing.T) {
	for _, mode := range []string{"symlink", "hardlink", "ancestor", "tamper"} {
		t.Run(mode, func(t *testing.T) {
			root, s, data, path := logoSourceFixture(t)
			switch mode {
			case "symlink":
				_ = os.Remove(path)
				target := filepath.Join(t.TempDir(), "outside.png")
				_ = os.WriteFile(target, data, 0600)
				_ = os.Symlink(target, path)
			case "hardlink":
				_ = os.Link(path, filepath.Join(t.TempDir(), "second.png"))
			case "ancestor":
				alias := filepath.Join(t.TempDir(), "alias")
				_ = os.Symlink(root, alias)
				root = alias
			case "tamper":
				_ = os.WriteFile(path, append(data, []byte("modified")...), 0600)
			}
			if _, e := ReadLogoSource(root, s); e == nil {
				t.Fatal("unsafe source accepted")
			}
		})
	}
	_, s, _, _ := logoSourceFixture(t)
	if _, e := EncodeLogoBundle([]LogoItem{{Source: s, PNG: []byte("<svg><script/></svg>")}}); e == nil {
		t.Fatal("active image bundle accepted")
	}
}
