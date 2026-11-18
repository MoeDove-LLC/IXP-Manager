package as112ops

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Same-device bind mounts do not change st_dev. Installer-owned version trees
// never introduce nested mounts, so reject such aliases using this process's
// mount table in addition to the inode/device checks. No mount is modified.
func retentionMounts(root string) error {
	f, e := os.Open("/proc/self/mountinfo")
	if e != nil {
		return errors.New("retention mount ownership unavailable")
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if e != nil || len(raw) > 4<<20 {
		return errors.New("retention mount inventory unavailable or oversized")
	}
	return checkRetentionMounts(root, string(raw))
}
func checkRetentionMounts(root, raw string) error {
	versions := filepath.Join(root, "versions")
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			return errors.New("invalid mount ownership inventory")
		}
		encoded := fields[4]
		var decoded strings.Builder
		for at := 0; at < len(encoded); at++ {
			if encoded[at] != '\\' {
				decoded.WriteByte(encoded[at])
				continue
			}
			if at+3 >= len(encoded) {
				return errors.New("invalid mount path encoding")
			}
			value, e := strconv.ParseUint(encoded[at+1:at+4], 8, 8)
			if e != nil || value == 0 {
				return errors.New("invalid mount path encoding")
			}
			decoded.WriteByte(byte(value))
			at += 3
		}
		point := decoded.String()
		if point == versions || strings.HasPrefix(point, versions+string(filepath.Separator)) {
			return errors.New("nested or bind-mounted version entries are not installer-owned")
		}
	}
	return nil
}
