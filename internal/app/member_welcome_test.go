package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/notify"
)

const welcomeTestCreated = "2026-01-01T00:00:00Z"

func welcomeTestFixture() (*App, State) {
	s := networkFixture()
	s.Settings["asn"], s.Settings["supportEmail"], s.Settings["website"] = float64(64500), "support@ixp.test", "https://ixp.test"
	s.Settings["smtpPassword"] = "SETTINGS-SECRET"
	m := s.Resources["members"][0]
	m["email"], m["nocEmail"], m["phone"], m["nocHours"] = "peering@one.test", "NOC@one.test", "+1 555 0100", "24x7"
	m["billingNotes"] = "BILLING-SECRET"
	s.Resources["members"][1]["name"] = "OTHER-TENANT-SECRET"
	s.Resources["racks"] = []Record{{"id": "rack1", "name": "Rack A", "locationId": "l1"}}
	s.Resources["switches"][0]["rackId"] = "rack1"
	s.Resources["switches"][0]["serialNumber"] = "SERIAL-SECRET"
	s.Resources["users"] = []Record{
		{"id": "u1", "name": "Member admin", "email": "admin@one.test", "role": "member", "memberIds": []any{"m1"}, "memberAdminIds": []any{"m1"}, "passwordHash": "PASSWORD-SECRET"},
		{"id": "u2", "name": "Shared current account", "email": "user@one.test", "role": "member", "memberIds": []any{"m2", "m1"}, "memberAdminIds": []any{"m2"}},
		{"id": "u3", "name": "OTHER-ACCOUNT-SECRET", "email": "OTHER-ACCOUNT@secret.test", "role": "member", "memberIds": []any{"m2"}},
		{"id": "staff", "name": "STAFF-SECRET", "email": "STAFF@secret.test", "role": "admin", "memberIds": []any{"m1"}},
		{"id": "detached", "name": "DETACHED-SECRET", "email": "DETACHED@secret.test", "role": "member", "memberId": "m1", "memberIds": []any{}, membershipDetachedField: true},
	}
	s.Security = map[string]SecurityUser{"u1": {Secret: "MFA-SECRET", Generation: 3}}
	s.Resources["contacts"] = []Record{
		{"id": "contact1", "name": "Technical contact", "email": "contact@one.test", "memberId": "m1", "notes": "CONTACT-NOTES-SECRET"},
		{"id": "duplicate-contact", "name": "Duplicate address", "email": "ADMIN@one.test", "memberId": "m1"},
		{"id": "contact-other", "name": "OTHER-CONTACT-SECRET", "email": "OTHER-CONTACT@secret.test", "memberId": "m2"},
	}
	one := lagConnection()
	one["linkMode"] = "lag"
	one["dnsName4"] = "ONE.example.test"
	one["password"] = "BGP-SECRET"
	two := lagConnection()
	two["id"], two["vlanId"], two["ipv4"], two["linkMode"] = "c2", "v2", "198.51.100.10", "lag"
	delete(two, "ipv6")
	s.Resources["connections"] = []Record{one, two,
		{"id": "c3", "name": "Single member LAG", "memberId": "m1", "portId": "p3", "linkMode": "lag", "vlanId": "v1", "ipv4": "192.0.2.11", "status": "pending"},
		{"id": "other-connection", "name": "OTHER-CONNECTION-SECRET", "memberId": "m2", "portId": "p4", "vlanId": "v2", "ipv4": "198.51.100.20", "status": "disabled"},
	}
	s.Resources["ip-addresses"] = []Record{
		{"id": "pool4", "name": "Assigned IPv4", "memberId": "m1", "vlanId": "v1", "address": "192.0.2.10", "dnsName": "one.example.test"},
		{"id": "pool6", "name": "Assigned IPv6", "memberId": "m1", "vlanId": "v1", "address": "2001:db8::10", "dnsName": "one-v6.example.test"},
		{"id": "other-pool", "name": "OTHER-POOL-SECRET", "memberId": "m2", "vlanId": "v2", "address": "198.51.100.44", "dnsName": "OTHER-DNS.secret.test"},
	}
	for _, rows := range s.Resources {
		for _, row := range rows {
			row["createdAt"] = welcomeTestCreated
		}
	}
	a := &App{store: &Store{securityKey: bytes.Repeat([]byte{7}, 32)}, mailConfig: notify.Config{Enabled: true, From: "sender@ixp.test", BaseURL: "https://portal.ixp.test"}}
	return a, s
}
func welcomeTestContext(t *testing.T, a *App, s *State) memberWelcomeContext {
	t.Helper()
	c, err := a.buildMemberWelcomeContext(s, "m1")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMemberWelcomeProjectionRealAssignmentsAndIsolation(t *testing.T) {
	a, s := welcomeTestFixture()
	before, _ := json.Marshal(s)
	c := welcomeTestContext(t, a, &s)
	if c.MemberID != "m1" || c.MemberName != "First member" || !c.DeliveryEnabled || c.From != "sender@ixp.test" || len(c.BlockingReasons) != 0 || len(c.Draft.Revision) != 64 {
		t.Fatal("wrong context envelope")
	}
	if !reflect.DeepEqual(c.Draft.To, []string{"noc@one.test"}) || !reflect.DeepEqual(c.Draft.Cc, []string{"admin@one.test", "user@one.test"}) || !reflect.DeepEqual(c.Draft.Bcc, []string{"support@ixp.test"}) {
		t.Fatal("wrong scoped default recipients", c.Draft.To, c.Draft.Cc, c.Draft.Bcc)
	}
	for _, text := range []string{"AS64501", "AS64500", "物理速率合计：35000 Mbps", "10000 Mbps", "25000 Mbps", "单成员 LAG", "物理速率合计：未知", "sw-a / xe-1", "sw-b / xe-2", "机房：DC", "机柜：Rack A", "VLAN：LAN1", "登记编号：100", "192.0.2.10/24", "255.255.255.0", "2001:db8::10/64", "198.51.100.10/24", "one.example.test", "one-v6.example.test", "Member admin <admin@one.test>", "https://portal.ixp.test/#patching", "https://portal.ixp.test/#peering", "support@ixp.test", "线上 tagging / trunk 与 duplex 未在当前资料中明确登记"} {
		if !strings.Contains(c.Draft.Text, text) {
			t.Error("missing operational evidence", text)
		}
	}
	raw, _ := json.Marshal(c)
	for _, secret := range []string{"OTHER-", "STAFF-", "DETACHED-", "SETTINGS-SECRET", "BILLING-SECRET", "SERIAL-SECRET", "PASSWORD-SECRET", "MFA-SECRET", "CONTACT-NOTES-SECRET", "BGP-SECRET", "192.0.2.250", "passwordHash"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("private or cross-tenant source leaked", secret)
		}
	}
	seen := map[string]bool{}
	for _, option := range c.RecipientOptions {
		if seen[option.Email] {
			t.Fatal("duplicate option email")
		}
		seen[option.Email] = true
		if option.Email == "admin@one.test" && option.Kind != "account" {
			t.Fatal("dedupe priority changed")
		}
	}
	after, _ := json.Marshal(s)
	if !bytes.Equal(before, after) {
		t.Fatal("read-only context changed source")
	}
}

