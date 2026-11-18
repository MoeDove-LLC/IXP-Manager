package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/routing"
	"github.com/moedove/ixp-manager/internal/traffic"
)

type CollectorLookingGlass struct {
	AllowTable bool `json:"allowTable,omitempty"`
}

type CollectorConfig struct {
	Bgpctl       string                `json:"bgpctl,omitempty"`
	BgpSocket    string                `json:"bgpSocket,omitempty"`
	QuaggaSocket string                `json:"quaggaSocket,omitempty"`
	VtySocket    string                `json:"vtySocket,omitempty"`
	FRRView      string                `json:"frrView,omitempty"`
	LookingGlass CollectorLookingGlass `json:"lookingGlass,omitempty"`
	Endpoint     string                `json:"endpoint"`
	RouterID     string                `json:"routerId"`
	Platform     string                `json:"platform"`
	Birdc        string                `json:"birdc,omitempty"`
	BirdSocket   string                `json:"birdSocket,omitempty"`
	Vtysh        string                `json:"vtysh,omitempty"`
	Ports        map[string]string     `json:"ports"`
	Peers        map[string]string     `json:"peers"`
}

var interfaceName = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)

func ValidateCollector(c CollectorConfig) error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/api/monitoring/ingest" {
		return errors.New("endpoint must be an HTTPS /api/monitoring/ingest URL without credentials, query or fragment")
	}
	if c.RouterID == "" || len(c.RouterID) > 100 || len(c.Ports)+len(c.Peers) == 0 || len(c.Ports)+len(c.Peers) > 2048 {
		return errors.New("routerId and 1..2048 explicit port/peer mappings are required")
	}
	if c.Platform != "bird2" && c.Platform != "bird3" && c.Platform != "frr" && c.Platform != "openbgpd" && c.Platform != "quagga" && c.Platform != "none" {
		return errors.New("collector platform must be bird2, bird3, frr, openbgpd, quagga or none")
	}
	if c.Platform == "none" && len(c.Peers) > 0 {
		return errors.New("peer mappings require a routing platform")
	}
	if c.Platform == "quagga" && len(c.Peers) > 0 && (!filepath.IsAbs(c.QuaggaSocket) || filepath.Clean(c.QuaggaSocket) != c.QuaggaSocket) {
		return errors.New("Quagga session collection requires the explicit absolute private bgpd.vty path")
	}
	seen := map[string]bool{}
	for id, name := range c.Ports {
		if id == "" || len(id) > 100 || !interfaceName.MatchString(name) || name == "." || name == ".." || seen[name] {
			return errors.New("invalid or duplicate interface mapping")
		}
		seen[name] = true
	}
	peerAddresses := map[string]bool{}
	for id, selector := range c.Peers {
		if id == "" || len(id) > 100 || selector == "" || len(selector) > 100 || strings.ContainsAny(selector, "\x00\r\n") {
			return errors.New("invalid peer mapping")
		}
		if c.Platform == "quagga" {
			address, err := NormalizeNeighborAddress(selector)
			if err != nil || peerAddresses[address] {
				return errors.New("Quagga peer mappings require distinct neighbor IP addresses")
			}
			peerAddresses[address] = true
		}
	}
	if c.FRRView != "" && c.FRRView != "MOEDOVE" {
		return errors.New("only the fixed MOEDOVE FRR view can be selected")
	}
	for _, binary := range []string{c.Birdc, c.Vtysh, c.Bgpctl, c.BgpSocket, c.BirdSocket, c.VtySocket} {
		if binary != "" && !filepath.IsAbs(binary) {
			return errors.New("custom collector executable must use an absolute local path")
		}
	}
	return nil
}
func readCounter(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if _, err = counter(value, 64); err != nil {
		return "", err
	}
	return value, nil
}

