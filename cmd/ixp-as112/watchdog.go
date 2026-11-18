package main

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The service manager owns the watchdog. This client only sends fixed protocol
// messages after the same goroutine has completed a controller Tick. A blocked
// Tick cannot be hidden by a separate heartbeat goroutine.
type systemdNotifier struct {
	socket   string
	watchdog time.Duration
	ready    bool
}

const reviewedWatchdogSeconds = 180

func newSystemdNotifier(getenv func(string) string, pid int) (*systemdNotifier, error) {
	n := &systemdNotifier{socket: getenv("NOTIFY_SOCKET")}
	if value := getenv("WATCHDOG_PID"); value != "" {
		watched, e := strconv.ParseUint(value, 10, 31)
		if e != nil || watched == 0 || int(watched) != pid {
			return nil, errors.New("watchdog environment does not name this supervisor process")
		}
	}
	if value := getenv("WATCHDOG_USEC"); value != "" {
		micros, e := strconv.ParseUint(value, 10, 64)
		if e != nil || micros > uint64((1<<63-1)/int64(time.Microsecond)) {
			return nil, errors.New("invalid service-manager watchdog timeout")
		}
		n.watchdog = time.Duration(micros) * time.Microsecond
	}
	if n.socket == "" {
		if n.watchdog > 0 {
			return nil, errors.New("watchdog timeout supplied without a notification socket")
		}
		return n, nil
	}
	if len(n.socket) > 107 || strings.ContainsAny(n.socket, "\x00\r\n") || n.socket[0] != '/' && n.socket[0] != '@' || n.socket == "@" || n.socket[0] == '/' && filepath.Clean(n.socket) != n.socket {
		return nil, errors.New("invalid local service-manager notification socket")
	}
	return n, nil
}
func (n *systemdNotifier) progress() error {
	if n.socket == "" {
		return nil
	}
	payload := ""
	if !n.ready {
		payload = "READY=1"
	}
	if n.watchdog > 0 {
		if payload != "" {
			payload += "\n"
		}
		payload += "WATCHDOG=1"
	}
	if payload == "" {
		return nil
	}
	if e := sendSystemdNotification(n.socket, payload); e != nil {
		return e
	}
	n.ready = true
	return nil
}
func (n *systemdNotifier) stopped() error {
	if n.socket == "" || !n.ready {
		return nil
	}
	return sendSystemdNotification(n.socket, "STOPPING=1")
}