func TestMemberWelcomeProjectionCanonicalOrdering(t *testing.T) {
	a, s := welcomeTestFixture()
	first := welcomeTestContext(t, a, &s)
	for _, rows := range s.Resources {
		slices.Reverse(rows)
	}
	for _, connection := range s.Resources["connections"] {
		if ports, ok := connection["portIds"].([]any); ok {
			slices.Reverse(ports)
		}
	}
	second := welcomeTestContext(t, a, &s)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("record/physical-port order changed context or revision")
	}
	find(&s, "users", "u1")["passwordHash"] = "CHANGED-SECRET"
	s.Security["u1"] = SecurityUser{Secret: "CHANGED-MFA", Generation: 99}
	find(&s, "contacts", "contact1")["notes"] = "CHANGED-CONTACT-SECRET"
	find(&s, "members", "m1")["billingNotes"] = "CHANGED-BILLING-SECRET"
	find(&s, "members", "m2")["name"] = "CHANGED-OTHER-TENANT"
	find(&s, "switches", "sw1")["serialNumber"] = "CHANGED-SERIAL"
	third := welcomeTestContext(t, a, &s)
	if !reflect.DeepEqual(second, third) {
		t.Fatal("unselected secrets/other-tenant data entered context binding")
	}
}

func TestMemberWelcomeProjectionBindsCurrentEvidence(t *testing.T) {
	for name, mutate := range map[string]func(*App, *State){
		"member-incarnation":              func(_ *App, s *State) { find(s, "members", "m1")["createdAt"] = "2026-02-01T00:00:00Z" },
		"member-address":                  func(_ *App, s *State) { find(s, "members", "m1")["nocEmail"] = "new@one.test" },
		"account-incarnation":             func(_ *App, s *State) { find(s, "users", "u1")["createdAt"] = "2026-02-01T00:00:00Z" },
		"account-removed":                 func(_ *App, s *State) { find(s, "users", "u1")["memberIds"] = []any{"m2"} },
		"account-email":                   func(_ *App, s *State) { find(s, "users", "u1")["email"] = "changed@one.test" },
		"admin-role":                      func(_ *App, s *State) { find(s, "users", "u2")["memberAdminIds"] = []any{"m1"} },
		"contact-incarnation":             func(_ *App, s *State) { find(s, "contacts", "contact1")["createdAt"] = "2026-02-01T00:00:00Z" },
		"deduplicated-source-incarnation": func(_ *App, s *State) { find(s, "contacts", "duplicate-contact")["createdAt"] = "2026-02-01T00:00:00Z" },
		"connection-incarnation":          func(_ *App, s *State) { find(s, "connections", "c1")["createdAt"] = "2026-02-01T00:00:00Z" },
		"connection-binding":              func(_ *App, s *State) { find(s, "connections", "c1")["bindingSince"] = "2026-02-01T00:00:00Z" },
		"connection-status":               func(_ *App, s *State) { find(s, "connections", "c1")["status"] = "pending" },
		"port-incarnation":                func(_ *App, s *State) { find(s, "ports", "p1")["createdAt"] = "2026-02-01T00:00:00Z" },
		"switch-incarnation":              func(_ *App, s *State) { find(s, "switches", "sw1")["createdAt"] = "2026-02-01T00:00:00Z" },
		"location-incarnation":            func(_ *App, s *State) { find(s, "locations", "l1")["createdAt"] = "2026-02-01T00:00:00Z" },
		"rack-incarnation":                func(_ *App, s *State) { find(s, "racks", "rack1")["createdAt"] = "2026-02-01T00:00:00Z" },
		"vlan-incarnation":                func(_ *App, s *State) { find(s, "vlans", "v1")["createdAt"] = "2026-02-01T00:00:00Z" },
		"pool-incarnation":                func(_ *App, s *State) { find(s, "ip-addresses", "pool6")["createdAt"] = "2026-02-01T00:00:00Z" },
		"pool-dns":                        func(_ *App, s *State) { find(s, "ip-addresses", "pool6")["dnsName"] = "new-v6.one.test" },
		"support":                         func(_ *App, s *State) { s.Settings["supportEmail"] = "new-support@ixp.test" },
		"ixp-identity":                    func(_ *App, s *State) { s.Settings["name"] = "Changed IXP" },
		"mail-origin":                     func(a *App, _ *State) { a.mailConfig.BaseURL = "https://new-portal.ixp.test" },
		"sender":                          func(a *App, _ *State) { a.mailConfig.From = "new-sender@ixp.test" },
	} {
		t.Run(name, func(t *testing.T) {
			a, s := welcomeTestFixture()
			before := welcomeTestContext(t, a, &s)
			mutate(a, &s)
			after := welcomeTestContext(t, a, &s)
			if after.Draft.Revision == before.Draft.Revision {
				t.Fatal("changed current evidence retained old review")
			}
		})
	}
}

