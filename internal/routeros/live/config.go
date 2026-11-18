package live

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/moedove/ixp-manager/internal/routeros"
)

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
var deviceIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,127}$`)
var systemIdentifier = regexp.MustCompile(`^[A-Za-z0-9+/]{1,128}$`)
var nativeID = regexp.MustCompile(`^\*[A-Fa-f0-9]{1,16}$`)

func validDeviceIdentifier(source routeros.IdentitySource, value string) bool {
	if source == routeros.LicenseSystemID {
		return systemIdentifier.MatchString(value)
	}
	return (source == routeros.LicenseSoftwareID || source == routeros.RouterBoardSerial) && deviceIdentifier.MatchString(value)
}

type preparedConfig struct {
	config          Config
	target          routeros.Target
	roots           *x509.CertPool
	knownHostLine   []byte
	knownHostDigest string
	identity        []byte
	password        []byte
}

func readFixedFile(path string, limit int64, private bool) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return nil, fail("invalid-private-file-path")
	}
	initial, e := os.Lstat(path)
	if e != nil || !trustedFile(initial, limit, private) {
		return nil, fail("private-file-permissions-or-size")
	}
	// Every directory is resolved relative to its already-open predecessor.
	// A concurrently replaced component cannot redirect the final open.
	fd, e := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if e != nil {
		return nil, fail("private-file-unreadable")
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, part := range parts[:len(parts)-1] {
		next, err := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		syscall.Close(fd)
		if err != nil {
			return nil, fail("private-file-parent-invalid")
		}
		fd = next
	}
	leaf, e := syscall.Openat(fd, parts[len(parts)-1], syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	syscall.Close(fd)
	if e != nil {
		return nil, fail("private-file-unreadable")
	}
	file := os.NewFile(uintptr(leaf), "private-routeros-input")
	defer file.Close()
	before, e := file.Stat()
	if e != nil || !trustedFile(before, limit, private) || !os.SameFile(initial, before) {
		return nil, fail("private-file-invalid")
	}
	raw, e := io.ReadAll(io.LimitReader(file, limit+1))
	after, statErr := file.Stat()
	if e != nil || statErr != nil || !trustedFile(after, limit, private) || !os.SameFile(before, after) || len(raw) == 0 || int64(len(raw)) != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || before.Sys().(*syscall.Stat_t).Ctim != after.Sys().(*syscall.Stat_t).Ctim {
		clear(raw)
		return nil, fail("private-file-read-not-stable")
	}
	return raw, nil
}
func trustedFile(info os.FileInfo, limit int64, private bool) bool {
	if info == nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit || info.Mode().Perm()&022 != 0 || private && info.Mode().Perm()&077 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return false
	}
	return stat.Uid == uint32(os.Geteuid()) || !private && stat.Uid == 0
}
func LoadConfig(path string) (Config, error) {
	var cfg Config
	raw, e := readFixedFile(path, 64<<10, true)
	if e != nil {
		return cfg, e
	}
	defer clear(raw)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF {
		return cfg, fail("invalid-private-config")
	}
	if _, e = validateConfig(cfg, false); e != nil {
		return Config{}, e
	}
	return cfg, nil
}
func canonicalHostTarget(host string, port int) string {
	if port == 22 {
		return host
	}
	return "[" + host + "]:" + strconv.Itoa(port)
}
func sshKeyType(raw []byte) (string, bool) {
	if len(raw) < 4 {
		return "", false
	}
	n := binary.BigEndian.Uint32(raw[:4])
	if n == 0 || n > 128 || int(n) > len(raw)-4 {
		return "", false
	}
	return string(raw[4 : 4+int(n)]), true
}

// A target may have exactly one trusted literal entry. Wildcards, hashed hosts,
// markers and multi-host entries are rejected rather than broadening its pin.
func canonicalKnownHost(raw []byte, host string, port int, expected string) ([]byte, error) {
	if len(raw) > 1<<20 || !digestPattern.MatchString(expected) {
		return nil, fail("invalid-ssh-host-pin")
	}
	target := canonicalHostTarget(host, port)
	matches := 0
	canonical := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.HasPrefix(fields[0], "@") || strings.ContainsAny(fields[0], "*?!|,") {
			return nil, fail("ambiguous-known-hosts-entry")
		}
		if fields[0] != target {
			continue
		}
		matches++
		if matches != 1 {
			return nil, fail("multiple-target-host-keys")
		}
		switch fields[1] {
		case "ssh-ed25519", "ssh-rsa", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521":
		default:
			return nil, fail("unsupported-ssh-host-key")
		}
		key, e := base64.StdEncoding.DecodeString(fields[2])
		if e != nil || len(key) < 16 || len(key) > 16384 {
			return nil, fail("invalid-ssh-host-key")
		}
		kind, ok := sshKeyType(key)
		if !ok || kind != fields[1] {
			return nil, fail("ssh-host-key-type-mismatch")
		}
		hash := sha256.Sum256(key)
		wanted, _ := hex.DecodeString(expected)
		if subtle.ConstantTimeCompare(hash[:], wanted) != 1 {
			return nil, fail("ssh-host-key-pin-mismatch")
		}
		canonical = target + " " + fields[1] + " " + base64.StdEncoding.EncodeToString(key) + "\n"
	}
	if matches != 1 {
		return nil, fail("target-host-key-missing")
	}
	return []byte(canonical), nil
}
func validateConfig(cfg Config, loadSecrets bool) (preparedConfig, error) {
	p := preparedConfig{config: cfg}
	if cfg.SchemaVersion != 1 || !identifierPattern.MatchString(cfg.RouterID) || cfg.Version != "7.24.6" || !validDeviceIdentifier(cfg.IdentitySource, cfg.DeviceID) || !digestPattern.MatchString(cfg.API.SPKISHA256) || !digestPattern.MatchString(cfg.SSH.HostKeySHA256) {
		return p, fail("unsupported-or-invalid-target-config")
	}
	switch cfg.IdentitySource {
	case routeros.LicenseSystemID, routeros.LicenseSoftwareID, routeros.RouterBoardSerial:
	default:
		return p, fail("invalid-identity-source")
	}
	host, port, e := net.SplitHostPort(cfg.API.Address)
	apiPort, pe := strconv.Atoi(port)
	if e != nil || pe != nil || host == "" || apiPort < 1 || apiPort > 65535 || cfg.API.ServerName == "" || cfg.API.Username == "" || len(cfg.API.Username) > 128 || strings.ContainsAny(cfg.API.Address+cfg.API.ServerName+cfg.API.Username, "\x00\r\n") {
		return p, fail("invalid-api-target")
	}
	if cfg.SSH.Host == "" || cfg.SSH.Username == "" || cfg.SSH.Port < 1 || cfg.SSH.Port > 65535 || strings.ContainsAny(cfg.SSH.Host+cfg.SSH.Username, "\x00\r\n \t") {
		return p, fail("invalid-ssh-target")
	}
	if cfg.ObservationTimeoutSeconds == 0 {
		p.config.ObservationTimeoutSeconds = 60
	}
	if cfg.OperationTimeoutSeconds == 0 {
		p.config.OperationTimeoutSeconds = 10
	}
	if cfg.SessionLifetimeSeconds == 0 {
		p.config.SessionLifetimeSeconds = 600
	}
	if cfg.MaxObjects == 0 {
		p.config.MaxObjects = 2048
	}
	if cfg.MaxGetterCalls == 0 {
		p.config.MaxGetterCalls = 8192
	}
	if p.config.ObservationTimeoutSeconds < 1 || p.config.ObservationTimeoutSeconds > 180 || p.config.OperationTimeoutSeconds < 1 || p.config.OperationTimeoutSeconds > 60 || p.config.SessionLifetimeSeconds < p.config.OperationTimeoutSeconds || p.config.SessionLifetimeSeconds > 600 || p.config.MaxObjects < 1 || p.config.MaxObjects > 20000 || p.config.MaxGetterCalls < 1 || p.config.MaxGetterCalls > 100000 {
		return p, fail("invalid-live-bounds")
	}
	if _, e := cfg.Supervisor.ControllerConfig(); e != nil {
		return p, e
	}
	ca, e := readFixedFile(cfg.API.CAFile, 1<<20, false)
	if e != nil {
		return p, e
	}
	p.roots = x509.NewCertPool()
	if !p.roots.AppendCertsFromPEM(ca) {
		return p, fail("invalid-api-ca")
	}
	caHash := sha256.Sum256(ca)
	clear(ca)
	hosts, e := readFixedFile(cfg.SSH.KnownHostsFile, 1<<20, false)
	if e != nil {
		return p, e
	}
	p.knownHostLine, e = canonicalKnownHost(hosts, cfg.SSH.Host, cfg.SSH.Port, cfg.SSH.HostKeySHA256)
	clear(hosts)
	if e != nil {
		return p, e
	}
	kh := sha256.Sum256(p.knownHostLine)
	p.knownHostDigest = hex.EncodeToString(kh[:])
	if loadSecrets {
		p.password, e = readFixedFile(cfg.API.PasswordFile, 1024, true)
		if e != nil {
			return p, e
		}
		p.identity, e = readFixedFile(cfg.SSH.IdentityFile, 64<<10, true)
		if e != nil {
			clear(p.password)
			return p, e
		}
	} else {
		for _, file := range []struct {
			path string
			max  int64
		}{{cfg.API.PasswordFile, 1024}, {cfg.SSH.IdentityFile, 64 << 10}} {
			raw, e := readFixedFile(file.path, file.max, true)
			if e != nil {
				return p, e
			}
			clear(raw)
		}
	}
	identity := struct {
		Domain, APIAddress, APIServerName, APIUser, APISPKI, CAHash, SSHHost, SSHUser, SSHHostPin, KnownHostLineHash string
		SSHPort                                                                                                      int
		IdentitySource                                                                                               routeros.IdentitySource
	}{"routeros-live-transport-v1", net.JoinHostPort(host, strconv.Itoa(apiPort)), cfg.API.ServerName, cfg.API.Username, cfg.API.SPKISHA256, hex.EncodeToString(caHash[:]), cfg.SSH.Host, cfg.SSH.Username, cfg.SSH.HostKeySHA256, p.knownHostDigest, cfg.SSH.Port, cfg.IdentitySource}
	data, e := json.Marshal(identity)
	if e != nil {
		return p, errors.New("invalid transport binding")
	}
	digest := sha256.Sum256(data)
	p.target = routeros.Target{RouterID: cfg.RouterID, Version: cfg.Version, DeviceID: cfg.DeviceID, TransportIdentitySHA256: hex.EncodeToString(digest[:])}
	return p, nil
}

// Target derives the binding from verified local pin files, never from a JSON
// assertion of authenticated/native status.
func Target(cfg Config) (routeros.Target, error) {
	p, e := validateConfig(cfg, false)
	return p.target, e
}
