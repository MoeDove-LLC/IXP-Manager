package app

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/lookingglass"
)

const lookingGlassFreshFor = 30 * time.Second
const lookingGlassQueueTTL = 2 * time.Minute
const lookingGlassLeaseTTL = 60 * time.Second

var errLookingGlassTarget = errors.New("diagnostic target not found")
var errLookingGlassStaff = errors.New("table queries require staff access")

type LookingGlassQuery struct {
	ID             string                `json:"id"`
	Revision       string                `json:"revision"`
	Query          lookingglass.Query    `json:"query"`
	RouterName     string                `json:"routerName"`
	PeerName       string                `json:"peerName,omitempty"`
	MemberID       string                `json:"memberId,omitempty"`
	RequesterID    string                `json:"requesterId"`
	MemberVisible  bool                  `json:"memberVisible"`
	Status         string                `json:"status"`
	CreatedAt      time.Time             `json:"createdAt"`
	ExpiresAt      time.Time             `json:"expiresAt"`
	StartedAt      time.Time             `json:"startedAt,omitempty"`
	CompletedAt    time.Time             `json:"completedAt,omitempty"`
	ObservedAt     time.Time             `json:"observedAt,omitempty"`
	Attempts       int                   `json:"attempts"`
	LeaseHash      string                `json:"leaseHash,omitempty"`
	LeaseOwner     string                `json:"leaseOwner,omitempty"`
	LeaseUntil     time.Time             `json:"leaseUntil,omitempty"`
	ResultStatus   string                `json:"resultStatus,omitempty"`
	ResultChecksum string                `json:"resultChecksum,omitempty"`
	File           encryptedArtifactFile `json:"file"`
	RouteCount     int                   `json:"routeCount"`
	Complete       bool                  `json:"complete"`
	Truncated      bool                  `json:"truncated"`
	ErrorCode      string                `json:"errorCode,omitempty"`
	Pruned         bool                  `json:"pruned"`
	PrunedAt       time.Time             `json:"prunedAt,omitempty"`
}

func lookingGlassRequiresKey(s State) bool {
	for _, q := range s.LookingGlassQueries {
		if q.File.Version != 0 && !q.Pruned {
			return true
		}
	}
	return false
}
func LookingGlassArtifactIDs(s State) []string {
	ids := []string{}
	for _, q := range s.LookingGlassQueries {
		if q.File.Version != 0 && !q.Pruned {
			ids = append(ids, q.ID)
		}
	}
	return ids
}

