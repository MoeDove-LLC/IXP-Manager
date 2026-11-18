package provision_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/provision"
)

// These contracts use the public package API. Optional real-validator runs are
// executed by the verification command in a user/network namespace; no daemon,
// live monitor command, device configuration or network probe is started.
func contractOptions(kind string) provision.Options {
	return provision.Options{Kind: kind, Families: []int{4, 6}, DNS: provision.DNSOptions{
		Prefix: "192.0.2.0/24", PrimaryNS: "NS1.Example.Test.", ResponsibleMailbox: "Hostmaster.Example.Test.",
		NameServers: []string{"ns2.example.test.", "NS1.Example.Test."}, Serial: 2026110702,
	}}
}

func contractTarget(id, address string) provision.Target {
	return provision.Target{ID: id, Kind: "member", Name: "Contract network", Address: address,
		DNSName: "Member.Example.Test.", MemberID: "-member", ASN: 64501, Active: true, Monitor: true, CanPing: true,
		VLANID: "-vlan", VLANName: "Peering LAN", InfrastructureID: "-fabric", SwitchIDs: []string{"-switch"},
		RackIDs: []string{"-rack"}, LocationIDs: []string{"-location"}}
}

func contractRender(t *testing.T, in provision.Input) provision.Artifact {
	t.Helper()
	a, err := provision.Render(in)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if err := provision.ValidateArtifact(a); err != nil {
		t.Fatalf("renderer produced an invalid artifact: %v", err)
	}
	return a
}

func TestContractReverseZoneBoundaries(t *testing.T) {
	valid := map[string]string{
		"192.0.0.0/8": "192.in-addr.arpa.", "192.0.0.0/16": "0.192.in-addr.arpa.",
		"192.0.2.0/24": "2.0.192.in-addr.arpa.", "192.0.2.10/32": "10.2.0.192.in-addr.arpa.",
		"2000::/4": "2.ip6.arpa.", "2001:db8::/32": "8.b.d.0.1.0.0.2.ip6.arpa.",
		"2001:db8:1234:5678::/64":                     "8.7.6.5.4.3.2.1.8.b.d.0.1.0.0.2.ip6.arpa.",
		"1111:1111:1111:1111:1111:1111:1111:1111/128": strings.Repeat("1.", 32) + "ip6.arpa.",
	}
	for prefix, want := range valid {
		t.Run(prefix, func(t *testing.T) {
			got, err := provision.ReverseZone(prefix)
			if err != nil || got != want {
				t.Fatalf("got %q, %v; want %q", got, err, want)
			}
		})
	}
	for _, prefix := range []string{"0.0.0.0/0", "192.0.2.0/25", "192.0.2.1/24", "::/0", "2001:db8::/63", "2001:db8::1/64", "::ffff:192.0.2.0/120", "fe80::%eth0/64"} {
		t.Run("reject-"+prefix, func(t *testing.T) {
			if _, err := provision.ReverseZone(prefix); err == nil {
				t.Fatal("accepted a noncanonical, partial, or mapped reverse zone")
			}
		})
	}
}