// ReadLinuxPorts reads kernel-maintained counters. Explicit mappings prevent
// accidental enumeration or disclosure of every interface on the host.
func ReadLinuxPorts(root, bootPath string, mapping map[string]string) ([]PortSample, error) {
	if len(mapping) == 0 {
		return []PortSample{}, nil
	}
	boot, err := os.ReadFile(bootPath)
	if err != nil {
		return nil, err
	}
	bootID := strings.TrimSpace(string(boot))
	if bootID == "" || len(bootID) > 80 {
		return nil, errors.New("invalid Linux boot ID")
	}
	keys := make([]string, 0, len(mapping))
	for id := range mapping {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	out := []PortSample{}
	for _, id := range keys {
		name := mapping[id]
		if !interfaceName.MatchString(name) || name == "." || name == ".." {
			return nil, errors.New("invalid Linux interface name")
		}
		base := filepath.Join(root, name)
		in, err := readCounter(filepath.Join(base, "statistics", "rx_bytes"))
		if err != nil {
			return nil, fmt.Errorf("read mapped interface counters: %w", err)
		}
		outBytes, err := readCounter(filepath.Join(base, "statistics", "tx_bytes"))
		if err != nil {
			return nil, err
		}
		idx, err := readCounter(filepath.Join(base, "ifindex"))
		if err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(filepath.Join(base, "operstate"))
		if err != nil {
			return nil, err
		}
		status := strings.TrimSpace(string(raw))
		if status != "up" && status != "down" {
			status = "unknown"
		}
		point := PortSample{PortID: id, InOctets: in, OutOctets: outBytes, OperStatus: status, CounterBits: 64, CounterEpoch: bootID + ":" + idx}
		optionalPair := func(inName, outName string) (*traffic.CounterPair, error) {
			p := &traffic.CounterPair{Bits: 64}
			for _, entry := range []struct {
				name  string
				value *string
			}{{inName, &p.In}, {outName, &p.Out}} {
				if entry.name == "" {
					continue
				}
				value, e := readCounter(filepath.Join(base, "statistics", entry.name))
				if os.IsNotExist(e) {
					continue
				}
				if e != nil {
					return nil, e
				}
				*entry.value = value
			}
			if p.In == "" && p.Out == "" {
				return nil, nil
			}
			return p, nil
		}
		point.Errors, err = optionalPair("rx_errors", "tx_errors")
		if err != nil {
			return nil, err
		}
		point.Discards, err = optionalPair("rx_dropped", "tx_dropped")
		if err != nil {
			return nil, err
		}
		point.Multicast, err = optionalPair("multicast", "")
		if err != nil {
			return nil, err
		}
		// Linux rx_packets/tx_packets are totals, not unicast counters. No
		// unicast or broadcast split is invented from those totals.
		out = append(out, point)
	}
	return out, nil
}

type limitedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len()+n > 4<<20 {
		b.overflow = true
		remaining := (4 << 20) - b.Len()
		if remaining > 0 {
			b.Buffer.Write(p[:remaining])
		}
		return n, nil
	}
	return b.Buffer.Write(p)
}
func localCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	var output limitedBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("local routing status command failed: %w", err)
	}
	if output.overflow {
		return nil, errors.New("routing command output exceeded limit")
	}
	return output.Bytes(), nil
}
func Collect(ctx context.Context, c CollectorConfig) (Batch, error) {
	return collect(ctx, c, Bindings{})
}

// CollectWithBindings uses the router-bound management observation target as
// the expected local identity. Its actual identity is checked by the native
// observer before the same tuple can accompany the measured session samples.
func CollectWithBindings(ctx context.Context, c CollectorConfig, bindings Bindings) (Batch, error) {
	return collect(ctx, c, bindings)
}

