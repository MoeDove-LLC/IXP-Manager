package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type PortBinding struct {
	PortID            string `json:"portId"`
	OwnershipRevision string `json:"ownershipRevision"`
}
type SessionBinding struct {
	PeerID            string `json:"peerId"`
	OwnershipRevision string `json:"ownershipRevision"`
}
type RouterIdentity struct {
	Platform string `json:"platform"`
	RouterID string `json:"routerId"`
	ASN      uint32 `json:"asn"`
}

func (i RouterIdentity) validQuagga() bool {
	addr, err := netip.ParseAddr(i.RouterID)
	return i.Platform == "quagga" && err == nil && addr.Is4() && addr.IsGlobalUnicast() &&
		i.ASN != 0 && i.ASN != 23456 && i.ASN != 65535 && i.ASN != ^uint32(0)
}

type Bindings struct {
	RouterID       string           `json:"routerId"`
	Ports          []PortBinding    `json:"ports"`
	Sessions       []SessionBinding `json:"sessions"`
	RouterIdentity *RouterIdentity  `json:"routerIdentity,omitempty"`
}

var bindingDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func FetchBindings(ctx context.Context, client *http.Client, endpoint, token, routerID string) (Bindings, error) {
	return fetchBindings(ctx, client, endpoint, token, routerID, false)
}

func FetchQuaggaBindings(ctx context.Context, client *http.Client, endpoint, token, routerID string) (Bindings, error) {
	return fetchBindings(ctx, client, endpoint, token, routerID, true)
}

func fetchBindings(ctx context.Context, client *http.Client, endpoint, token, routerID string, quagga bool) (Bindings, error) {
	var out Bindings
	if strings.TrimSpace(token) == "" || len(token) > 256 || strings.ContainsAny(token, "\r\n") {
		return out, errors.New("invalid collector token")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/api/monitoring/ingest" {
		return out, errors.New("invalid collector endpoint")
	}
	u.Path = "/api/monitoring/bindings"
	query := url.Values{"routerId": []string{routerID}}
	if quagga {
		query.Set("routerIdentity", "quagga")
	}
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if client == nil {
		client = &http.Client{}
	}
	safe := *client
	safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := safe.Do(req)
	if err != nil {
		return out, errors.New("collector ownership binding request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return out, fmt.Errorf("collector binding endpoint returned HTTP %d", response.StatusCode)
	}
	d := json.NewDecoder(io.LimitReader(response.Body, 8<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(&out); err != nil {
		return out, errors.New("invalid collector binding response")
	}
	if d.Decode(new(any)) != io.EOF || out.RouterID != routerID {
		return out, errors.New("binding response does not match router")
	}
	if quagga && (out.RouterIdentity == nil || !out.RouterIdentity.validQuagga()) {
		return out, errors.New("collector has no valid management binding for the local Quagga identity")
	}
	for _, v := range out.Ports {
		if v.PortID == "" || !bindingDigest.MatchString(v.OwnershipRevision) {
			return out, errors.New("invalid port binding")
		}
	}
	for _, v := range out.Sessions {
		if v.PeerID == "" || !bindingDigest.MatchString(v.OwnershipRevision) {
			return out, errors.New("invalid peer binding")
		}
	}
	return out, nil
}
func StampBindings(b *Batch, bindings Bindings) error {
	if b.RouterID != bindings.RouterID {
		return errors.New("collector binding belongs to another router")
	}
	ports := map[string]string{}
	peers := map[string]string{}
	for _, v := range bindings.Ports {
		if ports[v.PortID] != "" {
			return errors.New("duplicate port binding")
		}
		ports[v.PortID] = v.OwnershipRevision
	}
	for _, v := range bindings.Sessions {
		if peers[v.PeerID] != "" {
			return errors.New("duplicate peer binding")
		}
		peers[v.PeerID] = v.OwnershipRevision
	}
	for i := range b.Ports {
		value := ports[b.Ports[i].PortID]
		if value == "" {
			return errors.New("mapped port no longer has a valid ownership binding")
		}
		b.Ports[i].OwnershipRevision = value
	}
	for i := range b.Sessions {
		value := peers[b.Sessions[i].PeerID]
		if value == "" {
			return errors.New("mapped peer no longer has a valid ownership binding")
		}
		b.Sessions[i].OwnershipRevision = value
	}
	return nil
}
