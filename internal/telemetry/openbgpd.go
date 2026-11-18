package telemetry

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
)

// ParseOpenBGPDNeighbors consumes OpenBGPD8.8 bgpctl -j show neighbor JSON.
// Descriptions, authentication details and shutdown strings are never retained.
func ParseOpenBGPDNeighbors(raw []byte) (map[string]ObservedSession, error) {
	if len(raw) == 0 || len(raw) > 4<<20 {
		return nil, errors.New("OpenBGPD neighbor output size invalid")
	}
	var root struct {
		Neighbors *[]struct {
			Address string `json:"remote_addr"`
			ASN     string `json:"remote_as"`
			State   string `json:"state"`
			Stats   *struct {
				Prefixes *struct {
					Received *uint64 `json:"received"`
				} `json:"prefixes"`
			} `json:"stats"`
		} `json:"neighbors"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&root) != nil || decoder.Decode(new(any)) != io.EOF || root.Neighbors == nil {
		return nil, errors.New("unrecognized OpenBGPD neighbor JSON")
	}
	out := map[string]ObservedSession{}
	for _, peer := range *root.Neighbors {
		address, e := NormalizeNeighborAddress(peer.Address)
		if e != nil {
			return nil, errors.New("OpenBGPD neighbor address is unknown")
		}
		asn, e := strconv.ParseUint(peer.ASN, 10, 32)
		if e != nil || asn == 0 || asn == uint64(^uint32(0)) {
			return nil, errors.New("OpenBGPD neighbor ASN is unknown")
		}
		if _, exists := out[address]; exists {
			return nil, errors.New("ambiguous OpenBGPD neighbor identity")
		}
		observed := ObservedSession{NeighborAddress: address, NeighborASN: uint32(asn), State: sessionState(peer.State)}
		if peer.Stats != nil && peer.Stats.Prefixes != nil && peer.Stats.Prefixes.Received != nil {
			if *peer.Stats.Prefixes.Received > 1<<32-1 {
				return nil, errors.New("OpenBGPD prefix count exceeds uint32")
			}
			observed.ReceivedPrefixes = *peer.Stats.Prefixes.Received
			observed.PrefixesKnown = true
		}
		out[address] = verifiedObserved(observed)
	}
	return out, nil
}
