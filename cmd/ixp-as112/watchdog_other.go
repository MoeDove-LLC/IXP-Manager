//go:build !linux

package main

import "errors"

func sendSystemdNotification(string, string) error {
	return errors.New("service-manager notification requires Linux")
}
