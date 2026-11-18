//go:build !linux

package as112ops

func captureHostIdentity(string) string { return "" }