func lookingGlassTerminal(status string) bool {
	return status == "complete" || status == "failed" || status == "unsupported" || status == "expired" || status == "cancelled"
}
func lookingGlassDisplayStatus(q LookingGlassQuery, now time.Time) string {
	if !lookingGlassTerminal(q.Status) && !q.ExpiresAt.After(now) {
		return "expired"
	}
	return q.Status
}
func lookingGlassMemberEligible(s *State, peer Record) bool {
	if peer == nil || peer["enabled"] != true || str(find(s, "members", str(peer, "memberId")), "status") != "active" {
		return false
	}
	router := find(s, "routers", str(peer, "routerId"))
	for _, c := range s.Resources["connections"] {
		if str(c, "memberId") == str(peer, "memberId") && str(c, "vlanId") == str(router, "vlanId") && str(c, "status") == "active" && (sameObservationAddress(str(c, "ipv4"), str(peer, "address")) || sameObservationAddress(str(c, "ipv6"), str(peer, "address"))) {
			return true
		}
	}
	return false
}
func lookingGlassBinding(s *State, router, peer Record) string {
	values := []any{str(router, "id"), routerPlatform(router), number(router, "asn"), str(router, "routerId"), str(router, "localIpv4"), str(router, "localIpv6"), str(router, "vlanId"), routerRole(router), routerCollectorPolicy(router)}
	if peer != nil {
		values = append(values, str(peer, "id"), str(peer, "memberId"), str(peer, "address"), number(peer, "asn"), monitoringBindingRevision(s, "peer", str(peer, "id"), str(router, "id")))
	}
	b, _ := json.Marshal(values)
	return digest(string(b))
}
func lookingGlassBound(s *State, q LookingGlassQuery) bool {
	router := find(s, "routers", q.Query.RouterID)
	if router == nil {
		return false
	}
	var peer Record
	if q.Query.PeerID != "" {
		peer = find(s, "peers", q.Query.PeerID)
		if peer == nil || str(peer, "routerId") != q.Query.RouterID || str(peer, "memberId") != q.MemberID {
			return false
		}
	}
	return lookingGlassBinding(s, router, peer) == q.Query.OwnershipRevision
}
func lookingGlassVisible(s *State, q LookingGlassQuery, user Record) bool {
	if str(user, "role") != "member" {
		return true
	}
	return q.MemberVisible && q.MemberID == str(user, "memberId") && q.Query.PeerID != "" && lookingGlassBound(s, q) && lookingGlassMemberEligible(s, find(s, "peers", q.Query.PeerID))
}
func lookingGlassCanCancel(q LookingGlassQuery, user Record, now time.Time) bool {
	return !lookingGlassTerminal(lookingGlassDisplayStatus(q, now)) && (str(user, "role") == "admin" || q.RequesterID == str(user, "id"))
}
func lookingGlassAge(q LookingGlassQuery, now time.Time) time.Duration {
	age := now.Sub(q.ObservedAt)
	if receivedAge := now.Sub(q.CompletedAt); receivedAge > age {
		age = receivedAge
	}
	if age < 0 {
		return 0
	}
	return age
}
func lookingGlassPublic(q LookingGlassQuery, user Record, now time.Time) Record {
	status := lookingGlassDisplayStatus(q, now)
	return Record{"id": q.ID, "routerId": q.Query.RouterID, "routerName": q.RouterName, "peerId": q.Query.PeerID, "peerName": q.PeerName, "memberId": q.MemberID, "platform": q.Query.Platform, "view": q.Query.View, "prefix": q.Query.Prefix, "family": q.Query.Family, "maxRows": q.Query.MaxRows, "status": status, "revision": q.Revision, "createdAt": q.CreatedAt, "expiresAt": q.ExpiresAt, "startedAt": optionalTime(q.StartedAt), "completedAt": optionalTime(q.CompletedAt), "observedAt": optionalTime(q.ObservedAt), "resultStatus": q.ResultStatus, "routeCount": q.RouteCount, "complete": q.Complete, "truncated": q.Truncated, "errorCode": q.ErrorCode, "pruned": q.Pruned, "canCancel": lookingGlassCanCancel(q, user, now), "canPrune": str(user, "role") == "admin" && lookingGlassTerminal(status) && !q.Pruned}
}

func lookingGlassViews(platform string) []string {
	for _, c := range lookingglass.Capabilities() {
		if c.Platform == platform {
			return append([]string(nil), c.Views...)
		}
	}
	return []string{}
}
func lookingGlassPeerViews(platform string) []string {
	out := []string{}
	for _, v := range lookingGlassViews(platform) {
		if v != lookingglass.ViewTable {
			out = append(out, v)
		}
	}
	return out
}

func lookingGlassActor(s *State, user Record, session Session) (Record, error) {
	actor := find(s, "users", str(user, "id"))
	if actor == nil || s.Security[str(user, "id")].Generation != session.Generation || !rememberedSessionCurrent(s, session, time.Now()) {
		return nil, errors.New("authentication changed")
	}
	if str(actor, "role") == "member" && !contains(membershipIDs(actor), session.MemberID) {
		return nil, errors.New("member access changed")
	}
	if session.API {
		valid := false
		for _, current := range s.Security[str(user, "id")].Tokens {
			if current.ID != session.TokenID || !time.Now().Before(current.ExpiresAt) {
				continue
			}
			valid = true
			for _, scope := range session.Scopes {
				if !contains(current.Scopes, scope) {
					valid = false
					break
				}
			}
			break
		}
		if !valid {
			return nil, errors.New("API token expired, changed or was revoked")
		}
	} else if session.Expires.IsZero() || !time.Now().Before(session.Expires) {
		return nil, errors.New("session expired")
	}
	return withMember(actor, session.MemberID), nil
}

