package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routing"
)

type blockedRuntime struct {
	target routeros.Target
	writes int
}

type readonlyReviewRuntime struct{ *blockedRuntime }

func (r *readonlyReviewRuntime) Observe(_ context.Context, request control.ReadRequest) (control.Observation, error) {
	inv := api.Inventory{Resources: []api.Record{{"version": r.target.Version}}, Licenses: []api.Record{{"system-id": r.target.DeviceID}}, Capabilities: map[api.Resource]api.Capability{}}
	for _, resource := range []api.Resource{api.SystemResource, api.SystemLicense, api.RoutingTable, api.BGPInstance, api.BGPConnection, api.FilterRule} {
		inv.Capabilities[resource] = api.Supported
	}
	evidence := routeros.TargetEvidence{APIAuthenticated: true, APIIdentitySource: routeros.LicenseSystemID, APITransportIdentitySHA256: r.target.TransportIdentitySHA256, SSHAuthenticated: true, SSHIdentitySource: routeros.LicenseSystemID, SSHTransportIdentitySHA256: r.target.TransportIdentitySHA256, SSHDeviceID: r.target.DeviceID, SSHVersion: r.target.Version}
	snapshot, err := routeros.BuildSnapshot(inv, request.KnownState, routeros.CredentialEvidence{}, evidence)
	if err != nil {
		return control.Observation{}, err
	}
	resolver, err := routeros.NewAPIResolver(inv, r.target, evidence)
	return control.Observation{Target: r.target, Inventory: inv, Snapshot: &snapshot, Resolver: resolver, CrossChannelIdentityVerified: true, Owner: control.Owner{Known: true, Alive: true}, ObservedAt: time.Now()}, err
}

func (r *blockedRuntime) Binding() control.Binding { return control.Binding{Target: r.target} }
func (r *blockedRuntime) Observe(context.Context, control.ReadRequest) (control.Observation, error) {
	panic("unverified runtime was observed")
}
func (r *blockedRuntime) Apply(context.Context, routeros.APIMutation) (api.Result, error) {
	r.writes++
	panic("unverified write reached transport")
}
func (r *blockedRuntime) BindPlan(routeros.Plan, routeros.Credentials) error {
	panic("unverified plan reached transport")
}
func (r *blockedRuntime) Take(context.Context, time.Time) (control.Owner, error) {
	panic("unverified take reached transport")
}
func (r *blockedRuntime) Owner(context.Context) (control.Owner, error) {
	return control.Owner{Known: true}, nil
}
func (r *blockedRuntime) Confirm(context.Context) (control.Owner, error) {
	panic("unverified confirm reached transport")
}
func (r *blockedRuntime) Cancel(context.Context) (control.Owner, error) {
	panic("unverified cancel reached transport")
}
func (r *blockedRuntime) CloseOwner() error { return nil }

func cliTarget() routeros.Target {
	return routeros.Target{RouterID: "cli-fixture", Version: "7.24.6", DeviceID: "fixture-device", TransportIdentitySHA256: strings.Repeat("a", 64)}
}