func TestContractDNSNamesAndOptionsCannotInjectOrMutateInputs(t *testing.T) {
	for _, name := range []string{"a.example.\n$INCLUDE /tmp/other", "a.example.;@ IN A 127.0.0.1", "a\x00.example", "a..example", "-bad.example", "bad-.example", "192.0.2.1", "münchen.example", strings.Repeat("a", 64) + ".example"} {
		if _, err := provision.SafeFQDN(name); err == nil {
			t.Errorf("accepted unsafe DNS name %q", name)
		}
	}
	if name, err := provision.SafeFQDN("XN--MNCHEN-3YA.Example."); err != nil || name != "xn--mnchen-3ya.example." {
		t.Fatalf("punycode canonicalization: %q %v", name, err)
	}
	o := contractOptions("dns-arpa")
	o.Families = []int{6, 4}
	beforeFamilies := append([]int(nil), o.Families...)
	beforeNS := append([]string(nil), o.DNS.NameServers...)
	normalized, err := provision.NormalizeOptions(o)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(o.Families, beforeFamilies) || !reflect.DeepEqual(o.DNS.NameServers, beforeNS) {
		t.Fatal("normalization mutated caller slices")
	}
	if !reflect.DeepEqual(normalized.Families, []int{4, 6}) || normalized.DNS.PrimaryNS != "ns1.example.test." {
		t.Fatal("normalization did not preserve canonical DNS options")
	}
	for _, mutate := range []func(*provision.Options){
		func(o *provision.Options) { o.DNS.Serial = 0 }, func(o *provision.Options) { o.DNS.PrimaryNS = "ns.2.0.192.in-addr.arpa." },
		func(o *provision.Options) { o.Families = []int{6} }, func(o *provision.Options) { o.DNS.Expire = 1 },
		func(o *provision.Options) { o.DNS.NameServers = []string{"ns.example.\n$GENERATE 1-9"} },
	} {
		bad := contractOptions("dns-arpa")
		mutate(&bad)
		if _, err := provision.NormalizeOptions(bad); err == nil {
			t.Error("accepted invalid DNS serial, glue, family, timer or name options")
		}
	}
}

func TestContractDNSCoverageIsIndependentOfMonitoring(t *testing.T) {
	a := contractTarget("-active", "192.0.2.10")
	a.Monitor = false
	a.CanPing = false
	b := contractTarget("-inactive", "192.0.2.11")
	b.Active = false
	b.DNSName = "inactive.example.test."
	c := contractTarget("-missing", "192.0.2.12")
	c.DNSName = ""
	d := contractTarget("-outside", "198.51.100.10")
	in := provision.Input{Options: contractOptions("dns-arpa"), Targets: []provision.Target{d, c, b, a}}
	full := contractRender(t, in)
	if !full.Complete || full.ValidationLevel != "native" || full.TargetCount != 2 {
		t.Fatalf("DNS coverage/claim: %+v", full)
	}
	for _, line := range []string{"10.2.0.192.in-addr.arpa. IN PTR member.example.test.", "11.2.0.192.in-addr.arpa. IN PTR inactive.example.test.", "2026110702 ; serial"} {
		if !strings.Contains(full.Content, line) {
			t.Errorf("missing frozen DNS record %q", line)
		}
	}
	missing := false
	for _, notice := range full.Notices {
		if notice.Code == "missing-dns-name" && notice.TargetID == c.ID {
			missing = true
		}
	}
	if !missing {
		t.Fatal("missing hostname was silently invented or dropped without notice")
	}
	in.Targets = []provision.Target{a, b, c, d}
	again := contractRender(t, in)
	if again.Content != full.Content {
		t.Fatal("render changed with inventory order or wall clock")
	}
	in.Options.DNS.ActiveOnly = true
	if got := contractRender(t, in).TargetCount; got != 1 {
		t.Fatalf("activeOnly generated %d PTRs", got)
	}
	in.Options.DNS.ActiveOnly = false
	duplicate := a
	duplicate.ID = "-same-address"
	in.Targets = append(in.Targets, duplicate)
	if got := contractRender(t, in).TargetCount; got != 2 {
		t.Fatal("same-address identical PTR was not deduplicated")
	}
	in.Targets[len(in.Targets)-1].DNSName = "conflict.example.test."
	if _, err := provision.Render(in); err == nil {
		t.Fatal("conflicting same-zone PTR ownership accepted")
	}
}

