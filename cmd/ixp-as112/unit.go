package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/moedove/ixp-manager/internal/as112ops"
)

var unitPath = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)

func writeUnit(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("unit", flag.ContinueOnError)
	fs.SetOutput(output)
	planFile := fs.String("plan", "", "private plan to bind the reviewed unit")
	binary := fs.String("binary", "", "absolute installed ixp-as112 native executable")
	destination := fs.String("output", "", "NEW review-only unit file; no automatic installation or enabling")
	recover := fs.Bool("recover-ack", false, "include the explicit same-plan recovery grant in ExecStart")
	if done, err := parseFlags(fs, args); done || err != nil {
		return err
	}
	p, err := readPlan(*planFile)
	if err != nil {
		return err
	}
	path, err := cleanPath(*planFile)
	if err != nil || !unitPath.MatchString(path) || !unitPath.MatchString(p.Root) || !unitPath.MatchString(*binary) || filepath.Clean(*binary) != *binary || *destination == "" {
		return errors.New("unit paths must be absolute simple paths without spaces, escapes or systemd specifiers; --output is required")
	}
	pin, err := as112ops.PinBinary(*binary)
	if err != nil {
		return errors.New("unit supervisor executable is not a trusted native ELF")
	}
	text, err := unitContent(p, path, pin, *recover, os.Geteuid())
	if err != nil {
		return err
	}
	if err = writeNewPrivate(*destination, []byte(text)); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		ReviewOnly bool   `json:"reviewOnly"`
		Installed  bool   `json:"installed"`
		Enabled    bool   `json:"enabled"`
		Started    bool   `json:"started"`
		Plan       string `json:"planChecksum"`
	}{true, false, false, false, p.Checksum})
}

func unitContent(p as112ops.Plan, path string, pin as112ops.BinaryPin, recover bool, uid int) (string, error) {
	for _, value := range []string{path, p.Root, pin.Path} {
		if !unitPath.MatchString(value) || filepath.Clean(value) != value {
			return "", errors.New("unit path cannot contain escapes or systemd specifiers")
		}
	}
	for _, file := range []string{path, p.Root, pin.Path, p.Tools.Router.Path, p.Tools.PDNS.Path, p.Tools.NamedCheckzone.Path} {
		for _, hidden := range []string{"/root", "/home", "/run/user", "/tmp", "/var/tmp"} {
			if file == hidden || strings.HasPrefix(file, hidden+"/") {
				return "", errors.New("review-only unit uses ProtectHome and PrivateTmp; move plan, root and tools to a suitable /var/lib or system layout, or use foreground run")
			}
		}
	}
	grant := ""
	if recover {
		grant = " --recover-ack"
	}
	text := fmt.Sprintf(`# Review-only: not installed, enabled or started by this command.
# Reviewed plan SHA256: %s
# Supervisor executable SHA256 at generation: %s
# This comment is not an ExecStart hash check; protect the installed supervisor.
# The plan or approved execution binding pins boot/netns. Do not add PrivateNetwork=yes.
# Watchdog renewal follows a completed controller Tick, including safe withdrawal.
# 180s allows the 60s DNS probe, <=30s cadence and bounded owned-process stops.
# Requires a working systemd service manager; a unit file alone is not protection.
# Review account, port privileges, preconfigured addresses and host return paths.
# FRR's distribution runtime directories must already exist; no shared instance
# may be taken over. This file is a template, not an installation success claim.
[Unit]
Description=MoeDove dedicated AS112 DNS and BGP owner
After=network-online.target
Wants=network-online.target
ConditionPathExists=%s
StartLimitIntervalSec=60
StartLimitBurst=3

[Service]
Type=notify
NotifyAccess=main
WatchdogSec=%d
WatchdogSignal=SIGKILL
TimeoutStartSec=%d
User=%d
WorkingDirectory=%s
ExecStart=%s run --plan %s%s
Restart=on-failure
RestartSec=5
KillMode=control-group
KillSignal=SIGTERM
SendSIGKILL=yes
FinalKillSignal=SIGKILL
TimeoutStopSec=30
LimitCORE=0
UMask=0077
NoNewPrivileges=yes
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=%s
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK

[Install]
WantedBy=multi-user.target
`, p.Checksum, pin.SHA256, path, reviewedWatchdogSeconds, reviewedWatchdogSeconds, uid, p.Root, pin.Path, path, grant, p.Root)
	return text, nil
}