func TestPrivateSocketStatusAndUnverifiedStageCannotWrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	runtime := &blockedRuntime{target: cliTarget()}
	supervisor, err := control.Open(control.Config{Dir: dir, Target: runtime.target}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(dir, "control.sock")
	done := make(chan error, 1)
	go func() { done <- serveSocket(ctx, path, supervisor) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Lstat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket not ready")
		}
		time.Sleep(time.Millisecond)
	}
	var output bytes.Buffer
	if err = run(ctx, []string{"status", "--socket", path}, &output); err != nil {
		t.Fatal(err)
	}
	var status response
	if json.Unmarshal(output.Bytes(), &status) != nil || !status.OK || status.Status.Phase != control.Idle {
		t.Fatal("status not returned")
	}
	router := routing.Router{ID: runtime.target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "192.0.2.1"}
	plan, err := routeros.BuildPlan(runtime.target, router, nil, routeros.Snapshot{Target: runtime.target, Owner: routeros.OwnerName(runtime.target.RouterID), NamespaceEmpty: true}, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	body := stageWire{Plan: plan, KnownState: routeros.KnownState{Target: runtime.target}, TimeoutSeconds: 30, ExclusiveAccessAcknowledged: true, Credentials: map[string]credentialWire{"unused": {Revision: strings.Repeat("a", 22), Password: "fixture-secret-must-not-echo"}}}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "stage.json")
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err = run(ctx, []string{"stage", "--socket", path, "--file", file}, &output); err == nil {
		t.Fatal("unverified live gates accepted")
	}
	if json.Unmarshal(output.Bytes(), &status) != nil || status.OK || status.Error != "native-adapter-gates-unverified" || runtime.writes != 0 || bytes.Contains(output.Bytes(), []byte("fixture-secret")) {
		t.Fatal("gate or response secrecy failed", output.String())
	}
	// Unknown target/path fields never reach the supervisor.
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(time.Second))
	conn.Write([]byte(`{"version":1,"action":"status","host":"another-router"}`))
	conn.CloseWrite()
	var b [1]byte
	n, _ := conn.Read(b[:])
	conn.Close()
	if n != 0 {
		t.Fatal("unknown command field accepted")
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPrivateSocketReviewReturnsActualManifestWithoutWriteAuthority(t *testing.T) {
	dir, err := os.MkdirTemp("", "routeros-review-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	runtime := &readonlyReviewRuntime{&blockedRuntime{target: cliTarget()}}
	supervisor, err := control.Open(control.Config{Dir: dir, Target: runtime.target}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(dir, "control.sock")
	done := make(chan error, 1)
	go func() { done <- serveSocket(ctx, path, supervisor) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatal("socket server stopped", err)
		default:
		}
		if _, err := os.Lstat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket not ready")
		}
		time.Sleep(time.Millisecond)
	}
	var output bytes.Buffer
	if err := run(ctx, []string{"review", "--socket", path}, &output); err != nil {
		t.Fatal(err)
	}
	var result response
	if json.Unmarshal(output.Bytes(), &result) != nil || !result.OK || result.Review == nil || !result.Review.Snapshot.NamespaceEmpty || result.Review.KnownState.Target != runtime.target || result.Review.ObservedAt.IsZero() || runtime.writes != 0 {
		t.Fatal("read-only planning result was missing or changed authority")
	}
	file := filepath.Join(dir, "review.json")
	output.Reset()
	if err := run(ctx, []string{"review", "--socket", path, "--output", file}, &output); err != nil {
		t.Fatal(err)
	}
	var saved response
	if json.Unmarshal(output.Bytes(), &saved) != nil || !saved.OK || !saved.ReviewSaved || saved.Review != nil {
		t.Fatal("saved review response was not summarized")
	}
	private, err := privateRead(file)
	if err != nil || strictDecode(private, &saved) != nil || saved.Review == nil {
		t.Fatal("private review file was not reusable planning input", err)
	}
	if err := run(ctx, []string{"review", "--socket", path, "--output", file}, &bytes.Buffer{}); err == nil {
		t.Fatal("review replaced an existing planning record")
	}
	if unchanged, err := privateRead(file); err != nil || !bytes.Equal(private, unchanged) || runtime.writes != 0 {
		t.Fatal("failed review publication changed state or existing file", err)
	}
	if validRequest(request{Version: 1, Action: "review", Stage: &stageWire{}}) || validRequest(request{Version: 1, Action: "review", Checksum: strings.Repeat("a", 64)}) {
		t.Fatal("review accepted mutation input")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCLIPrivateInputAndActionBoundaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.json")
	if err := os.WriteFile(path, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := privateRead(path); err == nil {
		t.Fatal("public stage credential input accepted")
	}
	os.Chmod(path, 0600)
	link := filepath.Join(dir, "link.json")
	os.Symlink(path, link)
	if _, err := privateRead(link); err == nil {
		t.Fatal("symlink input accepted")
	}
	for _, args := range [][]string{{"capabilities", "--apply"}, {"status", "--config", path}, {"confirm", "--socket", path, "--id", "x"}, {"apply", "--socket", path}, {"stage", "--socket", path}, {"serve", "--config", path, "--state", dir, "--file", path}} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid command accepted", args[0])
		}
	}
	secret := credentialWire{Revision: strings.Repeat("a", 22), Password: "do-not-log-fixture"}
	if strings.Contains(fmt.Sprintf("%+v %#v", secret, stageWire{Credentials: map[string]credentialWire{"p": secret}}), secret.Password) {
		t.Fatal("private request formatting exposed credentials")
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"capabilities"}, &output); err != nil || !json.Valid(output.Bytes()) {
		t.Fatal("capability contract unavailable")
	}
}
