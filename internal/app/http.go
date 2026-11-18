package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/moedove/ixp-manager/internal/atlas"
	"github.com/moedove/ixp-manager/internal/integrations"
	"github.com/moedove/ixp-manager/internal/notify"
	"github.com/moedove/ixp-manager/internal/sflow"
	"github.com/moedove/ixp-manager/internal/telemetry"
	"github.com/moedove/ixp-manager/internal/traffic"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Config struct {
	QuaggaBinary                                       string
	Bird1Binary, Bird1IPv6Binary                       string
	AtlasConfig                                        string
	Bird3Binary, OpenBGPDBinary                        string
	NamedCheckzone                                     string
	PDNSServer                                         string
	ProviderConfig                                     string
	TrustedProxies                                     []string
	SMTPConfig                                         string
	OverallTrafficFile, OverallTrafficSHA256           string
	PublishOverallTraffic                              bool
	DocumentQuota                                      int64
	DataDir, WebDir, BirdBinary, FRRBinary, SetupToken string
	SecureCookie                                       bool
}
type App struct {
	quaggaBinary                                         string
	bird1Binary, bird1IPv6Binary                         string
	logoWork                                             chan struct{}
	legacyTraffic                                        *traffic.LegacyStore
	monthlyOverallTraffic                                *monthlyOverallTrafficFeed
	legacyMu                                             sync.Mutex
	sflow                                                *sflow.Store
	sflowMu                                              sync.Mutex
	sflowMappings                                        map[string]cachedFlowMapping
	sflowAdmissionMu                                     sync.Mutex
	sflowReaders                                         map[string]int
	sflowInFlight                                        int
	deliveryMu                                           sync.Mutex
	receiverApprovals                                    map[string]receiverApprovalCache
	pendingDeliveries                                    map[string]pendingDelivery
	deliveryStatus                                       map[string]deliveryObservation
	atlasProvider                                        atlas.Provider
	atlasConfig                                          atlas.Config
	atlasMu                                              sync.Mutex
	atlasClosed                                          bool
	bird3Binary, openBGPDBinary                          string
	namedCheckzone                                       string
	pdnsServer                                           string
	peeringDB                                            integrations.Provider
	deviceMu                                             sync.Mutex
	trustedProxies                                       []netip.Prefix
	mailConfig                                           notify.Config
	mailer                                               notify.Sender
	backgroundCancel                                     context.CancelFunc
	backgroundDone                                       chan struct{}
	backgroundHealth                                     schedulerHealthState
	backgroundMu                                         sync.Mutex
	storageCheckMu                                       sync.Mutex
	telemetry                                            *telemetry.Store
	documentQuota                                        int64
	store                                                *Store
	webDir, birdBinary, frrBinary, setupToken, dummyHash string
	secureCookie                                         bool
	mu                                                   sync.Mutex
	sessions                                             map[string]Session
	attempts                                             map[string]attempt
}

