// Package atlas implements constrained RIPE Atlas requests. Callers must persist
// a creation intent before Create; an uncertain POST must never be retried.
package atlas

import (
	"context"
	"time"
)

const OfficialOrigin = "https://atlas.ripe.net"
const MaxProbes = 20
const MaxSamples = 1000
const MaxEstimatedCredits = 20000
const MaxResponseBytes = 8 << 20

type Config struct {
	Enabled           bool   `json:"enabled"`
	BaseURL           string `json:"baseURL,omitempty"`
	APIKey            string `json:"apiKey"`
	AllowLoopbackHTTP bool   `json:"allowLoopbackHTTP,omitempty"`
}

func (c Config) String() string   { return "RIPE Atlas private configuration (credentials withheld)" }
func (c Config) GoString() string { return c.String() }

type Probe struct {
	ID            int64     `json:"id"`
	ASNV4         uint32    `json:"asnV4"`
	ASNV6         uint32    `json:"asnV6"`
	AddressV4     string    `json:"addressV4"`
	AddressV6     string    `json:"addressV6"`
	CountryCode   string    `json:"countryCode"`
	IsAnchor      bool      `json:"isAnchor"`
	IsPublic      bool      `json:"isPublic"`
	Status        string    `json:"status"`
	LastConnected time.Time `json:"lastConnected"`
}
type Spec struct {
	Type            string    `json:"type"`
	AF              int       `json:"af"`
	Target          string    `json:"target"`
	ProbeIDs        []int64   `json:"probeIds"`
	IsOneoff        bool      `json:"isOneoff"`
	IntervalSeconds int       `json:"intervalSeconds,omitempty"`
	DurationSeconds int       `json:"durationSeconds,omitempty"`
	StartAt         time.Time `json:"startAt,omitempty"`
	IsPublic        bool      `json:"isPublic"`
	Description     string    `json:"description"`
}
type Estimate struct {
	Samples  int    `json:"samples"`
	Credits  int    `json:"credits"`
	UnitCost int    `json:"unitCost"`
	Basis    string `json:"basis"`
}
type Measurement struct {
	ID              int64     `json:"id"`
	Type            string    `json:"type"`
	AF              int       `json:"af"`
	Target          string    `json:"target"`
	Description     string    `json:"description"`
	IsPublic        bool      `json:"isPublic"`
	IsOneoff        bool      `json:"isOneoff"`
	IntervalSeconds int       `json:"intervalSeconds,omitempty"`
	StartAt         time.Time `json:"startAt"`
	StopAt          time.Time `json:"stopAt,omitempty"`
	StatusID        int       `json:"statusId"`
	Status          string    `json:"status"`
}
type Reply struct {
	Address string   `json:"address,omitempty"`
	RTT     *float64 `json:"rtt,omitempty"`
	Timeout bool     `json:"timeout,omitempty"`
	Error   string   `json:"error,omitempty"`
}
type Hop struct {
	Hop     int     `json:"hop"`
	Replies []Reply `json:"replies"`
	Error   string  `json:"error,omitempty"`
}
type Result struct {
	ProbeID       int64     `json:"probeId"`
	MeasurementID int64     `json:"measurementId"`
	Type          string    `json:"type"`
	AF            int       `json:"af"`
	Timestamp     time.Time `json:"timestamp"`
	TargetAddress string    `json:"targetAddress"`
	SourceAddress string    `json:"sourceAddress,omitempty"`
	Sent          *int      `json:"sent,omitempty"`
	Received      *int      `json:"received,omitempty"`
	MinRTT        *float64  `json:"minRtt,omitempty"`
	AvgRTT        *float64  `json:"avgRtt,omitempty"`
	MaxRTT        *float64  `json:"maxRtt,omitempty"`
	Hops          []Hop     `json:"hops,omitempty"`
	Error         string    `json:"error,omitempty"`
}
type Provider interface {
	Origin() string
	Discover(context.Context, uint32, int, bool) ([]Probe, []byte, error)
	Create(context.Context, Spec) (int64, []byte, error)
	Measurement(context.Context, int64) (Measurement, []byte, error)
	Find(context.Context, string) ([]Measurement, []byte, error)
	Results(context.Context, int64, time.Time, time.Time) ([]Result, []byte, error)
	Stop(context.Context, int64) error
}