func TestContractDNSArtifactRejectsPartialOrOutsideOwners(t *testing.T) {
	in := provision.Input{Options: contractOptions("dns-arpa"), Targets: []provision.Target{contractTarget("target", "192.0.2.10")}}
	a := contractRender(t, in)
	for _, owner := range []string{"10.12.0.192.in-addr.arpa.", "999.2.0.192.in-addr.arpa.", "2.0.192.in-addr.arpa."} {
		bad := a
		bad.Content = strings.ReplaceAll(a.Content, "10.2.0.192.in-addr.arpa.", owner)
		if err := provision.ValidateArtifact(bad); err == nil {
			t.Errorf("accepted invalid PTR owner %s", owner)
		}
	}
	bad := a
	bad.Content += "$INCLUDE /tmp/unreviewed-zone\n"
	if err := provision.ValidateArtifact(bad); err == nil {
		t.Fatal("accepted an include directive")
	}
	in.Options.DNS.Prefix = "2001:db8::/32"
	in.Targets[0].Address = "2001:db8::1"
	v6 := contractRender(t, in)
	for _, owner := range []string{"1.8.b.d.0.1.0.0.2.ip6.arpa.", strings.Repeat("g.", 24) + "8.b.d.0.1.0.0.2.ip6.arpa."} {
		bad = v6
		lines := strings.Split(bad.Content, "\n")
		for i, line := range lines {
			if strings.Contains(line, " IN PTR ") {
				lines[i] = owner + " IN PTR member.example.test."
			}
		}
		bad.Content = strings.Join(lines, "\n")
		if err := provision.ValidateArtifact(bad); err == nil {
			t.Errorf("accepted malformed IPv6 PTR %s", owner)
		}
	}
}

func TestContractMonitorSelectionAndOpaqueIDs(t *testing.T) {
	for _, kind := range []string{"nagios", "smokeping"} {
		for _, policy := range []struct {
			name                  string
			active, monitor, ping bool
			want                  int
		}{{"enabled", true, true, true, 1}, {"inactive", false, true, true, 0}, {"excluded", true, false, true, 0}, {"no-ping", true, true, false, 0}} {
			t.Run(kind+"/"+policy.name, func(t *testing.T) {
				target := contractTarget("-target", "192.0.2.10")
				target.Active, target.Monitor, target.CanPing = policy.active, policy.monitor, policy.ping
				opts := contractOptions(kind)
				opts.VLANID = "-vlan"
				opts.InfrastructureID = "-fabric"
				a := contractRender(t, provision.Input{Options: opts, Targets: []provision.Target{target}})
				want := policy.want
				if kind == "nagios" && policy.active && policy.monitor && !policy.ping {
					want = 1
					if !strings.Contains(a.Content, "active_checks_enabled 0") || !strings.Contains(a.Content, "check_command null") || strings.Contains(a.Content, "service_description PING") {
						t.Fatal("CanPing=false still schedules active ping checks")
					}
				}
				if a.TargetCount != want || a.Complete || a.ValidationLevel != "structural" {
					t.Fatalf("unexpected monitoring selection or completeness: count=%d want=%d complete=%v level=%s", a.TargetCount, want, a.Complete, a.ValidationLevel)
				}
				validation, err := provision.Validate(context.Background(), a, provision.ValidatorOptions{Nagios: "/not-executed/nagios", Smokeping: "/not-executed/smokeping", MainConfig: "/unrelated/main.conf"})
				if err != nil || validation.Level != "structural" {
					t.Fatalf("snippet validation claimed target-native success: %+v %v", validation, err)
				}
			})
		}
	}
	for _, kind := range []string{"nagios", "smokeping"} {
		t.Run(kind+"/scopes-and-families", func(t *testing.T) {
			a := contractTarget("-v4", "192.0.2.10")
			b := contractTarget("-v6", "2001:db8::10")
			other := contractTarget("-other", "192.0.2.11")
			other.VLANID = "other-vlan"
			fabric := contractTarget("-fabric-other", "192.0.2.12")
			fabric.InfrastructureID = "other-fabric"
			sw := contractTarget("-switch-target", "192.0.2.250")
			sw.Kind = "switch"
			sw.SwitchID = "-switch"
			sw.VLANID = ""
			sw.MemberID = ""
			unrelated := sw
			unrelated.ID = "unrelated-switch"
			unrelated.SwitchID = "unrelated-switch"
			unrelated.Address = "192.0.2.251"
			opts := contractOptions(kind)
			opts.Families = []int{4}
			opts.VLANID = "-vlan"
			opts.InfrastructureID = "-fabric"
			in := provision.Input{Options: opts, Targets: []provision.Target{a, b, other, fabric, sw, unrelated}}
			result := contractRender(t, in)
			if result.TargetCount != 2 || !strings.Contains(result.Content, "192.0.2.250") || strings.Contains(result.Content, "192.0.2.251") || strings.Contains(result.Content, "192.0.2.11") || strings.Contains(result.Content, "2001:db8::10") {
				t.Fatal("scope/family selection did not preserve the referenced switch while excluding other tenants")
			}
			in.Options.Families = []int{6}
			v6 := contractRender(t, in)
			if v6.TargetCount != 1 || !strings.Contains(v6.Content, "2001:db8::10") || strings.Contains(v6.Content, "192.0.2.250") {
				t.Fatal("IPv6 selection included the wrong family")
			}
		})
	}
}