func TestMemberWelcomeProjectionEligibilityAndEditableDefaults(t *testing.T) {
	for _, status := range []string{"active", "pending", "suspended", "closed"} {
		t.Run(status, func(t *testing.T) {
			a, s := welcomeTestFixture()
			find(&s, "members", "m1")["status"] = status
			c := welcomeTestContext(t, a, &s)
			blocked := slices.Contains(c.BlockingReasons, "member-state-ineligible")
			if blocked != (status == "suspended" || status == "closed") {
				t.Fatal("wrong member state eligibility", c.BlockingReasons)
			}
		})
	}
	a, s := welcomeTestFixture()
	a.mailConfig.Enabled = false
	c := welcomeTestContext(t, a, &s)
	if c.DeliveryEnabled || len(c.BlockingReasons) != 0 || c.Draft.Text == "" {
		t.Fatal("SMTP disabled prevented preview")
	}
	find(&s, "members", "m1")["createdAt"] = ""
	c = welcomeTestContext(t, a, &s)
	if !slices.Contains(c.BlockingReasons, "member-entity-unproven") {
		t.Fatal("unknown member incarnation could be reviewed")
	}
	a, s = welcomeTestFixture()
	m := find(&s, "members", "m1")
	m["nocEmail"] = "invalid"
	c = welcomeTestContext(t, a, &s)
	if !reflect.DeepEqual(c.Draft.To, []string{"peering@one.test"}) {
		t.Fatal("NOC fallback wrong")
	}
	m["email"] = ""
	c = welcomeTestContext(t, a, &s)
	if len(c.Draft.To) != 0 || len(c.BlockingReasons) != 0 {
		t.Fatal("scoped account/contact selection became permanently blocked")
	}
	s.Resources["users"], s.Resources["contacts"] = nil, nil
	c = welcomeTestContext(t, a, &s)
	if !slices.Contains(c.BlockingReasons, "member-recipient-unavailable") || len(c.RecipientOptions) != 1 || c.RecipientOptions[0].Kind != "operations" {
		t.Fatal("support alone became eligible onboarding recipient")
	}
	a, s = welcomeTestFixture()
	s.Settings["supportEmail"] = "NOC@one.test"
	c = welcomeTestContext(t, a, &s)
	if len(c.Draft.Bcc) != 0 || len(c.Draft.To) != 1 {
		t.Fatal("same recipient repeated across headers")
	}
	for index := 0; index < 20; index++ {
		s.Resources["users"] = append(s.Resources["users"], Record{"id": fmt.Sprintf("extra-%02d", index), "createdAt": welcomeTestCreated, "name": "Account", "email": fmt.Sprintf("extra%02d@one.test", index), "role": "member", "memberIds": []any{"m1"}})
	}
	c = welcomeTestContext(t, a, &s)
	if len(c.Draft.To)+len(c.Draft.Cc)+len(c.Draft.Bcc) > notify.MaxRecipients || len(c.BlockingReasons) != 0 || !strings.Contains(c.Draft.Text, "并非向全部关联账号发送") || len(c.RecipientOptions) < 20 {
		t.Fatal("large member roster silently truncated or blocked editing")
	}
}