func New(c Config) (*App, error) {
	proxies, err := parseTrustedProxies(c.TrustedProxies)
	if err != nil {
		return nil, err
	}
	monthlyTraffic, err := loadMonthlyOverallTraffic(c.OverallTrafficFile, c.OverallTrafficSHA256, c.PublishOverallTraffic)
	if err != nil {
		return nil, err
	}
	s, err := OpenStore(c.DataDir)
	if err != nil {
		return nil, err
	}
	if c.SetupToken == "" {
		c.SetupToken = token(32)
	}
	a := &App{trustedProxies: proxies, store: s, webDir: c.WebDir, birdBinary: c.BirdBinary, frrBinary: c.FRRBinary, setupToken: c.SetupToken, secureCookie: c.SecureCookie, dummyHash: hashPassword(token(32)), sessions: map[string]Session{}, attempts: map[string]attempt{}}
	a.monthlyOverallTraffic = monthlyTraffic
	a.logoWork = make(chan struct{}, 2)
	a.namedCheckzone = c.NamedCheckzone
	a.pdnsServer = c.PDNSServer
	a.quaggaBinary = c.QuaggaBinary
	a.bird3Binary, a.openBGPDBinary = c.Bird3Binary, c.OpenBGPDBinary
	a.bird1Binary, a.bird1IPv6Binary = c.Bird1Binary, c.Bird1IPv6Binary
	a.documentQuota = c.DocumentQuota
	if a.documentQuota == 0 {
		a.documentQuota = 1 << 30
	}
	if err := a.validateSecurityEncryption(); err != nil {
		s.Close()
		return nil, err
	}
	if err := a.initDeploymentArtifacts(); err != nil {
		s.Close()
		return nil, err
	}
	if err := a.initProvisioningArtifacts(); err != nil {
		s.Close()
		return nil, err
	}
	if err := a.initDocuments(); err != nil {
		s.Close()
		return nil, err
	}
	if err := ValidateMemberLogoArtifacts(s); err != nil {
		s.Close()
		return nil, err
	}
	if err := a.initPolicy(); err != nil {
		s.Close()
		return nil, err
	}
	if err := a.initNotifications(c.SMTPConfig); err != nil {
		s.Close()
		return nil, err
	}
	if err := a.initIntegrations(c.ProviderConfig); err != nil {
		s.Close()
		return nil, err
	}
	if err := a.initDevices(); err != nil {
		s.Close()
		return nil, err
	}
	if err := a.initMonitoring(); err != nil {
		s.Close()
		return nil, err
	}
	if err := a.initLookingGlass(); err != nil {
		a.telemetry.Close()
		s.Close()
		return nil, err
	}
	if err := a.initAtlas(c.AtlasConfig); err != nil {
		a.telemetry.Close()
		s.Close()
		return nil, err
	}
	if err := a.initSFlow(); err != nil {
		a.closeAtlas()
		a.telemetry.Close()
		s.Close()
		return nil, err
	}
	if err := a.initLegacyTraffic(); err != nil {
		a.closeAtlas()
		a.sflow.Close()
		a.telemetry.Close()
		s.Close()
		return nil, err
	}
	return a, nil
}

