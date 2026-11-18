//go:build linux

package as112ops

import (
	"io"
	"os"
	"strings"
	"syscall"
)

func machineIdentity(raw []byte) string {
	value := strings.TrimSpace(string(raw))
	if len(value) != 32 || value == strings.Repeat("0", 32) {
		return ""
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return ""
		}
	}
	return value
}
func captureHostIdentity(instance string) string {
	f, e := os.OpenFile("/etc/machine-id", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return ""
	}
	defer f.Close()
	s, e := f.Stat()
	if e != nil {
		return ""
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	if !ok || !s.Mode().IsRegular() || s.Mode().Perm()&0022 != 0 || st.Uid != 0 || s.Size() > 4096 {
		return ""
	}
	raw, e := io.ReadAll(io.LimitReader(f, 4097))
	if e != nil {
		return ""
	}
	value := machineIdentity(raw)
	if value == "" {
		return ""
	}
	return scopeHostIdentity(instance, value)
}