func TestMemberWelcomeProjectionRejectsUnprovenScopedTopology(t *testing.T) {
	for name, mutate := range map[string]func(*State){
		"foreign-port":                   func(s *State) { find(s, "ports", "p1")["memberId"] = "m2" },
		"foreign-conflicting-connection": func(s *State) { find(s, "connections", "other-connection")["portId"] = "p1" },
		"foreign-pool":                   func(s *State) { find(s, "ip-addresses", "pool4")["memberId"] = "m2" },
		"pool-conflict":                  func(s *State) { find(s, "ip-addresses", "pool4")["dnsName"] = "wrong.example.test" },
		"duplicate-pool": func(s *State) {
			p := welcomeFields(find(s, "ip-addresses", "pool4"), "id createdAt name memberId vlanId address dnsName")
			p["id"] = "duplicate-pool"
			s.Resources["ip-addresses"] = append(s.Resources["ip-addresses"], p)
		},
		"unknown-port-entity": func(s *State) { delete(find(s, "ports", "p1"), "createdAt") },
		"missing-switch":      func(s *State) { find(s, "ports", "p1")["switchId"] = "missing" },
		"bad-switch-location": func(s *State) { find(s, "switches", "sw1")["locationId"] = "missing" },
		"bad-address":         func(s *State) { find(s, "connections", "c1")["ipv4"] = "203.0.113.1" },
		"bad-prefix":          func(s *State) { find(s, "vlans", "v1")["ipv4Prefix"] = "INVALID-SECRET" },
		"bad-name":            func(s *State) { find(s, "members", "m1")["name"] = "name\nPRIVATE-INJECTION" },
		"bad-dns":             func(s *State) { find(s, "connections", "c1")["dnsName4"] = "PRIVATE-INJECTION; run" },
	} {
		t.Run(name, func(t *testing.T) {
			a, s := welcomeTestFixture()
			mutate(&s)
			_, err := a.buildMemberWelcomeContext(&s, "m1")
			if err == nil {
				t.Fatal("unproven scoped topology accepted")
			}
			if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "PRIVATE-INJECTION") {
				t.Fatal("raw source diagnostics escaped")
			}
		})
	}
}

