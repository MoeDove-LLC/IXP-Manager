//go:build linux

package main

import (
	"errors"
	"net"
	"time"
)

func sendSystemdNotification(socket, payload string) error {
	const budget = 250 * time.Millisecond
	connection, e := net.DialTimeout("unixgram", socket, budget)
	if e != nil {
		return errors.New("service-manager notification unavailable")
	}
	defer connection.Close()
	if e = connection.SetWriteDeadline(time.Now().Add(budget)); e != nil {
		return errors.New("service-manager notification deadline unavailable")
	}
	// systemd enables SO_PASSCRED: the kernel attaches this actual sender's PID,
	// UID and GID. The client never supplies another PID or sends configuration,
	// environment values, application secrets or an arbitrary STATUS string.
	written, e := connection.Write([]byte(payload))
	if e != nil || written != len(payload) {
		return errors.New("service-manager notification delivery failed")
	}
	return nil
}