func collect(ctx context.Context, c CollectorConfig, bindings Bindings) (Batch, error) {
	if err := ValidateCollector(c); err != nil {
		return Batch{}, err
	}
	ports, err := ReadLinuxPorts("/sys/class/net", "/proc/sys/kernel/random/boot_id", c.Ports)
	if err != nil {
		return Batch{}, err
	}
	observedAt := time.Now().UTC()
	sessions := []SessionSample{}
	var identity *RouterIdentity
	if len(c.Peers) > 0 {
		var observed map[string]ObservedSession
		switch c.Platform {
		case "bird2", "bird3":
			binary := c.Birdc
			if binary == "" {
				binary = "birdc"
			}
			args := []string{}
			if c.BirdSocket != "" {
				args = append(args, "-s", c.BirdSocket)
			}
			args = append(args, "show", "protocols", "all")
			raw, err := localCommand(ctx, binary, args...)
			if err != nil {
				return Batch{}, err
			}
			if c.Platform == "bird3" {
				observed, err = ParseBIRD3(raw)
			} else {
				observed, err = ParseBIRD2(raw)
			}
			if err != nil {
				return Batch{}, err
			}
		case "frr":
			binary := c.Vtysh
			if binary == "" {
				binary = "vtysh"
			}
			command := "show bgp summary json"
			if c.FRRView == "MOEDOVE" {
				command = "show bgp view MOEDOVE summary json"
			}
			args := []string{"-c", command}
			if c.VtySocket != "" {
				args = append([]string{"--vty_socket", c.VtySocket}, args...)
			}
			raw, err := localCommand(ctx, binary, args...)
			if err != nil {
				return Batch{}, err
			}
			observed, err = ParseFRRSummary(raw)
			if err != nil {
				return Batch{}, err
			}
		case "openbgpd":
			binary := c.Bgpctl
			if binary == "" {
				binary = "bgpctl"
			}
			socket := c.BgpSocket
			if socket == "" {
				socket = "/run/moedove-openbgpd/bgpd.sock"
			}
			raw, e := localCommand(ctx, binary, "-s", socket, "-j", "show", "neighbor")
			if e != nil {
				return Batch{}, e
			}
			observed, e = ParseOpenBGPDNeighbors(raw)
			if e != nil {
				return Batch{}, e
			}
		case "quagga":
			if bindings.RouterID != c.RouterID || bindings.RouterIdentity == nil || !bindings.RouterIdentity.validQuagga() {
				return Batch{}, errors.New("Quagga session observation requires a current router identity from the scoped collector bindings")
			}
			expected := *bindings.RouterIdentity
			addresses := make([]string, 0, len(c.Peers))
			for _, raw := range c.Peers {
				address, _ := NormalizeNeighborAddress(raw)
				addresses = append(addresses, address)
			}
			sort.Strings(addresses)
			native, e := routing.ObserveQuaggaSessions(ctx, routing.QuaggaObservationOptions{Socket: c.QuaggaSocket, RouterID: expected.RouterID, RouterASN: expected.ASN}, addresses)
			if e != nil {
				return Batch{}, e
			}
			observed = make(map[string]ObservedSession, len(native))
			for key, sample := range native {
				observed[key] = ObservedSession{NeighborAddress: sample.NeighborAddress, NeighborASN: sample.NeighborASN, State: sample.State,
					ReceivedPrefixes: sample.ReceivedPrefixes, PrefixesKnown: sample.PrefixesKnown}
			}
			identity = &expected
		}
		keys := make([]string, 0, len(c.Peers))
		for id := range c.Peers {
			keys = append(keys, id)
		}
		sort.Strings(keys)
		for _, id := range keys {
			selector := c.Peers[id]
			if c.Platform == "frr" || c.Platform == "openbgpd" || c.Platform == "quagga" {
				ip, err := netip.ParseAddr(selector)
				if err != nil {
					return Batch{}, errors.New("FRR/OpenBGPD/Quagga peer selectors must be IP addresses")
				}
				selector = ip.String()
			}
			value, ok := observed[selector]
			if !ok {
				value = ObservedSession{State: "unknown"}
			}
			var prefixes *uint64
			if value.PrefixesKnown {
				n := value.ReceivedPrefixes
				prefixes = &n
			}
			sessions = append(sessions, SessionSample{PeerID: id, State: value.State, ReceivedPrefixes: prefixes, NeighborAddress: value.NeighborAddress, NeighborASN: value.NeighborASN})
		}
	}
	if len(ports) == 0 {
		observedAt = time.Now().UTC()
	}
	return Batch{RouterID: c.RouterID, ObservedAt: observedAt, Ports: ports, Sessions: sessions, RouterIdentity: identity}, nil
}
func Upload(ctx context.Context, client *http.Client, endpoint, token string, b Batch) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
		return errors.New("HTTPS telemetry endpoint required")
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") || len(token) > 256 {
		return errors.New("invalid collector token")
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	// Copy the caller's transport but never follow redirects with credentials.
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	safe := *client
	safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := safe.Do(request)
	if err != nil {
		return errors.New("telemetry HTTPS upload failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("telemetry endpoint returned HTTP %d", response.StatusCode)
	}
	var result struct {
		OK                 bool `json:"ok"`
		Skipped            int  `json:"skippedBeforeOwnership"`
		IdentityMismatches int  `json:"identityMismatches"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result); err != nil || !result.OK {
		return errors.New("telemetry endpoint did not acknowledge observations")
	}
	if result.IdentityMismatches > 0 {
		return errors.New("observed BGP neighbor identity differs from configuration; verify deployed peer mapping")
	}
	if result.Skipped > 0 {
		return errors.New("ownership changed during collection; collect a fresh sample with new bindings")
	}
	return nil
}