func TestContractMonitoringRejectsAmbiguousAddressAcrossFabrics(t *testing.T) {
	for _, kind := range []string{"nagios", "smokeping"} {
		a := contractTarget("-a", "192.0.2.10")
		b := a
		b.ID = "-b"
		b.InfrastructureID = "second-fabric"
		b.VLANID = "second-vlan"
		b.MemberID = "second-member"
		in := provision.Input{Options: contractOptions(kind), Targets: []provision.Target{a, b}}
		if _, err := provision.Render(in); err == nil {
			t.Errorf("%s accepted ambiguous cross-fabric probing", kind)
		}
		in.Options.InfrastructureID = "-fabric"
		if count := contractRender(t, in).TargetCount; count != 1 {
			t.Fatalf("explicit fabric scope produced %d targets", count)
		}
	}
}

func TestContractLegacyLabelsNeverBecomeDirectives(t *testing.T) {
	legacy := "萌鸽 #;\\$`{}|&<>\"'=@"
	sanitized := "萌鸽 ＃；＼＄｀｛｝｜＆＜＞＂＇＝＠"
	for _, kind := range []string{"nagios", "smokeping"} {
		in := provision.Input{Options: contractOptions(kind), Targets: []provision.Target{contractTarget("-target", "192.0.2.10")}}
		in.Targets[0].Name = legacy
		a := contractRender(t, in)
		if !strings.Contains(a.Content, sanitized) || strings.Contains(a.Content, legacy) {
			t.Errorf("%s did not preserve safe legacy label", kind)
		}
		for _, bad := range []string{"name\ndefine command {", "name\rhost = 127.0.0.1", "name\u2028binary = /bin/sh", "name\u200bhidden"} {
			in.Targets[0].Name = bad
			if _, err := provision.Render(in); err == nil {
				t.Errorf("%s accepted a control-line label", kind)
			}
		}
		for _, bad := range []string{"/bin/sh", "check!argument", "name;command", "name\ncommand"} {
			options := contractOptions(kind)
			if kind == "nagios" {
				options.Nagios.ServiceTemplate = bad
			} else {
				options.Smokeping.Probe4 = bad
			}
			if _, err := provision.NormalizeOptions(options); err == nil {
				t.Errorf("%s accepted a command/path as an identifier", kind)
			}
		}
		bad := a
		bad.Complete = true
		if err := provision.ValidateArtifact(bad); err == nil {
			t.Errorf("%s accepted an include as a complete service config", kind)
		}
	}
}

func TestContractDNSFailsClosedWithoutChecker(t *testing.T) {
	a := contractRender(t, provision.Input{Options: contractOptions("dns-arpa"), Targets: []provision.Target{contractTarget("-target", "192.0.2.10")}})
	result, err := provision.Validate(context.Background(), a, provision.ValidatorOptions{NamedCheckzone: filepath.Join(t.TempDir(), "not-installed")})
	if err == nil || result.Level == "native" {
		t.Fatal("missing named-checkzone was reported as native validation success")
	}
}

