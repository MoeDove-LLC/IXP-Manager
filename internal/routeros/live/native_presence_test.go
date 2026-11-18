package live

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
)

func nativePresenceFailure(t *testing.T, field string, err error) {
	t.Helper()
	code := "none"
	if err != nil {
		code = "unclassified"
		var failure *api.Error
		if errors.As(err, &failure) {
			code = string(failure.Code)
		}
	}
	t.Fatalf("%s=false api_code=%s", field, code)
}

// This is one independently selected primitive probe in a disposable pinned
// CHR. It does not invoke or override a production mutation path, take Safe
// Mode, apply a BGP policy, or prove batch removal/rollback semantics.
func TestNativeCHRLiveRuleObjectPresence(t *testing.T) {
	cfg := nativeLiveConfig(t)
	cfg.RouterID = "native-rule-object-presence"
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	r, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("presence_open=false code=%s", liveCode(err))
	}
	defer r.Close()
	profile, err := json.Marshal(r.profile)
	if err != nil {
		t.Fatal("presence_native_gates_unchanged=false")
	}
	reader, ok := r.api.(interface {
		ReadObjectPresence(context.Context, api.Resource, string) (api.ObjectPresence, error)
	})
	if !ok {
		t.Fatal("presence_reader_available=false")
	}
	before, err := r.Observe(ctx, control.ReadRequest{KnownState: routeros.KnownState{Target: r.target}, Strict: true})
	if err != nil || before.Snapshot == nil || !before.Snapshot.NamespaceEmpty || !before.CrossChannelIdentityVerified || !before.Owner.Known || !before.Owner.Alive || before.Owner.Active || before.Owner.ThisSession || !before.Owner.HistoryKnown || before.Owner.HistoryActions != 0 || len(before.Inventory.FilterRules) != 0 || len(before.Inventory.Connections) != 0 {
		t.Fatalf("presence_baseline_verified=false code=%s", liveCode(err))
	}
	var nonce [12]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		t.Fatal("presence_nonce_available=false")
	}
	chain, comment := "md_presence_"+hex.EncodeToString(nonce[:]), "mdlab-presence:"+hex.EncodeToString(nonce[:])
	const body = "reject;"
	// The menu was actually empty; this random chain is unreferenced and the
	// only direct fixture mutation is one Add followed by its exact-ID Remove.
	adds := 1
	added, err := r.api.Apply(ctx, api.Mutation{Resource: api.FilterRule, Action: api.Add, Attributes: []api.Attribute{{Name: "chain", Value: chain}, {Name: "rule", Value: body}, {Name: "comment", Value: comment}}})
	if err != nil || !added.Acknowledged || !nativeID.MatchString(added.ID) {
		nativePresenceFailure(t, "presence_fixture_add", err)
	}
	id := strings.ToUpper(added.ID)
	actual, err := reader.ReadObjectPresence(ctx, api.FilterRule, id)
	if err != nil || !actual.Present || actual.Record == nil || !strings.EqualFold(actual.Record[".id"], id) || actual.Record["chain"] != chain || actual.Record["rule"] != body || actual.Record["comment"] != comment {
		nativePresenceFailure(t, "presence_object_exact", err)
	}
	owner, err := r.Owner(ctx)
	if err != nil || !owner.Known || !owner.Alive || owner.Active || owner.ThisSession || !owner.HistoryKnown || owner.HistoryActions != 0 || owner.SessionID != before.Owner.SessionID {
		t.Fatalf("presence_owner_before_remove=false code=%s", liveCode(err))
	}
	removes := 1
	removed, err := r.api.Apply(ctx, api.Mutation{Resource: api.FilterRule, Action: api.Remove, ID: id})
	if err != nil || !removed.Acknowledged || removed.ID != "" {
		nativePresenceFailure(t, "presence_remove_acknowledged", err)
	}
	absent := 0
	for i := 0; i < 2; i++ {
		observation, err := reader.ReadObjectPresence(ctx, api.FilterRule, id)
		// Error, trap, EOF and missing completion can never count as absence.
		if err != nil || observation.Present || observation.Record != nil {
			nativePresenceFailure(t, "presence_absence_twice_verified", err)
		}
		absent++
	}
	after, err := r.api.Inventory(ctx)
	if err != nil || len(after.FilterRules) != 0 || after.SafeMode == nil || after.SafeMode.Enabled || after.SafeMode.Current {
		nativePresenceFailure(t, "presence_full_menu_absent", err)
	}
	if _, err = r.identity(ctx, after); err != nil {
		t.Fatalf("presence_final_identity_owner_verified=false code=%s", liveCode(err))
	}
	owner, err = r.Owner(ctx)
	if err != nil || !owner.Known || !owner.Alive || owner.Active || owner.ThisSession || !owner.HistoryKnown || owner.HistoryActions != 0 || owner.SessionID != before.Owner.SessionID {
		t.Fatalf("presence_final_identity_owner_verified=false code=%s", liveCode(err))
	}
	finalProfile, err := json.Marshal(r.profile)
	if err != nil || !bytes.Equal(profile, finalProfile) {
		t.Fatal("presence_native_gates_unchanged=false")
	}
	t.Logf("presence_rule_primitive_verified=true presence_baseline_verified=true presence_object_exact=true presence_remove_acknowledged=true presence_absence_twice_verified=true presence_full_menu_absent=true presence_final_identity_owner_verified=true presence_native_gates_unchanged=true presence_add_attempts=%d presence_remove_attempts=%d presence_absent_queries=%d presence_batch_gc_verified=false presence_publication_verified=false presence_rollback_verified=false", adds, removes, absent)
}
