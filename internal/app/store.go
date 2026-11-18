package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/moedove/ixp-manager/internal/core"
	"github.com/moedove/ixp-manager/internal/notify"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type Record map[string]any

type Audit struct {
	Details    Record `json:"details,omitempty"`
	ID         string `json:"id"`
	Time       string `json:"time"`
	Actor      string `json:"actor"`
	ActorName  string `json:"actorName,omitempty"`
	Action     string `json:"action"`
	Resource   string `json:"resource"`
	ResourceID string `json:"resourceId"`
}
type Deployment struct {
	AS112NextHop4       string            `json:"as112NextHop4,omitempty"`
	AS112NextHop6       string            `json:"as112NextHop6,omitempty"`
	AS112Service        string            `json:"as112Service,omitempty"`
	AS112IPv4           bool              `json:"as112IPv4,omitempty"`
	AS112IPv6           bool              `json:"as112IPv6,omitempty"`
	RFC1997Passthrough  *bool             `json:"rfc1997Passthrough,omitempty"`
	RFC1997Mode         string            `json:"rfc1997Mode,omitempty"`
	RouterRole          string            `json:"routerRole,omitempty"`
	CollectorPolicy     string            `json:"collectorPolicy,omitempty"`
	ArtifactVersion     int               `json:"artifactVersion,omitempty"`
	ArtifactSize        int64             `json:"artifactSize,omitempty"`
	ArtifactChecksum    string            `json:"artifactChecksum,omitempty"`
	CredentialRevisions map[string]string `json:"credentialRevisions,omitempty"`
	CredentialPresence  map[string]bool   `json:"credentialPresence,omitempty"`
	Pruned              bool              `json:"pruned,omitempty"`
	PrunedAt            time.Time         `json:"prunedAt,omitempty"`
	PrunedBy            string            `json:"prunedBy,omitempty"`
	ID                  string            `json:"id"`
	RouterID            string            `json:"routerId"`
	Platform            string            `json:"platform"`
	Status              string            `json:"status"`
	Config              string            `json:"config,omitempty"`
	Checksum            string            `json:"checksum"`
	CreatedAt           string            `json:"createdAt"`
	Validation          string            `json:"validation,omitempty"`
	ApprovedBy          string            `json:"approvedBy,omitempty"`
}
type State struct {
	MailingLists             *MailingState            `json:"mailingLists,omitempty"`
	MemberLogos              map[string]MemberLogo    `json:"memberLogos,omitempty"`
	LegacyImports            []LegacyImportApproval   `json:"legacyImports,omitempty"`
	SFlowReceivers           []SFlowReceiver          `json:"sflowReceivers,omitempty"`
	ObservationKeyHash       string                   `json:"observationKeyHash,omitempty"`
	CoreBundles              []core.Bundle            `json:"coreBundles,omitempty"`
	Atlas                    AtlasState               `json:"atlas,omitempty"`
	LookingGlassQueries      []LookingGlassQuery      `json:"lookingGlassQueries,omitempty"`
	ProvisionRevisions       []ProvisionRevision      `json:"provisionRevisions,omitempty"`
	ProvisionSerials         map[string]uint32        `json:"provisionSerials,omitempty"`
	ProvisionApprovedSerials map[string]uint32        `json:"provisionApprovedSerials,omitempty"`
	PeerCredentials          map[string]BGPCredential `json:"peerCredentials,omitempty"`
	PeeringRequests          []PeeringRequest         `json:"peeringRequests,omitempty"`
	ExternalProposals        []ExternalProposal       `json:"externalProposals,omitempty"`
	DeviceDiscoveries        []DeviceDiscovery        `json:"deviceDiscoveries,omitempty"`
	Outbox                   notify.Queue             `json:"outbox"`
	Recovery                 []RecoveryRequest        `json:"recovery,omitempty"`
	Invitations              []Invitation             `json:"invitations,omitempty"`
	Jobs                     []Job                    `json:"jobs,omitempty"`
	PatchHistory             []PatchHistory           `json:"patchHistory,omitempty"`
	LegacyPatching           *LegacyPatchingState     `json:"legacyPatching,omitempty"`
	LegacyDocstore           *LegacyDocstoreState     `json:"legacyDocstore,omitempty"`
	NoteNotifications        *NoteNotificationState   `json:"noteNotifications,omitempty"`
	PatchNotifications       *PatchNotificationState  `json:"patchNotifications,omitempty"`
	LegacyMailing            *LegacyMailingState      `json:"legacyMailing,omitempty"`
	LegacyNotes              *LegacyNotesState        `json:"legacyNotes,omitempty"`
	LegacyAudit              []legacyAuditBatch       `json:"legacyAudit,omitempty"`
	LOAs                     []LOA                    `json:"loas,omitempty"`
	PolicySnapshots          []PolicySnapshot         `json:"policySnapshots,omitempty"`
	RememberedDevices        []RememberedDevice       `json:"rememberedDevices,omitempty"`
	Security                 map[string]SecurityUser  `json:"security"`
	Directories              []Directory              `json:"directories,omitempty"`
	Documents                []Document               `json:"documents,omitempty"`
	Version                  int                      `json:"version"`
	Resources                map[string][]Record      `json:"resources"`
	Settings                 Record                   `json:"settings"`
	Audit                    []Audit                  `json:"audit"`
	Deployments              []Deployment             `json:"deployments"`
}
type Store struct {
	revision    uint64
	closed      bool
	securityKey []byte
	mu          sync.RWMutex
	path        string
	state       State
	lock        *os.File
}

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("data directory already in use: %w", err)
	}
	s := &Store{path: filepath.Join(dir, "state.json"), lock: lock, state: State{Version: 1, Resources: map[string][]Record{}, Settings: Record{"name": "MoeDove IXP", "asn": 0}, Audit: []Audit{}, Deployments: []Deployment{}}}
	b, err := os.ReadFile(s.path)
	if err == nil {
		var loaded State
		decoder := json.NewDecoder(bytes.NewReader(b))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&loaded)
		if err == nil {
			if e := decoder.Decode(new(any)); e != io.EOF {
				err = errors.New("state must contain one JSON document")
			}
		}
		if err == nil {
			s.state = loaded
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.Close()
		return nil, err
	}
	if s.state.Version != 1 || s.state.Resources == nil || s.state.Settings == nil {
		s.Close()
		return nil, errors.New("invalid or unsupported data store")
	}
	if s.state.Security == nil {
		s.state.Security = map[string]SecurityUser{}
	}
	key, err := loadSecurityKey(dir, &s.state)
	if err != nil {
		s.Close()
		return nil, err
	}
	s.securityKey = key
	if err := normalizeLegacyPatching(&s.state); err != nil {
		s.Close()
		return nil, err
	}
	if err := validatePatching(&s.state); err != nil {
		s.Close()
		return nil, err
	}
	if err := validateDocuments(&s.state); err != nil {
		s.Close()
		return nil, err
	}
	if err := validateMemberLogos(&s.state); err != nil {
		s.Close()
		return nil, err
	}
	if err := validateDevices(&s.state); err != nil {
		s.Close()
		return nil, err
	}
	if err := validatePeering(&s.state); err != nil {
		s.Close()
		return nil, err
	}
	coreMigrated := normalizeLegacyCorePorts(&s.state)
	if err := validateAll(&s.state); err != nil {
		s.Close()
		return nil, fmt.Errorf("invalid persisted resources: %w", err)
	}
	if err := validateNoteNotifications(&s.state); err != nil {
		s.Close()
		return nil, err
	}
	if err := validateRememberedDevices(&s.state); err != nil {
		s.Close()
		return nil, err
	}
	if err := validateMailingState(&s.state); err != nil {
		s.Close()
		return nil, err
	}
	if err := validatePatchNotifications(&s.state); err != nil {
		s.Close()
		return nil, err
	}
	if err := validateLegacyMailing(&s.state); err != nil {
		s.Close()
		return nil, err
	}
	if err := validateLegacyNotes(&s.state); err != nil {
		s.Close()
		return nil, err
	}
	if err := validateLegacyAuditBatches(s.state.LegacyAudit); err != nil {
		s.Close()
		return nil, err
	}
	if err := validateBGPCredentials(&s.state); err != nil {
		s.Close()
		return nil, err
	}
	if err := validateProvisionProfiles(&s.state); err != nil {
		s.Close()
		return nil, err
	}
	if coreMigrated {
		if err := s.Update(func(next *State) error {
			audit(next, "", "core-reservation-migrate", "core-links", fmt.Sprint(len(next.Resources["core-links"])))
			return nil
		}); err != nil {
			s.Close()
			return nil, fmt.Errorf("persist core reservation migration: %w", err)
		}
	}
	return s, nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.lock == nil {
		return nil
	}
	err := syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
	return errors.Join(err, s.lock.Close())
}
func (s *Store) Snapshot() State { s.mu.RLock(); defer s.mu.RUnlock(); return cloneState(s.state) }
func cloneState(s State) State {
	b, _ := json.Marshal(s)
	var c State
	_ = json.Unmarshal(b, &c)
	if c.Security == nil {
		c.Security = map[string]SecurityUser{}
	}
	return c
}
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateLocked(fn)
}

