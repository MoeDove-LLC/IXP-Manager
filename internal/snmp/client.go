package snmp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/gosnmp/gosnmp"
)

const MaxInterfaces = 2048
const MaxEntities = 4096
const MaxSensors = 4096
const MaxFDB = 16384
const MaxSnapshotBytes = 8 << 20

var resourceID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func ValidateConfig(c Config) error {
	if !resourceID.MatchString(c.SwitchID) {
		return errors.New("a valid explicit switchId is required")
	}
	target, err := netip.ParseAddr(c.Target)
	if err != nil || (!target.IsGlobalUnicast() && !target.IsLoopback()) || target.Is4In6() || target.Zone() != "" {
		return errors.New("SNMP target must be an explicit unicast IP address")
	}
	if c.Endpoint != "" {
		u, e := url.Parse(c.Endpoint)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSuffix(u.Path, "/") != "/api/devices" {
			return errors.New("endpoint must be an HTTPS /api/devices URL without credentials/query/fragment")
		}
	}
	if c.TimeoutSeconds < 0 || c.TimeoutSeconds > 5 || c.Retries < 0 || c.Retries > 2 {
		return errors.New("SNMP timeout must be1..5seconds(default3), retries0..2")
	}
	if len(c.ContextName) > 64 || strings.ContainsAny(c.ContextName, "\x00\r\n") {
		return errors.New("invalid SNMP context")
	}
	switch c.Version {
	case "3":
		if c.Username == "" || len(c.Username) > 64 || strings.ContainsAny(c.Username, "\x00\r\n") || len(c.AuthPassphrase) < 8 || len(c.AuthPassphrase) > 128 || len(c.PrivacyPassphrase) < 8 || len(c.PrivacyPassphrase) > 128 {
			return errors.New("SNMPv3 requires local username and8..128byte authentication/privacy secrets")
		}
		if _, ok := authProtocols[c.AuthProtocol]; !ok {
			return errors.New("unsupported SNMPv3 authentication protocol (SHA/SHA224/SHA256/SHA384/SHA512)")
		}
		if _, ok := privacyProtocols[c.PrivacyProtocol]; !ok {
			return errors.New("unsupported SNMPv3 AES privacy protocol")
		}
		if c.Community != "" || c.AllowV2c {
			return errors.New("SNMPv3 configuration cannot carry community mode settings")
		}
	case "2c":
		if c.ContextName != "" {
			return errors.New("SNMP contextName is supported only in v3; v2c context must be configured as a separate explicit device")
		}
		if !c.AllowV2c || c.Community == "" || len(c.Community) > 128 {
			return errors.New("SNMPv2c requires explicit allowV2c and a private community")
		}
		if c.Username != "" || c.AuthPassphrase != "" || c.PrivacyPassphrase != "" {
			return errors.New("SNMPv2c cannot carry v3 secrets")
		}
	default:
		return errors.New("SNMP version must be3(authPriv) or explicitly opted-in2c")
	}
	seenPorts := map[string]bool{}
	seenIndices := map[uint32]bool{}
	if len(c.Mappings) > MaxInterfaces {
		return errors.New("too many port mappings")
	}
	for _, mapping := range c.Mappings {
		if !resourceID.MatchString(mapping.PortID) || mapping.IfIndex < 1 || mapping.IfIndex > 2147483647 || seenPorts[mapping.PortID] || seenIndices[mapping.IfIndex] {
			return errors.New("port mappings must have unique valid portId/ifIndex")
		}
		seenPorts[mapping.PortID] = true
		seenIndices[mapping.IfIndex] = true
	}
	return nil
}

var authProtocols = map[string]gosnmp.SnmpV3AuthProtocol{"": gosnmp.SHA256, "SHA": gosnmp.SHA, "SHA224": gosnmp.SHA224, "SHA256": gosnmp.SHA256, "SHA384": gosnmp.SHA384, "SHA512": gosnmp.SHA512}
var privacyProtocols = map[string]gosnmp.SnmpV3PrivProtocol{"": gosnmp.AES, "AES": gosnmp.AES, "AES192": gosnmp.AES192, "AES256": gosnmp.AES256, "AES192C": gosnmp.AES192C, "AES256C": gosnmp.AES256C}

