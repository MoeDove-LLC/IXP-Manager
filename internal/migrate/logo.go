package migrate

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/moedove/ixp-manager/internal/memberlogo"
)

const LogoBundleLimit = 48 << 20
const LogoDecodedLimit = 32 << 20
const LogoCountLimit = 1000
const LogoPixelLimit = 64_000_000
const LogoBundleKind = "ixp-manager-member-logos"

type LogoSource struct {
	ID           string `json:"id"`
	MemberID     string `json:"memberId"`
	StoredName   string `json:"storedName"`
	OriginalName string `json:"originalName"`
	UploadedBy   string `json:"uploadedBy"`
	UploadedAt   string `json:"uploadedAt"`
}
type LogoItem struct {
	Source LogoSource `json:"source"`
	PNG    []byte     `json:"png"`
	Width  int        `json:"-"`
	Height int        `json:"-"`
}
type LogoBundle struct {
	Version int        `json:"version"`
	Kind    string     `json:"kind"`
	Items   []LogoItem `json:"items"`
}

var logoKey = regexp.MustCompile(`^[a-f0-9]{40}\.png$`)
var logoMember = regexp.MustCompile(`^legacy-members-[1-9][0-9]*$`)
var logoSourceID = regexp.MustCompile(`^[1-9][0-9]*$`)

func ValidateLogoSource(s LogoSource) error {
	if len(s.ID) > 19 || len(s.MemberID) > 34 || !logoKey.MatchString(s.StoredName) || !logoMember.MatchString(s.MemberID) || !logoSourceID.MatchString(s.ID) {
		return errors.New("invalid legacy logo identity or stored PNG key")
	}
	for _, v := range []string{s.OriginalName, s.UploadedBy, s.UploadedAt} {
		if !utf8.ValidString(v) || len(v) > 1024 || strings.ContainsAny(v, "\x00\r\n") {
			return errors.New("legacy logo source text must be bounded single-line text")
		}
	}
	return nil
}

// The explicit legacy public root is traversed through descriptors. No source
// filename, environment setting, URL or manifest-supplied path is opened.
func ReadLogoSource(root string, source LogoSource) (memberlogo.Image, error) {
	if err := ValidateLogoSource(source); err != nil {
		return memberlogo.Image{}, err
	}
	current, err := openPatchRoot(root)
	if err != nil {
		return memberlogo.Image{}, err
	}
	parts := []string{"logos", source.StoredName[:1], source.StoredName[1:2], source.StoredName}
	for i, part := range parts {
		flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= syscall.O_DIRECTORY
		}
		fd, e := syscall.Openat(int(current.Fd()), part, flags, 0)
		current.Close()
		if e != nil {
			return memberlogo.Image{}, errors.New("legacy logo must be a regular file beneath the explicit public root")
		}
		current = os.NewFile(uintptr(fd), "legacy-logo")
	}
	defer current.Close()
	before, err := current.Stat()
	if err != nil {
		return memberlogo.Image{}, err
	}
	valid := func(st os.FileInfo) bool {
		v, ok := st.Sys().(*syscall.Stat_t)
		return st.Mode().IsRegular() && st.Size() > 0 && st.Size() <= memberlogo.MaxInputBytes && ok && v.Nlink == 1
	}
	if !valid(before) {
		return memberlogo.Image{}, errors.New("legacy logo exceeds the byte limit, is special, or has hard links")
	}
	data, err := io.ReadAll(io.LimitReader(current, memberlogo.MaxInputBytes+1))
	if err != nil {
		return memberlogo.Image{}, err
	}
	after, err := current.Stat()
	if err != nil || !valid(after) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || int64(len(data)) != before.Size() {
		return memberlogo.Image{}, errors.New("legacy logo changed while being read")
	}
	sum := sha1.Sum(data) // Legacy content identity only; new blob integrity uses SHA-256.
	if hex.EncodeToString(sum[:])+".png" != source.StoredName {
		return memberlogo.Image{}, errors.New("legacy logo content does not match its original stored key")
	}
	return memberlogo.Normalize(bytes.NewReader(data))
}

func EncodeLogoBundle(items []LogoItem) ([]byte, error) {
	b := LogoBundle{Version: 1, Kind: LogoBundleKind, Items: items}
	if _, err := validateLogoBundle(&b); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	if len(raw) > LogoBundleLimit {
		return nil, errors.New("logo bundle exceeds 48 MiB")
	}
	return raw, nil
}
func ReadLogoBundle(reader io.Reader) (*LogoBundle, error) {
	limited := &io.LimitedReader{R: reader, N: LogoBundleLimit + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var b LogoBundle
	if err := decoder.Decode(&b); err != nil {
		return nil, err
	}
	if decoder.Decode(new(any)) != io.EOF || limited.N <= 0 {
		return nil, errors.New("logo bundle must be one JSON document of at most 48 MiB")
	}
	if _, err := validateLogoBundle(&b); err != nil {
		return nil, err
	}
	return &b, nil
}
func validateLogoBundle(b *LogoBundle) (int, error) {
	if b.Version != 1 || b.Kind != LogoBundleKind || len(b.Items) < 1 || len(b.Items) > LogoCountLimit {
		return 0, errors.New("invalid logo bundle version, kind or item count")
	}
	seen := map[string]bool{}
	total := 0
	var pixels int64
	for i, item := range b.Items {
		if err := ValidateLogoSource(item.Source); err != nil {
			return 0, err
		}
		if seen[item.Source.MemberID] {
			return 0, errors.New("multiple logos for one member require manual review")
		}
		seen[item.Source.MemberID] = true
		if len(item.PNG) > memberlogo.MaxInputBytes {
			return 0, errors.New("bundled logo exceeds upload byte limit")
		}
		img, err := memberlogo.Normalize(bytes.NewReader(item.PNG))
		if err != nil {
			return 0, fmt.Errorf("logo %s: %w", item.Source.ID, err)
		}
		if !bytes.Equal(item.PNG, img.PNG) {
			return 0, errors.New("logo bundle requires normalized PNG bytes")
		}
		pixels += int64(img.Width) * int64(img.Height)
		if pixels > LogoPixelLimit {
			return 0, errors.New("logo bundle exceeds 64 million cumulative decoded pixels")
		}
		total += len(img.PNG)
		if total > LogoDecodedLimit {
			return 0, errors.New("logo bundle exceeds 32 MiB cumulative PNG data")
		}
		b.Items[i].PNG = img.PNG
		b.Items[i].Width = img.Width
		b.Items[i].Height = img.Height
	}
	return total, nil
}