func (a *App) lookingGlassCapabilities(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if r.Method != "GET" {
		problem(w, 405, "method not allowed")
		return
	}
	s := a.store.Snapshot()
	actor, err := lookingGlassActor(&s, user, session)
	if err != nil {
		problem(w, 403, "diagnostic access changed")
		return
	}
	user = actor
	peers := []Record{}
	routers := []Record{}
	for _, peer := range s.Resources["peers"] {
		if str(user, "role") == "member" && (str(peer, "memberId") != str(user, "memberId") || !lookingGlassMemberEligible(&s, peer)) {
			continue
		}
		router := find(&s, "routers", str(peer, "routerId"))
		if router == nil {
			continue
		}
		views := lookingGlassPeerViews(routerPlatform(router))
		if len(views) == 0 {
			continue
		}
		peers = append(peers, Record{"peerId": peer["id"], "name": peer["name"], "routerId": router["id"], "routerName": router["name"], "platform": routerPlatform(router), "address": peer["address"], "asn": peer["asn"], "views": views})
	}
	if str(user, "role") != "member" {
		for _, router := range s.Resources["routers"] {
			views := lookingGlassViews(routerPlatform(router))
			if !contains(views, lookingglass.ViewTable) {
				continue
			}
			families := []int{}
			if str(router, "localIpv4") != "" {
				families = append(families, 4)
			}
			if str(router, "localIpv6") != "" {
				families = append(families, 6)
			}
			routers = append(routers, Record{"routerId": router["id"], "name": router["name"], "platform": routerPlatform(router), "families": families, "views": []string{lookingglass.ViewTable}})
		}
	}
	respond(w, 200, Record{"items": lookingglass.Capabilities(), "peers": peers, "routers": routers, "maxRows": lookingglass.MaxRows, "staleAfterSeconds": 30, "pollAfterSeconds": 2})
}

type lookingGlassRequest struct {
	Force    bool   `json:"force,omitempty"`
	PeerID   string `json:"peerId"`
	RouterID string `json:"routerId"`
	View     string `json:"view"`
	Prefix   string `json:"prefix"`
	Family   int    `json:"family"`
	MaxRows  int    `json:"maxRows"`
}

func prepareLookingGlass(s *State, body lookingGlassRequest, user Record, now time.Time) (LookingGlassQuery, error) {
	var peer, router Record
	if body.View == lookingglass.ViewTable {
		if str(user, "role") == "member" {
			return LookingGlassQuery{}, errLookingGlassStaff
		}
		if body.PeerID != "" {
			return LookingGlassQuery{}, errors.New("table queries require staff access and a fixed router target")
		}
		router = find(s, "routers", body.RouterID)
		if router == nil {
			return LookingGlassQuery{}, errors.New("router not found")
		}
		if (body.Family != 4 && body.Family != 6) || (body.Family == 4 && str(router, "localIpv4") == "") || (body.Family == 6 && str(router, "localIpv6") == "") {
			return LookingGlassQuery{}, errors.New("select an address family configured on this router")
		}
	} else {
		peer = find(s, "peers", body.PeerID)
		if peer == nil {
			return LookingGlassQuery{}, errLookingGlassTarget
		}
		if str(user, "role") == "member" && (str(peer, "memberId") != str(user, "memberId") || !lookingGlassMemberEligible(s, peer)) {
			return LookingGlassQuery{}, errLookingGlassTarget
		}
		router = find(s, "routers", str(peer, "routerId"))
		if router == nil || body.RouterID != "" && body.RouterID != str(router, "id") {
			return LookingGlassQuery{}, errors.New("peer router does not match")
		}
		address, err := netip.ParseAddr(str(peer, "address"))
		if err != nil {
			return LookingGlassQuery{}, errors.New("invalid peer address")
		}
		family := 6
		if address.Is4() {
			family = 4
		}
		if body.Family != 0 && body.Family != family {
			return LookingGlassQuery{}, errors.New("peer address family does not match")
		}
		body.Family = family
	}
	if !contains(lookingGlassViews(routerPlatform(router)), body.View) {
		return LookingGlassQuery{}, errors.New("this routing platform does not support the selected view")
	}
	query := lookingglass.Query{RouterID: str(router, "id"), PeerID: str(peer, "id"), Platform: routerPlatform(router), ExpectedRouterID: str(router, "routerId"), ExpectedRouterASN: uint32(number(router, "asn")), NeighborAddress: str(peer, "address"), NeighborASN: uint32(number(peer, "asn")), Family: body.Family, View: body.View, Prefix: body.Prefix, MaxRows: body.MaxRows, OwnershipRevision: lookingGlassBinding(s, router, peer)}
	query, err := lookingglass.NormalizeQuery(query)
	if err != nil {
		return LookingGlassQuery{}, err
	}
	return LookingGlassQuery{ID: token(16), Revision: token(16), Query: query, RouterName: str(router, "name"), PeerName: str(peer, "name"), MemberID: str(peer, "memberId"), RequesterID: str(user, "id"), MemberVisible: lookingGlassMemberEligible(s, peer), Status: "queued", CreatedAt: now, ExpiresAt: now.Add(lookingGlassQueueTTL)}, nil
}