func TestContractNativeBINDCompleteZones(t *testing.T) {
	binary := os.Getenv("NAMED_CHECKZONE_BIN")
	if binary == "" {
		t.Skip("set NAMED_CHECKZONE_BIN to run real offline BIND validation")
	}
	for _, family := range []int{4, 6} {
		t.Run(fmt.Sprint(family), func(t *testing.T) {
			opts := contractOptions("dns-arpa")
			target := contractTarget("-native", "192.0.2.10")
			if family == 6 {
				opts.DNS.Prefix = "2001:db8::/32"
				target.Address = "2001:db8::10"
			}
			a := contractRender(t, provision.Input{Options: opts, Targets: []provision.Target{target}})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			v, err := provision.Validate(ctx, a, provision.ValidatorOptions{NamedCheckzone: binary})
			if err != nil || v.Level != "native" {
				t.Fatalf("real named-checkzone rejected generated zone: %+v %v", v, err)
			}
			bad := filepath.Join(t.TempDir(), "invalid.zone")
			if err := os.WriteFile(bad, []byte(a.Content+"@ IN A 999.0.0.1\n"), 0600); err != nil {
				t.Fatal(err)
			}
			output, err := exec.CommandContext(ctx, binary, "-i", "local", a.Zone, bad).CombinedOutput()
			if err == nil || ctx.Err() != nil {
				t.Fatalf("real BIND did not reject invalid address record: %v %s", err, output)
			}
		})
	}
}