func ReadPrivateConfig(path string) (Config, error) {
	var c Config
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return c, errors.New("private SNMP configuration cannot be opened")
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return c, errors.New("SNMP configuration must be a private regular file (0600), at most1MiB")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&c); err != nil {
		return Config{}, errors.New("invalid private SNMP configuration JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Config{}, errors.New("one private configuration document required")
	}
	if err = ValidateConfig(c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Transport exposes no SNMP SET operation. Implementations and tests can only
// retrieve fixed system objects or walk the collector's approved MIB tables.
type Transport interface {
	Get([]string) ([]gosnmp.SnmpPDU, error)
	Walk(string, func(gosnmp.SnmpPDU) error) error
	EngineEpoch() string
	Close() error
}
type realTransport struct{ client *gosnmp.GoSNMP }

func Open(ctx context.Context, c Config) (Transport, error) {
	if err := ValidateConfig(c); err != nil {
		return nil, err
	}
	timeout := c.TimeoutSeconds
	if timeout == 0 {
		timeout = 3
	}
	port := c.Port
	if port == 0 {
		port = 161
	}
	client := &gosnmp.GoSNMP{Target: c.Target, Port: port, Transport: "udp", Timeout: time.Duration(timeout) * time.Second, Retries: c.Retries, ExponentialTimeout: false, Context: ctx, MaxOids: 16, MaxRepetitions: 20, Logger: gosnmp.NewLogger(log.New(io.Discard, "", 0)), ContextName: c.ContextName}
	if c.Version == "3" {
		client.Version = gosnmp.Version3
		client.SecurityModel = gosnmp.UserSecurityModel
		client.MsgFlags = gosnmp.AuthPriv
		client.SecurityParameters = &gosnmp.UsmSecurityParameters{UserName: c.Username, AuthenticationProtocol: authProtocols[c.AuthProtocol], AuthenticationPassphrase: c.AuthPassphrase, PrivacyProtocol: privacyProtocols[c.PrivacyProtocol], PrivacyPassphrase: c.PrivacyPassphrase}
	} else {
		client.Version = gosnmp.Version2c
		client.Community = c.Community
	}
	if err := client.Connect(); err != nil {
		return nil, errors.New("SNMP connection failed; check local credentials and target reachability")
	}
	return &realTransport{client}, nil
}
func (t *realTransport) Get(oids []string) ([]gosnmp.SnmpPDU, error) {
	for _, oid := range oids {
		if !systemOIDs[oid] {
			return nil, errors.New("OID is outside the fixed system read allowlist")
		}
	}
	packet, err := t.client.Get(oids)
	if err != nil {
		return nil, safeTransportError(err)
	}
	if packet != nil && packet.Error == gosnmp.AuthorizationError {
		return nil, errors.New("SNMP authentication refused")
	}
	if packet == nil || packet.Error != gosnmp.NoError {
		return nil, errors.New("SNMP system read refused")
	}
	return packet.Variables, nil
}
func (t *realTransport) Walk(root string, fn func(gosnmp.SnmpPDU) error) error {
	if !walkRoots[root] {
		return errors.New("OID is outside the fixed MIB walk allowlist")
	}
	return t.client.BulkWalk(root, fn)
}
func (t *realTransport) EngineEpoch() string {
	security, ok := t.client.SecurityParameters.(*gosnmp.UsmSecurityParameters)
	if !ok {
		return ""
	}
	b, _ := json.Marshal(struct {
		ID      string
		Boots   uint32
		Context string
	}{hex.EncodeToString([]byte(security.AuthoritativeEngineID)), security.AuthoritativeEngineBoots, t.client.ContextName})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (t *realTransport) Close() error {
	if t.client.Conn != nil {
		return t.client.Conn.Close()
	}
	return nil
}

func safeTransportError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gosnmp.ErrUnknownUsername) || errors.Is(err, gosnmp.ErrWrongDigest) || errors.Is(err, gosnmp.ErrDecryption) || errors.Is(err, gosnmp.ErrUnknownSecurityLevel) || strings.Contains(strings.ToLower(err.Error()), "authentication") {
		return errors.New("SNMP authentication refused")
	}
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || (errors.As(err, &networkError) && networkError.Timeout()) || strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return errors.New("SNMP request timed out")
	}
	return errors.New("SNMP transport request failed; details withheld")
}

func (t *realTransport) EngineIdentity() string {
	security, ok := t.client.SecurityParameters.(*gosnmp.UsmSecurityParameters)
	if !ok || security.AuthoritativeEngineID == "" {
		return ""
	}
	data, _ := json.Marshal(struct{ ID, Context string }{hex.EncodeToString([]byte(security.AuthoritativeEngineID)), t.client.ContextName})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