// BootstrapToken is printed once by the local CLI, never exposed over HTTP.
func (a *App) BootstrapToken() string {
	if len(a.store.Snapshot().Resources["users"]) == 0 {
		return a.setupToken
	}
	return ""
}
func (a *App) Close() error {
	a.stopBackground()
	a.closeAtlas()
	var err error
	a.legacyMu.Lock()
	if a.legacyTraffic != nil {
		err = a.legacyTraffic.Close()
	}
	a.legacyMu.Unlock()
	a.sflowMu.Lock()
	if a.sflow != nil {
		err = errors.Join(err, a.sflow.Close())
	}
	a.sflowMu.Unlock()
	if a.telemetry != nil {
		err = errors.Join(err, a.telemetry.Close())
	}
	return errors.Join(err, a.store.Close())
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func problem(w http.ResponseWriter, status int, message string) {
	respond(w, status, Record{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := strings.Split(r.Header.Get("Content-Type"), ";")[0]; ct != "application/json" {
		problem(w, 415, "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		problem(w, 400, "invalid JSON: "+err.Error())
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		problem(w, 400, "request must contain one JSON value")
		return false
	}
	return true
}

const legacyCustomerSelectionPath = "/api/v4/customer/by-vlan-and-protocol"

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if r.URL.Path == "/healthz" && r.Method == "GET" {
		respond(w, 200, Record{"status": "ok"})
		return
	}
	ixfVersion, publicIXF, isIXF := ixfRoute(r.URL.Path)
	if !strings.HasPrefix(r.URL.Path, "/api/") && !(isIXF && publicIXF) {
		a.static(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	publicMonthlyTraffic, isMonthlyTraffic := monthlyOverallTrafficRoute(r.URL.Path)
	if isMonthlyTraffic && (publicMonthlyTraffic || a.monthlyOverallTrafficPublished()) {
		a.serveMonthlyOverallTraffic(w, r, nil, Session{})
		return
	}
	if _, _, public, matched := logoRoute(r.URL.Path); matched && public {
		a.memberLogoRoutes(w, r, nil, Session{})
		return
	}
	if r.URL.Path == "/api/public/documents" || strings.HasPrefix(r.URL.Path, "/api/public/documents/") {
		a.publicDocuments(w, r)
		return
	}
	if r.Header.Get("Authorization") != "" && bearerScope(r) == "" && !(r.URL.Path == "/api/search" && r.Method == "GET") {
		problem(w, 403, "API tokens cannot access this route")
		return
	}
	if isIXF && publicIXF {
		a.ixf(w, r, true, ixfVersion)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			problem(w, 403, "cross-site request rejected")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Host != r.Host {
				problem(w, 403, "origin rejected")
				return
			}
		}
	}
	if a.recoveryMailRoute(w, r) {
		return
	}
	if a.authRoute(w, r) {
		return
	}
	user, session, ok := a.authenticate(r)
	if !ok {
		problem(w, 401, "sign in required")
		return
	}
	if !session.API && r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("X-CSRF-Token") != session.CSRF {
		problem(w, 403, "invalid CSRF token")
		return
	}
	if memberAccountDetached(user) && !(r.URL.Path == "/api/session" && r.Method == http.MethodGet) && !(r.URL.Path == "/api/logout" && r.Method == http.MethodPost) {
		problem(w, http.StatusForbidden, "this account must accept a member invitation to access the workspace")
		return
	}
	// Search enforces each included domain's current read scope itself.
	if r.URL.Path == "/api/search" {
		a.search(w, r, user, session)
		return
	}
	if session.API {
		scope := bearerScope(r)
		if scope == "" || !contains(session.Scopes, scope) || !contains(availableScopes(str(user, "role")), scope) {
			problem(w, 403, "API token scope does not allow this action")
			return
		}
	}
	if isMonthlyTraffic {
		a.serveMonthlyOverallTraffic(w, r, user, session)
		return
	}
	if a.legacyAccountInvitationRoutes(w, r, user, session) {
		return
	}
	if r.URL.Path == legacyCustomerSelectionPath {
		a.legacyCustomersByVlanAndProtocol(w, r, user, session)
		return
	}
	if r.URL.Path == "/api/notifications" || strings.HasPrefix(r.URL.Path, "/api/notifications/") {
		a.notifications(w, r, user, session)
		return
	}
	if r.URL.Path == "/api/jobs" || strings.HasPrefix(r.URL.Path, "/api/jobs/") {
		a.jobs(w, r, user, session)
		return
	}
	if r.URL.Path == storageCheckPath {
		a.runStorageCheck(w, r, user, session)
		return
	}
	if r.URL.Path == "/api/invitations" || strings.HasPrefix(r.URL.Path, "/api/invitations/") {
		a.invitations(w, r, user)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/fabrics/") {
		a.fabrics(w, r, user, session)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/integrations") {
		a.integrations(w, r, user, session)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/peering") {
		a.peering(w, r, user)
		return
	}
	if ipamScope(r.URL.Path) {
		a.ipam(w, r, user)
		return
	}
	if a.macLearning(w, r, user, session) || a.devices(w, r, user, session) {
		return
	}
	if a.patchNotifications(w, r, user, session) || a.patching(w, r, user) {
		return
	}
	if a.legacyNotesRoutes(w, r, user, session) {
		return
	}
	if a.legacyAuditRoutes(w, r, user, session) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/account/note-") {
		a.noteNotificationsRoutes(w, r, user, session)
		return
	}
	if a.legacyMailingRoutes(w, r, user, session) {
		return
	}
	if a.mailingListsRoutes(w, r, user, session) {
		return
	}
	if a.rememberedDevicesRoutes(w, r, user, session) {
		return
	}
	if a.contactExportRoutes(w, r, user, session) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/account/") {
		a.account(w, r, user, session)
		return
	}
	role := str(user, "role")
	admin := role == "admin"
	write := admin || role == "operator"
	if r.URL.Path == "/api/session" && r.Method == "GET" {
		respond(w, 200, Record{"user": user, "csrfToken": session.CSRF})
		return
	}
	if r.URL.Path == "/api/logout" && r.Method == "POST" {
		a.logoutSession(w, r, user, session)
		return
	}
	if r.URL.Path == "/api/memberships" || strings.HasPrefix(r.URL.Path, "/api/memberships/") {
		a.memberships(w, r, user, session)
		return
	}
	if a.memberLogoRoutes(w, r, user, session) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/member/") {
		a.memberService(w, r, user, session)
		return
	}
	if a.memberWelcomeRoutes(w, r, user, session) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/members/") {
		a.memberLifecycle(w, r, user)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/monitoring/") {
		a.monitoring(w, r, user, session)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/traffic/") {
		a.trafficRoutes(w, r, user, session)
		return
	}
	if r.URL.Path == "/api/sflow" || strings.HasPrefix(r.URL.Path, "/api/sflow/") {
		a.sflowRoutes(w, r, user, session)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/policy/") {
		a.policyRoutes(w, r, user)
		return
	}
	if r.URL.Path == "/api/looking-glass" || strings.HasPrefix(r.URL.Path, "/api/looking-glass/") {
		a.lookingGlassRoutes(w, r, user, session)
		return
	}
	if r.URL.Path == "/api/atlas" || strings.HasPrefix(r.URL.Path, "/api/atlas/") {
		a.atlasRoutes(w, r, user, session)
		return
	}
	if r.URL.Path == "/api/core" || strings.HasPrefix(r.URL.Path, "/api/core/") {
		if !a.coreObservationRoutes(w, r, user, session) {
			a.coreRoutes(w, r, user, session)
		}
		return
	}
	if r.URL.Path == "/api/peer-credentials" || strings.HasPrefix(r.URL.Path, "/api/peer-credentials/") {
		a.peerCredentials(w, r, user, session)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/peer-readiness/") {
		a.peerReadiness(w, r, user, session)
		return
	}
	if !write && r.Method != "GET" && !(r.Method == "HEAD" && strings.HasPrefix(r.URL.Path, "/api/documents/")) {
		problem(w, 403, "read-only account")
		return
	}
	if r.URL.Path == "/api/documents" || strings.HasPrefix(r.URL.Path, "/api/documents/") || r.URL.Path == "/api/directories" || strings.HasPrefix(r.URL.Path, "/api/directories/") {
		a.documents(w, r, user, session)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/resources/") {
		if r.URL.Path == "/api/resources/"+provisionProfileKind || strings.HasPrefix(r.URL.Path, "/api/resources/"+provisionProfileKind+"/") {
			a.provisionProfiles(w, r, user, session)
			return
		}
		a.resources(w, r, user, session)
		return
	}
	if isIXF {
		if role == "member" {
			problem(w, 403, "staff access required")
			return
		}
		a.ixf(w, r, false, ixfVersion)
		return
	}
	switch r.URL.Path {
	case "/api/router-platforms":
		if r.Method == "GET" {
			a.platforms(w, r)
			return
		}
	case "/api/schema":
		if r.Method == "GET" {
			respond(w, 200, Schemas)
			return
		}
	case "/api/dashboard":
		if r.Method == "GET" {
			s := a.store.Snapshot()
			counts := Record{}
			for k := range Schemas {
				counts[k] = len(visible(&s, k, user))
			}
			recent := []Audit{}
			if role != "member" {
				recent = s.Audit
				if len(recent) > 8 {
					recent = recent[len(recent)-8:]
				}
			}
			respond(w, 200, Record{"counts": counts, "recentAudit": recent, "settings": s.Settings})
			return
		}
	case "/api/audit":
		if role == "member" {
			problem(w, 403, "staff access required")
			return
		}
		if r.Method == "GET" {
			s := a.store.Snapshot()
			items := s.Audit
			if items == nil {
				items = []Audit{}
			}
			respond(w, 200, Record{"items": items})
			return
		}
	case "/api/settings":
		if role == "member" {
			problem(w, 403, "staff access required")
			return
		}
		if r.Method == "GET" {
			respond(w, 200, a.store.Snapshot().Settings)
			return
		}
		if r.Method == "PUT" && admin {
			var b struct {
				Name         string  `json:"name"`
				ASN          float64 `json:"asn"`
				Website      string  `json:"website"`
				SupportEmail string  `json:"supportEmail"`
				IXFID        float64 `json:"ixfId"`
				ShortName    string  `json:"shortName"`
				Country      string  `json:"country"`
				PublishIXF   bool    `json:"publishIxf"`
			}
			if !decode(w, r, &b) {
				return
			}
			if strings.TrimSpace(b.Name) == "" || b.ASN < 1 || b.ASN > 4294967294 || b.ASN != float64(uint32(b.ASN)) {
				problem(w, 400, "name and valid ASN required")
				return
			}
			if b.IXFID < 0 || b.IXFID > 9007199254740991 || b.IXFID != float64(uint64(b.IXFID)) {
				problem(w, 400, "publishing IX-F requires an assigned IX-F ID and short name")
				return
			}
			if b.Country != "" && (len(b.Country) != 2 || b.Country[0] < 'A' || b.Country[0] > 'Z' || b.Country[1] < 'A' || b.Country[1] > 'Z') {
				problem(w, 400, "country must be a two-letter uppercase country code")
				return
			}
			settings := Record{"name": b.Name, "asn": b.ASN, "website": b.Website, "supportEmail": b.SupportEmail, "ixfId": b.IXFID, "shortName": b.ShortName, "country": b.Country, "publishIxf": b.PublishIXF}
			err := a.store.Update(func(s *State) error {
				s.Settings = settings
				if b.PublishIXF {
					if _, err := exportIXF(s); err != nil {
						return fmt.Errorf("cannot publish IX-F: %w", err)
					}
				}
				audit(s, str(user, "id"), "update", "settings", "")
				return nil
			})
			if err != nil {
				problem(w, 400, err.Error())
			} else {
				respond(w, 200, settings)
			}
			return
		}
	case "/api/export":
		if !admin {
			problem(w, 403, "administrator required")
			return
		}
		if r.Method == "GET" {
			s := a.store.Snapshot()
			delete(s.Resources, "users")
			warnings := []string{}
			if len(s.MemberLogos) > 0 {
				warnings = append(warnings, "Member logos and publication permissions are excluded from resource JSON; use a complete offline backup")
			}
			if len(s.PeerCredentials) > 0 || len(s.Deployments) > 0 || len(s.ProvisionRevisions) > 0 {
				warnings = append(warnings, "BGP credentials and generated configuration artifacts are excluded from this resource export; use ixp-backup for a complete private backup")
			}
			if err := carryProvisionSerialFloors(&s); err != nil {
				problem(w, 409, "DNS serial baseline is ambiguous; reconcile the zone before exporting resources")
				return
			}
			w.Header().Set("Content-Disposition", `attachment; filename="moedove-export.json"`)
			if len(s.SFlowReceivers) > 0 || len(s.LegacyImports) > 0 {
				warnings = append(warnings, "Receiver approvals and observation/legacy history are excluded; use the complete offline backup for those identities and files")
			}
			if s.LegacyPatching != nil || len(s.Documents) > 0 || len(s.PatchHistory) > 0 {
				warnings = append(warnings, "Document bytes, legacy patch snapshots and allocation-bound attachment permissions are excluded from resource JSON; use a complete offline backup")
			}
			if s.LegacyNotes != nil {
				warnings = append(warnings, "Original note/preference archives, account mappings and acceptance receipts are excluded from resource JSON; use a complete offline backup or the staff archive download")
			}
			respond(w, 200, Record{"version": 1, "resources": s.Resources, "coreBundles": exportCoreBundles(s.CoreBundles), "sflowReceivers": exportSFlowReceivers(&s), "source": "moedove-ixp", "warnings": warnings})
			return
		}
	case "/api/import/patching":
		a.importPatchBundle(w, r, user, session)
		return
	case "/api/import/member-logos", "/api/import/member-logos/preview":
		a.importMemberLogos(w, r, user, session)
		return
	case "/api/import/docstore":
		a.importDocstoreBundle(w, r, user, session)
		return
	case "/api/import", "/api/import/preview":
		if !admin {
			problem(w, 403, "administrator required")
			return
		}
		if r.Method == "POST" {
			a.importData(w, r, user, session)
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, "/api/deployments") {
		if role == "member" {
			problem(w, 403, "staff access required")
			return
		}
		a.deployments(w, r, user, session)
		return
	}
	if r.URL.Path == "/api/provisioning/monitoring-health" {
		a.monitoringDeliveryHealth(w, r, user, session)
		return
	}
	if r.URL.Path == "/api/provisioning" || strings.HasPrefix(r.URL.Path, "/api/provisioning/") {
		a.provisioning(w, r, user, session)
		return
	}
	problem(w, 404, "API endpoint not found")
}
func (a *App) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		problem(w, 405, "method not allowed")
		return
	}
	if a.webDir == "" {
		problem(w, 503, "frontend not built; run make build")
		return
	}
	p := filepath.Join(a.webDir, filepath.Clean("/"+r.URL.Path))
	info, err := os.Stat(p)
	if err == nil && !info.IsDir() {
		http.ServeFile(w, r, p)
		return
	}
	if filepath.Ext(r.URL.Path) != "" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(a.webDir, "index.html"))
}
func visible(s *State, kind string, user Record) []Record {
	out := []Record{}
	for _, v := range s.Resources[kind] {
		if str(user, "role") == "member" {
			member := str(user, "memberId")
			allowed := false
			switch kind {
			case "members":
				allowed = str(v, "id") == member
			case "connections", "contacts", "equipment":
				allowed = str(v, "memberId") == member
			case "member-tags":
				allowed = str(v, "memberId") == member && find(s, "tags", str(v, "tagId"))["internal"] != true
			case "tags":
				if v["internal"] != true {
					for _, link := range s.Resources["member-tags"] {
						if str(link, "memberId") == member && str(link, "tagId") == str(v, "id") {
							allowed = true
						}
					}
				}
			case "notes":
				allowed = str(v, "memberId") == member && str(v, "visibility") == "member"
			}
			if !allowed {
				continue
			}
		}
		if kind == "connections" {
			out = append(out, decorateConnection(s, v))
		} else if kind == "console-servers" || kind == "console-connections" {
			out = append(out, decorateConsole(s, kind, v))
		} else {
			out = append(out, clean(v))
		}
	}
	return out
}
func (a *App) resources(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/resources/"), "/")
	kind := parts[0]
	if _, ok := Schemas[kind]; !ok || len(parts) > 2 {
		problem(w, 404, "unknown resource")
		return
	}
	id := ""
	if len(parts) == 2 {
		id = parts[1]
	}
	role := str(user, "role")
	if kind == "users" && role != "admin" {
		problem(w, 403, "administrator required")
		return
	}
	if r.Method == "GET" {
		s := a.store.Snapshot()
		items := visible(&s, kind, user)
		if kind == "console-connections" && id == "" {
			query := r.URL.Query()
			serverID, memberID := query.Get("consoleServerId"), query.Get("memberId")
			if serverID != "" || memberID != "" {
				filtered := []Record{}
				for _, item := range items {
					if (serverID == "" || str(item, "consoleServerId") == serverID) &&
						(memberID == "" || str(item, "memberId") == memberID) {
						filtered = append(filtered, item)
					}
				}
				items = filtered
			}
		}
		for i, item := range items {
			items[i] = a.resourceView(&s, kind, item)
		}
		if id != "" {
			for _, v := range items {
				if str(v, "id") == id {
					w.Header().Set("ETag", fmt.Sprintf("%q", str(v, "_etag")))
					respond(w, 200, v)
					return
				}
			}
			problem(w, 404, "resource not found")
			return
		}
		respond(w, 200, Record{"items": items})
		return
	}
	if r.Method != "POST" && r.Method != "PUT" && r.Method != "DELETE" {
		problem(w, 405, "method not allowed")
		return
	}
	if (r.Method == "POST" && id != "") || (r.Method != "POST" && id == "") {
		problem(w, 400, "invalid resource URL")
		return
	}
	var body Record
	var passwordHash string
	if r.Method != "DELETE" {
		if !decode(w, r, &body) {
			return
		}
		if body == nil {
			problem(w, 400, "request must be a JSON object")
			return
		}
		for _, key := range []string{"passwordHash", "id", "createdAt", "updatedAt", "ownershipSince", "bindingSince", membershipDetachedField} {
			if _, ok := body[key]; ok {
				problem(w, 400, "server-managed field: "+key)
				return
			}
		}
		if kind == "users" && str(body, "password") != "" {
			password := str(body, "password")
			if len(password) < 12 || len(password) > 128 {
				problem(w, 400, "password must contain 12 to 128 bytes")
				return
			}
			passwordHash = hashPassword(password)
		}
	}
	expected, preconditionStatus, preconditionErr := resourcePrecondition(r, body)
	if kind == "infrastructures" && r.Method == "POST" {
		if _, provided := body["excludeIxf"]; !provided {
			body["excludeIxf"] = true
		}
	}
	var result Record
	status := 200
	err := a.store.Update(func(s *State) error {
		if code, err := resourceWriter(s, user, session, kind); err != nil {
			status = code
			return err
		}
		old := find(s, kind, id)
		if r.Method != "POST" && old == nil {
			status = 404
			return fmt.Errorf("resource not found")
		}
		if preconditionErr != nil {
			status = preconditionStatus
			return preconditionErr
		}
		if r.Method != "POST" {
			current := a.resourceETag(kind, old)
			if current == "" {
				status = 500
				return fmt.Errorf("resource revision unavailable")
			}
			if current != expected {
				status = http.StatusPreconditionFailed
				return fmt.Errorf("resource changed since it was opened; reload and review the newer values before saving")
			}
		}
		for _, binding := range []struct{ kind, key string }{{"members", "peeringdbId"}, {"connections", "peeringdbNetixlanId"}} {
			if kind != binding.kind {
				continue
			}
			value, provided := body[binding.key]
			if r.Method == "DELETE" {
				continue
			}
			if provided {
				if old == nil || fmt.Sprint(value) != fmt.Sprint(old[binding.key]) {
					return fmt.Errorf("external association IDs are managed through integration review")
				}
			} else if old != nil && old[binding.key] != nil {
				body[binding.key] = old[binding.key]
			}
		}
		if kind == "peers" && old != nil && r.Method == "PUT" {
			if _, provided := body["allowedRoutes"]; !provided && old["allowedRoutes"] != nil {
				if !samePrefixSet(old["allowedPrefixes"], body["allowedPrefixes"]) {
					return fmt.Errorf("this peer has origin-bound policy; review changes in the policy center")
				}
				body["allowedRoutes"] = old["allowedRoutes"]
			}
		}
		if kind == "members" && r.Method == "PUT" && old != nil && str(old, "status") != str(body, "status") && (str(body, "status") == "suspended" || str(body, "status") == "closed") {
			return fmt.Errorf("use the member lifecycle action to disable related connection and peer intents atomically")
		}
		if r.Method != "POST" && old == nil {
			status = 404
			return fmt.Errorf("resource not found")
		}
		if err := guardPeerCredentialMutation(s, kind, r.Method, old, body); err != nil {
			return err
		}
		if err := guardBIRD1ResourceMutation(s, kind, r.Method, body); err != nil {
			return err
		}
		if err := guardProvisionDNSMutation(s, kind, r.Method, old, body); err != nil {
			return err
		}
		if err := guardLegacyCustomerResourceMutation(kind, r.Method, old, body); err != nil {
			return err
		}
		if err := guardDeviceResourceMutation(s, kind, r.Method, old, body); err != nil {
			return err
		}
		if err := guardCoreResourceMutation(s, kind, r.Method, old, body); err != nil {
			return err
		}
		if err := guardTrafficResourceMutation(kind, r.Method, old, body); err != nil {
			return err
		}
		if err := guardConsoleMutation(s, kind, r.Method, old, body); err != nil {
			return err
		}
		if err := guardPatchResourceMutation(s, kind, r.Method, old, body); err != nil {
			return err
		}
		if kind == "users" && r.Method != "POST" && str(old, "role") == "admin" && (r.Method == "DELETE" || str(body, "role") != "admin") {
			n := 0
			for _, u := range s.Resources["users"] {
				if str(u, "role") == "admin" {
					n++
				}
			}
			if n <= 1 {
				return fmt.Errorf("cannot remove the last administrator")
			}
		}
		if r.Method == "DELETE" {
			if err := logoDeleteGuard(s, kind, id); err != nil {
				return err
			}
			for other, schema := range Schemas {
				for key, f := range schema {
					if f.Ref == kind {
						for _, v := range s.Resources[other] {
							if recordReferences(v, key, id) {
								return fmt.Errorf("resource is referenced by %s/%s", other, str(v, "id"))
							}
						}
					}
				}
			}
			for i, v := range s.Resources[kind] {
				if str(v, "id") == id {
					s.Resources[kind] = append(s.Resources[kind][:i], s.Resources[kind][i+1:]...)
					break
				}
			}
			if kind == "users" {
				delete(s.Security, id)
			}
			if kind == "peers" {
				delete(s.PeerCredentials, id)
			}
			if err := validateResellerFanouts(s); err != nil {
				return err
			}
			if err := validateDocuments(s); err != nil {
				return err
			}
			if err := validateDevices(s); err != nil {
				return err
			}
			if err := validatePeering(s); err != nil {
				return err
			}
			if err := validatePatching(s); err != nil {
				return err
			}
			if err := validateProvisionProfiles(s); err != nil {
				return err
			}
			if err := validateCore(s); err != nil {
				return err
			}
			if err := validateTrafficInventory(s); err != nil {
				return err
			}
			if err := validateSFlowInventory(s); err != nil {
				return err
			}
			if kind == "core-links" {
				stampCoreFlatPorts(s, old, nil, time.Now().UTC().Format(time.RFC3339Nano))
			}
			if kind == "patch-ports" {
				if err := recordPatchResourceChange(s, user, r.Method, old, nil); err != nil {
					return err
				}
			}
			if kind == "notes" {
				if err := a.noteChanged(s, user, session, old, nil); err != nil {
					return err
				}
			}
			audit(s, str(user, "id"), "delete", kind, id)
			return nil
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if old != nil {
			body["id"] = id
			body["createdAt"] = old["createdAt"]
			if kind == "users" {
				body["passwordHash"] = old["passwordHash"]
				if memberAccountDetached(old) && str(body, "role") == "member" && len(membershipIDs(body)) == 0 {
					body[membershipDetachedField] = true
				}
			}
		} else {
			id = token(16)
			body["id"] = id
			body["createdAt"] = now
		}
		body["updatedAt"] = now
		if kind == "infrastructures" && body["isPrimary"] == true {
			for _, other := range s.Resources[kind] {
				if str(other, "id") != id && other["isPrimary"] == true {
					other["isPrimary"] = false
					other["updatedAt"] = now
				}
			}
		}
		setOwnershipTime(kind, old, body, now)
		if err := validate(s, kind, body); err != nil {
			return err
		}
		if kind == "users" {
			password := str(body, "password")
			if old == nil && password == "" {
				return fmt.Errorf("password is required")
			}
			if password != "" {
				body["passwordHash"] = passwordHash
			}
			delete(body, "password")
		}
		if old == nil {
			s.Resources[kind] = append(s.Resources[kind], body)
		} else {
			for i, v := range s.Resources[kind] {
				if str(v, "id") == id {
					s.Resources[kind][i] = body
				}
			}
		}
		if kind == "users" && old != nil {
			invalidateSecurity(s, id)
		}
		if kind == "core-links" {
			stampCoreFlatPorts(s, old, body, now)
		}
		if err := validateAll(s); err != nil {
			return err
		}
		if err := validateDocuments(s); err != nil {
			return err
		}
		if err := validateDevices(s); err != nil {
			return err
		}
		if err := validatePeering(s); err != nil {
			return err
		}
		if err := validatePatching(s); err != nil {
			return err
		}
		if kind == "patch-ports" {
			if err := recordPatchResourceChange(s, user, r.Method, old, body); err != nil {
				return err
			}
		}
		if kind == "notes" {
			if err := a.noteChanged(s, user, session, old, body); err != nil {
				return err
			}
		}
		audit(s, str(user, "id"), strings.ToLower(r.Method), kind, id)
		if kind == "connections" {
			result = decorateConnection(s, body)
		} else if kind == "console-servers" || kind == "console-connections" {
			result = decorateConsole(s, kind, body)
		} else {
			result = clean(body)
		}
		result = a.resourceView(s, kind, result)
		return nil
	})
	if err != nil {
		if status == 200 {
			status = 400
		}
		problem(w, status, err.Error())
		return
	}
	if kind == "users" && r.Method != "POST" {
		a.mu.Lock()
		for key, v := range a.sessions {
			if v.UserID == id {
				delete(a.sessions, key)
			}
		}
		a.mu.Unlock()
	}
	if r.Method == "DELETE" {
		respond(w, 200, Record{"ok": true})
	} else {
		w.Header().Set("ETag", fmt.Sprintf("%q", str(result, "_etag")))
		if r.Method == "POST" {
			status = 201
		}
		respond(w, status, result)
	}
}