// updateLocked commits a transaction while its caller holds the Store write lock.
func (s *Store) updateLocked(fn func(*State) error) error {
	if s.closed {
		return errors.New("data store is closed")
	}
	next := cloneState(s.state)
	if err := fn(&next); err != nil {
		return err
	}
	pruneRememberedDevices(&next)
	pruneNoteMailBindings(&next)
	prunePatchNotifications(&next)
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), s.path); err != nil {
		return err
	}
	s.state = next
	s.revision++
	// The committed snapshot must remain visible even if directory fsync fails.
	d, err := os.Open(filepath.Dir(s.path))
	if err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
func audit(s *State, actor, action, kind, id string) {
	actorName := str(find(s, "users", actor), "name")
	s.Audit = append(s.Audit, Audit{ID: token(12), Time: time.Now().UTC().Format(time.RFC3339Nano), Actor: actor, ActorName: actorName, Action: action, Resource: kind, ResourceID: id})
}
func str(r Record, k string) string     { v, _ := r[k].(string); return v }
func number(r Record, k string) float64 { v, _ := r[k].(float64); return v }
func find(s *State, kind, id string) Record {
	for _, r := range s.Resources[kind] {
		if str(r, "id") == id {
			return r
		}
	}
	return nil
}
func clean(r Record) Record {
	c := Record{}
	for k, v := range r {
		if k != "passwordHash" && k != "password" && k != "ownershipSince" && k != "bindingSince" {
			c[k] = v
		}
	}
	return c
}

func auditDetails(s *State, actor, action, kind, id string, details Record) {
	audit(s, actor, action, kind, id)
	s.Audit[len(s.Audit)-1].Details = details
}