func TestContractNativeNagiosCompleteFixture(t *testing.T) {
	binary := os.Getenv("NAGIOS_BIN")
	if binary == "" {
		t.Skip("set NAGIOS_BIN to run real offline Nagios -v validation")
	}
	opts, err := provision.NormalizeOptions(contractOptions("nagios"))
	if err != nil {
		t.Fatal(err)
	}
	member := contractTarget("-native-member", "192.0.2.10")
	member.Name = "萌鸽 #;\\$`{}|&<>\"'=@"
	member.Busy = true
	v6 := contractTarget("-native-v6", "2001:db8::10")
	sw := contractTarget("-native-switch", "192.0.2.250")
	sw.Kind = "switch"
	sw.SwitchID = "-switch"
	sw.Model = "Model # 1"
	sw.VendorID = "-vendor"
	router := contractTarget("-native-router", "192.0.2.1")
	router.Kind = "router"
	router.RouterID = "-router"
	a := contractRender(t, provision.Input{Options: opts, Targets: []provision.Target{member, v6, sw, router}, Sessions: []provision.Session{{ID: "-peer", TargetID: member.ID, RouterID: "-router", RouterRole: "route-collector", Enabled: true, Monitor: true}}, CoreBundles: []provision.CoreBundle{{ID: "-bundle", TargetID: sw.ID, Enabled: true}}})
	if a.Complete || a.ValidationLevel != "structural" {
		t.Fatal("snippet was mislabeled before building a target fixture")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "must-not-run")
	script := filepath.Join(dir, "check-fixture.sh")
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("check-fixture.sh", "#!/bin/sh\nprintf executed > '"+marker+"'\nexit 0\n")
	write("generated.cfg", a.Content)
	var templates strings.Builder
	commands := []string{"null", "contract_notify", "check_moedove_ping4", "check_moedove_ping6", "check_moedove_ping_busy4", "check_moedove_ping_busy6", "check_moedove_bgp", "check_moedove_router", "check_moedove_switch", "check_moedove_core"}
	commandBlock := func(name string) string {
		return fmt.Sprintf("define command {\n command_name %s\n command_line /bin/sh %s\n}\n", name, script)
	}
	for _, name := range commands {
		templates.WriteString(commandBlock(name))
	}
	templates.WriteString("define timeperiod {\n timeperiod_name contract_24x7\n alias Contract time\n sunday 00:00-24:00\n monday 00:00-24:00\n tuesday 00:00-24:00\n wednesday 00:00-24:00\n thursday 00:00-24:00\n friday 00:00-24:00\n saturday 00:00-24:00\n}\n")
	templates.WriteString("define contact {\n contact_name contract-fixture\n alias Disabled fixture contact\n host_notifications_enabled 0\n service_notifications_enabled 0\n host_notification_period contract_24x7\n service_notification_period contract_24x7\n host_notification_options n\n service_notification_options n\n host_notification_commands contract_notify\n service_notification_commands contract_notify\n}\n")
	for _, name := range []string{opts.Nagios.MemberHostTemplate, opts.Nagios.SwitchHostTemplate, opts.Nagios.RouterHostTemplate} {
		fmt.Fprintf(&templates, "define host {\n name %s\n register 0\n max_check_attempts 1\n check_interval 5\n retry_interval 1\n check_period contract_24x7\n notification_period contract_24x7\n notification_interval 0\n contacts contract-fixture\n notifications_enabled 0\n active_checks_enabled 0\n}\n", name)
	}
	for _, name := range []string{opts.Nagios.ServiceTemplate, opts.Nagios.PingServiceTemplate, opts.Nagios.BusyPingServiceTemplate, opts.Nagios.BGPServiceTemplate, opts.Nagios.RouterServiceTemplate, opts.Nagios.SwitchServiceTemplate, opts.Nagios.CoreServiceTemplate} {
		fmt.Fprintf(&templates, "define service {\n name %s\n register 0\n max_check_attempts 1\n check_interval 5\n retry_interval 1\n check_period contract_24x7\n notification_period contract_24x7\n notification_interval 0\n contacts contract-fixture\n notifications_enabled 0\n active_checks_enabled 0\n}\n", name)
	}
	write("templates.cfg", templates.String())
	main := fmt.Sprintf("log_file=%s\ncfg_file=%s\ncfg_file=%s\nobject_cache_file=%s\nstatus_file=%s\ncommand_file=%s\nlock_file=%s\ntemp_file=%s\ntemp_path=%s\ncheck_external_commands=0\nexecute_host_checks=0\nexecute_service_checks=0\nenable_notifications=0\nenable_event_handlers=0\nprocess_performance_data=0\nretain_state_information=0\n", filepath.Join(dir, "nagios.log"), filepath.Join(dir, "templates.cfg"), filepath.Join(dir, "generated.cfg"), filepath.Join(dir, "objects.cache"), filepath.Join(dir, "status.dat"), filepath.Join(dir, "command.pipe"), filepath.Join(dir, "nagios.pid"), filepath.Join(dir, "nagios.tmp"), dir)
	// Use the invoking namespace's numeric identity. The unpacked Debian binary
	// defaults to service accounts that intentionally are not installed here.
	write("nagios.cfg", main+fmt.Sprintf("nagios_user=%d\nnagios_group=%d\ncheck_result_path=%s\n", os.Geteuid(), os.Getegid(), dir))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, "-v", filepath.Join(dir, "nagios.cfg")).CombinedOutput()
	if err != nil {
		t.Fatalf("real Nagios rejected full temporary fixture: %v\n%s", err, output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("Nagios validation executed a monitor command")
	}
	// A missing referenced command must be caught by the actual target parser,
	// even though the generated fragment itself remains structurally valid.
	write("templates.cfg", strings.Replace(templates.String(), commandBlock("check_moedove_bgp"), "", 1))
	output, err = exec.CommandContext(ctx, binary, "-v", filepath.Join(dir, "nagios.cfg")).CombinedOutput()
	if err == nil || ctx.Err() != nil {
		t.Fatalf("real Nagios did not reject the missing fixed BGP command: %v\n%s", err, output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("negative native validation executed a monitor command")
	}
}