func expireLookingGlass(s *State, now time.Time) bool {
	changed := false
	for i := range s.LookingGlassQueries {
		q := &s.LookingGlassQueries[i]
		if lookingGlassTerminal(q.Status) {
			continue
		}
		if !q.ExpiresAt.After(now) || q.Status == "running" && !q.LeaseUntil.After(now) && q.Attempts >= 3 {
			q.Status = "expired"
			q.CompletedAt = now
			q.ErrorCode = "deadline-exceeded"
			q.LeaseHash = ""
			q.LeaseOwner = ""
			q.Revision = token(16)
			changed = true
		} else if q.Status == "running" && !q.LeaseUntil.After(now) {
			q.Status = "queued"
			q.LeaseHash = ""
			q.LeaseOwner = ""
			q.Revision = token(16)
			changed = true
		}
	}
	return changed
}
func (a *App) sweepLookingGlass(now time.Time) error {
	s := a.store.Snapshot()
	needed := false
	for _, q := range s.LookingGlassQueries {
		if !lookingGlassTerminal(q.Status) && (!q.ExpiresAt.After(now) || q.Status == "running" && !q.LeaseUntil.After(now)) {
			needed = true
			break
		}
	}
	if !needed {
		return nil
	}
	return a.store.Update(func(s *State) error { expireLookingGlass(s, now); return nil })
}

