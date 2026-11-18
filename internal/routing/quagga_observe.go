package routing

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// QuaggaObservationOptions binds one local Unix VTY and its expected BGP
// instance. RouterID is the BGP router identifier, not an application ID.
type QuaggaObservationOptions struct {
	Socket    string
	RouterID  string
	RouterASN uint32
}

type QuaggaObservedSession struct {
	NeighborAddress  string
	NeighborASN      uint32
	State            string
	ReceivedPrefixes uint64
	PrefixesKnown    bool
}

const quaggaObservationLimit = 4 << 20
const quaggaObserveIPv4 = "show ip bgp view MOEDOVE_QUAGGA summary"
const quaggaObserveIPv6 = "show bgp view MOEDOVE_QUAGGA ipv6 unicast summary"

// ObserveQuaggaSessions returns only the explicitly selected peer addresses.
// Missing peers are absent from the result. Each populated family summary must
// independently match the expected router identity. An empty family can be
// attributed only if the same daemon supplied identity in the other family.
func ObserveQuaggaSessions(ctx context.Context, opts QuaggaObservationOptions, addresses []string) (map[string]QuaggaObservedSession, error) {
	localID, err := quaggaObservationAddress(opts.RouterID)
	if err != nil || !localID.Is4() || !validASN(opts.RouterASN) {
		return nil, errors.New("explicit Quagga IPv4 router identifier and a valid local ASN required")
	}
	requested, families, err := quaggaObservationTargets(addresses)
	if err != nil {
		return nil, err
	}
	result := make(map[string]QuaggaObservedSession, len(requested))
	if len(requested) == 0 {
		return result, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	client, err := openQuaggaObservation(ctx, opts.Socket)
	if err != nil {
		return nil, err
	}
	defer client.conn.Close()
	if err := client.version(ctx); err != nil {
		return nil, err
	}
	identity := false
	observed := [2]bool{}
	commands := []string{quaggaObserveIPv4, quaggaObserveIPv6}
	read := func(family int) error {
		observed[family] = true
		raw, err := client.read(ctx, commands[family], quaggaObservationLimit)
		if err != nil {
			return err
		}
		defer clear(raw)
		sessions, found, err := parseQuaggaSummary(raw, localID, opts.RouterASN, family == 1)
		if err != nil {
			return err
		}
		identity = identity || found
		for address, session := range sessions {
			if requested[address] {
				result[session.NeighborAddress] = session
			}
		}
		return nil
	}
	for family, needed := range families {
		if needed {
			if err := read(family); err != nil {
				return nil, err
			}
		}
	}
	if !identity {
		for family, alreadyRead := range observed {
			if !alreadyRead {
				if err := read(family); err != nil {
					return nil, err
				}
			}
		}
		if !identity {
			return nil, errors.New("native Quagga summaries did not report the expected router identity")
		}
	}
	if err = client.version(ctx); err != nil {
		return nil, err
	}
	if err = client.verifyEndpoint(); err != nil {
		return nil, err
	}
	return result, nil
}

func quaggaObservationAddress(raw string) (netip.Addr, error) {
	address, err := netip.ParseAddr(raw)
	if err != nil || address.Zone() != "" || address.Is4In6() || address.IsUnspecified() || address.IsMulticast() {
		return netip.Addr{}, errors.New("invalid unicast address in Quagga observation")
	}
	return address, nil
}

func quaggaObservationTargets(addresses []string) (map[netip.Addr]bool, [2]bool, error) {
	var families [2]bool
	if len(addresses) > 2048 {
		return nil, families, errors.New("Quagga observations require no more than 2048 explicit peers")
	}
	selected := make(map[netip.Addr]bool, len(addresses))
	for _, raw := range addresses {
		address, err := quaggaObservationAddress(raw)
		if err != nil || selected[address] {
			return nil, families, errors.New("invalid or duplicate Quagga observation peer")
		}
		selected[address] = true
		if address.Is4() {
			families[0] = true
		} else {
			families[1] = true
		}
	}
	return selected, families, nil
}

type quaggaObservation struct {
	socket     string
	dir        os.FileInfo
	endpoint   *syscall.Stat_t
	pid        int
	generation string
	conn       *net.UnixConn
}

func privateQuaggaObservationDir(path string) (os.FileInfo, error) {
	if !safePath.MatchString(path) || filepath.Clean(path) != path || filepath.Base(path) != "bgpd.vty" || len(path) >= len(syscall.RawSockaddrUnix{}.Path) {
		return nil, errors.New("Quagga observer needs an explicit absolute bgpd.vty path")
	}
	dir := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || dir != resolved {
		return nil, errors.New("Quagga observer requires the actual private VTY directory without symlinks")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return nil, errors.New("Quagga VTY directory must be private and owned by the observer UID")
	}
	return info, nil
}

func openQuaggaObservation(ctx context.Context, path string) (*quaggaObservation, error) {
	dir, err := privateQuaggaObservationDir(path)
	if err != nil {
		return nil, err
	}
	endpoint, err := quaggaSocketIdentity(path)
	if err != nil {
		return nil, errors.New("private Quagga VTY is unavailable")
	}
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, errors.New("cannot connect to the fixed private Quagga VTY")
	}
	client := &quaggaObservation{socket: path, dir: dir, endpoint: endpoint, conn: conn.(*net.UnixConn)}
	fd, err := client.conn.SyscallConn()
	if err != nil {
		client.conn.Close()
		return nil, errors.New("cannot inspect the local Quagga VTY credentials")
	}
	var credentials *syscall.Ucred
	var inspect error
	err = fd.Control(func(raw uintptr) {
		credentials, inspect = syscall.GetsockoptUcred(int(raw), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil || inspect != nil || credentials == nil || credentials.Uid != uint32(os.Geteuid()) || credentials.Pid <= 0 {
		client.conn.Close()
		return nil, errors.New("Quagga VTY did not authenticate a same-UID local process")
	}
	client.pid = int(credentials.Pid)
	client.generation, _, err = quaggaGeneration(client.pid)
	if err != nil || client.verifyEndpoint() != nil {
		client.conn.Close()
		return nil, errors.New("Quagga VTY identity could not be bound")
	}
	return client, nil
}

func (c *quaggaObservation) verifyEndpoint() error {
	dir, err := privateQuaggaObservationDir(c.socket)
	actual, socketErr := quaggaSocketIdentity(c.socket)
	generation, state, procErr := quaggaGeneration(c.pid)
	if err != nil || socketErr != nil || procErr != nil || !os.SameFile(c.dir, dir) || c.endpoint.Dev != actual.Dev || c.endpoint.Ino != actual.Ino || generation != c.generation || state == "Z" {
		return errors.New("Quagga VTY or its local process changed during observation")
	}
	return nil
}

func (c *quaggaObservation) read(ctx context.Context, command string, limit int) ([]byte, error) {
	if err := c.verifyEndpoint(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := quaggaSetVTYDeadline(ctx, c.conn); err != nil {
		return nil, errors.New("cannot bound the local Quagga VTY request")
	}
	if n, err := io.WriteString(c.conn, command+"\x00"); err != nil || n != len(command)+1 {
		return nil, errors.New("cannot send the fixed read-only Quagga VTY query")
	}
	raw, err := quaggaVTYReply(c.conn, limit)
	if err != nil {
		return nil, err
	}
	if err := c.verifyEndpoint(); err != nil {
		clear(raw)
		return nil, err
	}
	return raw, nil
}

func (c *quaggaObservation) version(ctx context.Context) error {
	data, err := c.read(ctx, "show version", 4096)
	if err != nil {
		return err
	}
	defer clear(data)
	line, _, _ := strings.Cut(string(data), "\n")
	if !strings.HasPrefix(line, "Quagga 1.2.4 (") || !strings.HasSuffix(line, ").") || !strings.Contains(string(data), "Copyright 1996-2005 Kunihiro Ishiguro, et al.") {
		return errors.New("local VTY is not the supported Quagga 1.2.4 response")
	}
	return nil
}
