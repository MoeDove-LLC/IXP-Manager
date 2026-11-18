//go:build !linux

package as112ops

import "errors"

func PinBinary(string) (BinaryPin, error) {
	return BinaryPin{}, errors.New("AS112 process ownership requires Linux")
}
func NewNativeRuntime() Runtime { return nil }
func CaptureTarget() (TargetPin, error) {
	return TargetPin{}, errors.New("AS112 target pin requires Linux")
}