func TestMemberWelcomeProjectionAddressEvidenceAndNoTemplateInference(t *testing.T) {
	a, s := welcomeTestFixture()
	v := find(&s, "vlans", "v1")
	v["prefixes"] = []any{"192.0.2.0/25"}
	c := welcomeTestContext(t, a, &s)
	if !strings.Contains(c.Draft.Text, "IPv4：192.0.2.10（前缀长度未能唯一确定）") {
		t.Fatal("ambiguous mask silently selected")
	}
	find(&s, "connections", "c1")["trunk"] = true
	find(&s, "ports", "p1")["duplex"] = "full"
	s.Settings["website"] = "https://user:URL-SECRET@private.test/"
	a.mailConfig.BaseURL = "javascript:URL-SECRET"
	c = welcomeTestContext(t, a, &s)
	if strings.Contains(c.Draft.Text, "URL-SECRET") || strings.Contains(c.Draft.Text, "全双工") || strings.Contains(c.Draft.Text, "线上 tagging：启用") {
		t.Fatal("unsupported/private template data inferred")
	}
	if !strings.Contains(c.Draft.Text, "门户地址未配置") {
		t.Fatal("unsafe portal URL treated as usable")
	}
	s.Resources["connections"] = nil
	c = welcomeTestContext(t, a, &s)
	if !strings.Contains(c.Draft.Text, "尚无已登记连接") {
		t.Fatal("empty member presented as connected")
	}
}

func TestMemberWelcomeProjectionBounds(t *testing.T) {
	a, s := welcomeTestFixture()
	s.Settings["name"] = strings.Repeat("交", 66)
	find(&s, "members", "m1")["name"] = strings.Repeat("会", 66)
	c := welcomeTestContext(t, a, &s)
	if len(c.Draft.Subject) > 300 || c.Draft.Subject != "成员入网运营资料" || !strings.Contains(c.Draft.Text, str(s.Settings, "name")) {
		t.Fatal("long source names made default subject unsendable or disappeared")
	}
	a, s = welcomeTestFixture()
	s.Resources["connections"] = nil
	for index := 0; index <= memberWelcomeConnectionLimit; index++ {
		s.Resources["connections"] = append(s.Resources["connections"], Record{"id": fmt.Sprintf("c-%03d", index), "memberId": "m1"})
	}
	if _, err := a.buildMemberWelcomeContext(&s, "m1"); err == nil || !strings.Contains(err.Error(), "100") {
		t.Fatal("scoped connection bound not enforced", err)
	}
	a, s = welcomeTestFixture()
	s.Resources["contacts"] = nil
	for index := 0; index <= memberWelcomePeopleLimit; index++ {
		s.Resources["contacts"] = append(s.Resources["contacts"], Record{"id": fmt.Sprintf("contact-%03d", index), "createdAt": welcomeTestCreated, "memberId": "m1", "name": "Contact", "email": fmt.Sprintf("c%d@one.test", index)})
	}
	if _, err := a.buildMemberWelcomeContext(&s, "m1"); err == nil {
		t.Fatal("unbounded contacts accepted")
	}
	a, s = welcomeTestFixture()
	s.Resources["users"] = nil
	for index := 0; index < memberWelcomePeopleLimit; index++ {
		s.Resources["users"] = append(s.Resources["users"], Record{"id": fmt.Sprintf("account-%03d", index), "createdAt": welcomeTestCreated, "memberIds": []any{"m1"}, "memberAdminIds": []any{"m1"}, "role": "member", "name": strings.Repeat("a", 200), "email": fmt.Sprintf("u%03d@%s.%s.example.test", index, strings.Repeat("b", 60), strings.Repeat("c", 60))})
	}
	if _, err := a.buildMemberWelcomeContext(&s, "m1"); err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatal("oversized body silently truncated", err)
	}
	s.Resources["users"] = append(s.Resources["users"], Record{"id": "too-many", "createdAt": welcomeTestCreated, "role": "member", "memberId": "m1"})
	if _, err := a.buildMemberWelcomeContext(&s, "m1"); err == nil {
		t.Fatal("unbounded accounts accepted")
	}
	if _, err := a.buildMemberWelcomeContext(&s, "does-not-exist"); err == nil {
		t.Fatal("missing member accepted")
	}
}