func (a *App) lookingGlassRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if r.URL.Path == "/api/looking-glass/capabilities" {
		a.lookingGlassCapabilities(w, r, user, session)
		return
	}
	if r.URL.Path == "/api/looking-glass/claim" {
		a.claimLookingGlass(w, r, user, session)
		return
	}
	if r.URL.Path == "/api/looking-glass/complete" {
		a.completeLookingGlass(w, r, user, session)
		return
	}
	base := "/api/looking-glass/queries"
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, base), "/")
	if !strings.HasPrefix(r.URL.Path, base) || (len(parts) > 3) {
		problem(w, 404, "looking-glass endpoint not found")
		return
	}
	now := time.Now().UTC()
	if len(parts) == 1 && parts[0] == "" {
		if r.Method == "GET" {
			a.listLookingGlass(w, r, user, session)
			return
		}
		if r.Method != "POST" {
			problem(w, 405, "method not allowed")
			return
		}
		var body lookingGlassRequest
		if !decode(w, r, &body) {
			return
		}
		var result LookingGlassQuery
		cached := false
		status := 400
		err := a.store.Update(func(s *State) error {
			actor, err := lookingGlassActor(s, user, session)
			if err != nil {
				status = 403
				return err
			}
			q, err := prepareLookingGlass(s, body, actor, now)
			if err != nil {
				if errors.Is(err, errLookingGlassTarget) {
					status = 404
				}
				if errors.Is(err, errLookingGlassStaff) {
					status = 403
				}
				return err
			}
			if body.Force && (session.API || str(actor, "role") != "admin") {
				status = 403
				return errors.New("forcing a fresh diagnostic requires an administrator browser session")
			}
			expireLookingGlass(s, now)
			for _, old := range s.LookingGlassQueries {
				if old.Pruned || !lookingGlassVisible(s, old, actor) || old.Query != q.Query {
					continue
				}
				if !lookingGlassTerminal(old.Status) || !body.Force && old.Status == "complete" && lookingGlassAge(old, now) <= lookingGlassFreshFor {
					result = old
					cached = true
					return nil
				}
			}
			if len(s.LookingGlassQueries) >= 2000 {
				status = 409
				return errors.New("looking-glass history quota reached; an administrator must prune older queries")
			}
			pendingUser, pendingRouter, recent := 0, 0, 0
			for _, old := range s.LookingGlassQueries {
				if old.RequesterID == str(actor, "id") && now.Sub(old.CreatedAt) < time.Minute {
					recent++
				}
				if !lookingGlassTerminal(old.Status) {
					if old.RequesterID == str(actor, "id") {
						pendingUser++
					}
					if old.Query.RouterID == q.Query.RouterID {
						pendingRouter++
					}
				}
			}
			if pendingUser >= 4 || pendingRouter >= 8 || recent >= 20 {
				status = 429
				return errors.New("diagnostic queue is busy; wait before requesting another query")
			}
			s.LookingGlassQueries = append(s.LookingGlassQueries, q)
			audit(s, str(actor, "id"), "request-query", "looking-glass", q.ID)
			result = q
			return nil
		})
		if err != nil {
			problem(w, status, err.Error())
			return
		}
		code := 202
		if cached {
			code = 200
		}
		respond(w, code, Record{"item": lookingGlassPublic(result, user, now), "cached": cached})
		return
	}
	if len(parts) < 2 || parts[1] == "" {
		problem(w, 404, "query not found")
		return
	}
	s := a.store.Snapshot()
	actor, err := lookingGlassActor(&s, user, session)
	if err != nil {
		problem(w, 403, "diagnostic access changed")
		return
	}
	user = actor
	q := lookingGlassByID(&s, parts[1])
	if q == nil || !lookingGlassVisible(&s, *q, user) {
		problem(w, 404, "query not found")
		return
	}
	if len(parts) == 2 && r.Method == "GET" {
		respond(w, 200, lookingGlassPublic(*q, user, now))
		return
	}
	if len(parts) == 3 && parts[2] == "result" && r.Method == "GET" {
		a.lookingGlassResult(w, r, &s, *q, user)
		return
	}
	if len(parts) == 3 && parts[2] == "cancel" && r.Method == "POST" {
		a.cancelLookingGlass(w, r, user, session, *q)
		return
	}
	if len(parts) == 3 && parts[2] == "prune" && r.Method == "POST" {
		a.retainLookingGlass(w, r, user, session, *q, false)
		return
	}
	if len(parts) == 2 && r.Method == "DELETE" {
		a.retainLookingGlass(w, r, user, session, *q, true)
		return
	}
	problem(w, 405, "method not allowed")
}
func lookingGlassByID(s *State, id string) *LookingGlassQuery {
	for i := range s.LookingGlassQueries {
		if s.LookingGlassQueries[i].ID == id {
			return &s.LookingGlassQueries[i]
		}
	}
	return nil
}
func (a *App) listLookingGlass(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			problem(w, 400, "limit must be 1..100")
			return
		}
		limit = n
	}
	s := a.store.Snapshot()
	actor, err := lookingGlassActor(&s, user, session)
	if err != nil {
		problem(w, 403, "diagnostic access changed")
		return
	}
	user = actor
	start := len(s.LookingGlassQueries) - 1
	if cursor := r.URL.Query().Get("before"); cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || !identifier.MatchString(string(raw)) {
			problem(w, 400, "invalid cursor")
			return
		}
		found := false
		for i, q := range s.LookingGlassQueries {
			if q.ID == string(raw) && lookingGlassVisible(&s, q, user) {
				start = i - 1
				found = true
				break
			}
		}
		if !found {
			problem(w, 400, "invalid cursor")
			return
		}
	}
	items := []Record{}
	ids := []string{}
	more := false
	now := time.Now().UTC()
	for i := start; i >= 0; i-- {
		q := s.LookingGlassQueries[i]
		if !lookingGlassVisible(&s, q, user) {
			continue
		}
		if len(items) == limit {
			more = true
			break
		}
		items = append(items, lookingGlassPublic(q, user, now))
		ids = append(ids, q.ID)
	}
	next := ""
	if more {
		next = base64.RawURLEncoding.EncodeToString([]byte(ids[len(ids)-1]))
	}
	respond(w, 200, Record{"items": items, "nextCursor": next, "hasMore": more, "pollAfterSeconds": 2})
}

func (a *App) cancelLookingGlass(w http.ResponseWriter, r *http.Request, user Record, session Session, initial LookingGlassQuery) {
	var body struct {
		ExpectedRevision string `json:"expectedRevision"`
	}
	if !decode(w, r, &body) {
		return
	}
	var result LookingGlassQuery
	err := a.store.Update(func(s *State) error {
		actor, err := lookingGlassActor(s, user, session)
		if err != nil {
			return err
		}
		q := lookingGlassByID(s, initial.ID)
		if q == nil || !lookingGlassVisible(s, *q, actor) {
			return errors.New("query no longer available")
		}
		now := time.Now().UTC()
		if q.Revision != body.ExpectedRevision || !lookingGlassCanCancel(*q, actor, now) {
			return errors.New("query changed or cannot be cancelled")
		}
		q.Status = "cancelled"
		q.CompletedAt = now
		q.Revision = token(16)
		q.LeaseHash = ""
		q.LeaseOwner = ""
		audit(s, str(actor, "id"), "cancel-query", "looking-glass", q.ID)
		result = *q
		return nil
	})
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	respond(w, 200, lookingGlassPublic(result, user, time.Now().UTC()))
}
