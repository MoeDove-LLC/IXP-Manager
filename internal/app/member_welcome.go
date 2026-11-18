package app

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/moedove/ixp-manager/internal/notify"
)

const memberWelcomeTextLimit = 64 << 10
const memberWelcomeConnectionLimit = 100
const memberWelcomePeopleLimit = 200

type memberWelcomeDraft struct {
	Revision string   `json:"revision"`
	To       []string `json:"to"`
	Cc       []string `json:"cc"`
	Bcc      []string `json:"bcc"`
	Subject  string   `json:"subject"`
	Text     string   `json:"text"`
}
type memberWelcomeContext struct {
	MemberID         string                 `json:"memberId"`
	MemberName       string                 `json:"memberName"`
	DeliveryEnabled  bool                   `json:"deliveryEnabled"`
	From             string                 `json:"from"`
	Draft            memberWelcomeDraft     `json:"draft"`
	RecipientOptions []patchRecipientOption `json:"recipientOptions"`
	BlockingReasons  []string               `json:"blockingReasons"`
}

type memberWelcomeText struct {
	strings.Builder
	err error
}

func (b *memberWelcomeText) line(format string, values ...any) {
	if b.err != nil {
		return
	}
	text := fmt.Sprintf(format, values...) + "\n"
	if b.Len()+len(text) > memberWelcomeTextLimit {
		b.err = errors.New("欢迎邮件资料超过 64 KiB，请先缩小登记资料范围")
		return
	}
	_, _ = b.WriteString(text)
}

// This projection never creates an account, changes membership, issues a
// password link, resolves a URL, or reads BGP/security credentials. Its revision
// binds only selected current identities, recipients and operational evidence.
func (a *App) buildMemberWelcomeContext(s *State, memberID string) (memberWelcomeContext, error) {
	c := memberWelcomeContext{MemberID: memberID, DeliveryEnabled: a.mailConfig.Enabled, From: a.mailConfig.From, Draft: memberWelcomeDraft{To: []string{}, Cc: []string{}, Bcc: []string{}}, RecipientOptions: []patchRecipientOption{}, BlockingReasons: []string{}}
	if s == nil || !identifier.MatchString(memberID) || find(s, "members", memberID) == nil {
		return c, errors.New("成员不存在")
	}
	member := find(s, "members", memberID)
	var err error
	c.MemberName, err = welcomeLabel(str(member, "name"))
	if err != nil {
		return c, err
	}
	if !validNoteEntity(noteEntity(member)) {
		c.BlockingReasons = append(c.BlockingReasons, "member-entity-unproven")
	}
	if !contains([]string{"active", "pending"}, str(member, "status")) {
		c.BlockingReasons = append(c.BlockingReasons, "member-state-ineligible")
	}
	exchange, err := welcomeLabel(str(s.Settings, "name"))
	if err != nil {
		return c, err
	}
	if exchange == "" {
		exchange = "交换中心"
	}
	if c.MemberName == "" {
		return c, errors.New("成员名称未登记")
	}
	c.Draft.Subject = exchange + " · " + c.MemberName + " · 入网运营资料"
	if len(c.Draft.Subject) > 300 {
		c.Draft.Subject = "成员入网运营资料"
	}
	evidence := map[string]Record{}
	bind := func(kind string, row Record, fields string) error {
		if row == nil || !validNoteEntity(noteEntity(row)) {
			return errors.New("成员运营资料引用的当前实体身份不完整")
		}
		evidence[kind+":"+str(row, "id")] = welcomeFields(row, "id createdAt "+fields)
		return nil
	}
	// The missing member incarnation is represented as a blocking condition so
	// the operator can still inspect the scope without obtaining a send review.
	evidence["member:"+memberID] = welcomeFields(member, "id createdAt name status type asn email nocEmail phone noc24hPhone nocHours nocWebsite website")
	seenOptions := map[string]bool{}
	add := func(id, label, email, kind string) error {
		email = welcomeMailbox(email)
		if email == "" || seenOptions[email] {
			return nil
		}
		label, e := welcomeLabel(label)
		if e != nil {
			return e
		}
		if label == "" {
			label = email
		}
		seenOptions[email] = true
		c.RecipientOptions = append(c.RecipientOptions, patchRecipientOption{ID: id, Label: label, Email: email, Kind: kind})
		return nil
	}
	if err = add("member:noc", "成员 NOC", str(member, "nocEmail"), "member"); err != nil {
		return c, err
	}
	if err = add("member:email", "成员互联邮箱", str(member, "email"), "member"); err != nil {
		return c, err
	}
	accounts := []Record{}
	for _, user := range noteSortedRecords(s, "users") {
		if str(user, "role") != "member" || !contains(membershipIDs(user), memberID) || memberAccountDetached(user) {
			continue
		}
		if len(accounts) >= memberWelcomePeopleLimit {
			return c, errors.New("成员关联账号超过 200 个，请分批整理运营资料")
		}
		if !validNoteEntity(noteEntity(user)) {
			return c, errors.New("成员账号的当前实体身份不完整")
		}
		name, e := welcomeLabel(str(user, "name"))
		if e != nil {
			return c, e
		}
		account := Record{"id": str(user, "id"), "createdAt": str(user, "createdAt"), "name": name, "email": welcomeMailbox(str(user, "email")), "role": "member", "memberId": memberID, "memberAdmin": memberAdministrator(user, memberID)}
		accounts = append(accounts, account)
		evidence["account:"+str(user, "id")] = account
		if err = add("account:"+str(user, "id"), name, str(user, "email"), "account"); err != nil {
			return c, err
		}
	}
	contacts := 0
	for _, contact := range noteSortedRecords(s, "contacts") {
		if str(contact, "memberId") != memberID {
			continue
		}
		contacts++
		if contacts > memberWelcomePeopleLimit {
			return c, errors.New("成员联系人超过 200 个，请分批整理运营资料")
		}
		if err = bind("contact", contact, "memberId name email"); err != nil {
			return c, err
		}
		if err = add("contact:"+str(contact, "id"), str(contact, "name"), str(contact, "email"), "contact"); err != nil {
			return c, err
		}
	}
	support := welcomeMailbox(str(s.Settings, "supportEmail"))
	if err = add("operations:support", "交换中心支持", support, "operations"); err != nil {
		return c, err
	}
	eligible := false
	for _, option := range c.RecipientOptions {
		if option.Kind != "operations" {
			eligible = true
		}
	}
	if !eligible {
		c.BlockingReasons = append(c.BlockingReasons, "member-recipient-unavailable")
	}
	to := welcomeMailbox(str(member, "nocEmail"))
	if to == "" {
		to = welcomeMailbox(str(member, "email"))
	}
	if to != "" {
		c.Draft.To = append(c.Draft.To, to)
	}
	usedHeaders := map[string]bool{}
	if to != "" {
		usedHeaders[to] = true
	}
	if support != "" && !usedHeaders[support] {
		c.Draft.Bcc = append(c.Draft.Bcc, support)
		usedHeaders[support] = true
	}
	// Leave a To slot when only scoped contact/account candidates are present.
	ccLimit := notify.MaxRecipients - max(1, len(c.Draft.To)) - len(c.Draft.Bcc)
	ccOmitted := false
	for _, account := range accounts {
		address := str(account, "email")
		if address == "" || usedHeaders[address] {
			continue
		}
		if len(c.Draft.Cc) >= ccLimit {
			ccOmitted = true
			continue
		}
		usedHeaders[address] = true
		c.Draft.Cc = append(c.Draft.Cc, address)
	}
	slices.SortFunc(c.RecipientOptions, func(l, r patchRecipientOption) int { return strings.Compare(l.ID, r.ID) })
	var body memberWelcomeText
	body.line("%s · 成员运营资料", exchange)
	body.line("成员：%s", c.MemberName)
	if asn := number(member, "asn"); asn > 0 && asn <= 4294967294 && asn == math.Trunc(asn) {
		body.line("成员 ASN：AS%.0f", asn)
	} else {
		body.line("成员 ASN：未登记")
	}
	if asn := number(s.Settings, "asn"); asn > 0 && asn <= 4294967294 && asn == math.Trunc(asn) {
		body.line("交换中心 ASN：AS%.0f", asn)
	}
	body.line("成员登记状态：%s", welcomeMemberStatus(str(member, "status")))
	body.line("以下为当前登记与分配资料，不表示物理链路、BGP 会话或 DNS 已经生效。\n")
	connections := 0
	scopedConnections := []Record{}
	for _, connection := range noteSortedRecords(s, "connections") {
		if str(connection, "memberId") == memberID {
			scopedConnections = append(scopedConnections, connection)
		}
		if len(scopedConnections) > memberWelcomeConnectionLimit {
			return c, errors.New("成员连接超过 100 条，请分批整理运营资料")
		}
	}
	for _, connection := range scopedConnections {
		connections++
		if err = bind("connection", connection, "name memberId vlanId portId portIds linkMode mtu status ipv4 ipv6 dnsName4 dnsName6 speed ownershipSince bindingSince"); err != nil {
			return c, err
		}
		copy := welcomeFields(connection, "id memberId portId portIds linkMode speed status vlanId mtu ipv4 ipv6")
		if validateNetworkConnection(s, copy) != nil {
			return c, errors.New("成员连接或物理端口归属存在冲突，请核对后再生成邮件")
		}
		ports, _ := connectionPortIDs(connection)
		if _, multiple := connection["portIds"]; multiple {
			canonical := make([]any, len(ports))
			for index, id := range ports {
				canonical[index] = id
			}
			evidence["connection:"+str(connection, "id")]["portIds"] = canonical
		}
		vlan := find(s, "vlans", str(connection, "vlanId"))
		if err = bind("vlan", vlan, "name tag infrastructureId ipv4Prefix ipv6Prefix prefixes"); err != nil {
			return c, err
		}
		prefixes, e := vlanPrefixes(vlan)
		if e != nil {
			return c, errors.New("成员 VLAN 前缀资料无效")
		}
		canonicalPrefixes := make([]string, 0, len(prefixes))
		for _, prefix := range prefixes {
			canonicalPrefixes = append(canonicalPrefixes, prefix.String())
		}
		slices.Sort(canonicalPrefixes)
		evidence["vlan:"+str(vlan, "id")]["prefixes"] = canonicalPrefixes
		name, e := welcomeLabel(str(connection, "name"))
		if e != nil {
			return c, e
		}
		vlanName, e := welcomeLabel(str(vlan, "name"))
		if e != nil {
			return c, e
		}
		body.line("连接 %d：%s", connections, name)
		body.line("登记状态：%s", welcomeConnectionStatus(str(connection, "status")))
		mode := "单端口"
		if str(copy, "linkMode") == "lag" {
			mode = "LAG"
			if len(ports) == 1 {
				mode = "单成员 LAG"
			}
		}
		body.line("接入形式：%s；物理端口数：%d", mode, len(ports))
		if mtu := number(connection, "mtu"); mtu > 0 {
			body.line("登记 MTU：%.0f", mtu)
		}
		if infraID := fabricID(vlan); infraID != "" {
			infra := find(s, "infrastructures", infraID)
			if err = bind("infrastructure", infra, "name"); err != nil {
				return c, err
			}
			label, e := welcomeLabel(str(infra, "name"))
			if e != nil {
				return c, e
			}
			body.line("交换网络：%s", label)
		}
		for _, id := range ports {
			port := find(s, "ports", id)
			if err = bind("port", port, "name switchId speed status memberId ownershipSince"); err != nil {
				return c, err
			}
			sw := find(s, "switches", str(port, "switchId"))
			if err = bind("switch", sw, "name locationId rackId infrastructureId"); err != nil {
				return c, err
			}
			if fabricID(sw) != fabricID(vlan) {
				return c, errors.New("成员连接与交换机不属于同一交换网络")
			}
			location := find(s, "locations", str(sw, "locationId"))
			if err = bind("location", location, "name"); err != nil {
				return c, err
			}
			labels := []string{}
			for _, row := range []Record{sw, port, location} {
				label, e := welcomeLabel(str(row, "name"))
				if e != nil {
					return c, e
				}
				labels = append(labels, label)
			}
			body.line("交换机／端口：%s / %s；机房：%s", labels[0], labels[1], labels[2])
			if rackID := str(sw, "rackId"); rackID != "" {
				rack := find(s, "racks", rackID)
				if err = bind("rack", rack, "name locationId"); err != nil {
					return c, err
				}
				if str(rack, "locationId") != str(location, "id") {
					return c, errors.New("机柜与交换机机房登记不一致")
				}
				label, e := welcomeLabel(str(rack, "name"))
				if e != nil {
					return c, e
				}
				body.line("机柜：%s", label)
			}
			if speed := number(port, "speed"); speed > 0 && !math.IsInf(speed, 0) && !math.IsNaN(speed) {
				body.line("端口登记速率：%g Mbps", speed)
			} else {
				body.line("端口速率：未登记")
			}
		}
		if capacity, known := connectionCapacity(s, connection); known {
			body.line("物理速率合计：%g Mbps", capacity)
		} else {
			body.line("物理速率合计：未知（端口速率未完整登记）")
		}
		body.line("VLAN：%s；登记编号：%.0f", vlanName, number(vlan, "tag"))
		body.line("线上 tagging / trunk 与 duplex 未在当前资料中明确登记，请另行核对。")
		for _, family := range []int{4, 6} {
			field := fmt.Sprintf("ipv%d", family)
			raw := str(connection, field)
			if raw == "" {
				body.line("IPv%d：未登记地址", family)
				continue
			}
			address, e := provisionAddress(raw, family)
			if e != nil {
				return c, errors.New("成员连接地址无效")
			}
			matches := []netip.Prefix{}
			for _, prefix := range prefixes {
				if prefix.Contains(address) {
					matches = append(matches, prefix)
				}
			}
			if len(matches) == 1 {
				body.line("IPv%d：%s/%d", family, address, matches[0].Bits())
				if family == 4 {
					body.line("IPv4 子网掩码：%s", net.IP(net.CIDRMask(matches[0].Bits(), 32)).String())
				}
			} else {
				body.line("IPv%d：%s（前缀长度未能唯一确定）", family, address)
			}
			dns, e := provisionDNSName(connection, fmt.Sprintf("dnsName%d", family))
			if e != nil {
				return c, errors.New("成员登记 DNS 名称无效")
			}
			poolCount := 0
			for _, pool := range noteSortedRecords(s, "ip-addresses") {
				if str(pool, "vlanId") != str(vlan, "id") {
					continue
				}
				poolAddress, e := netip.ParseAddr(str(pool, "address"))
				if e != nil || poolAddress != address {
					continue
				}
				poolCount++
				if poolCount > 1 || str(pool, "memberId") != "" && str(pool, "memberId") != memberID {
					return c, errors.New("成员地址池归属存在冲突")
				}
				if err = bind("ip-address", pool, "vlanId memberId address dnsName ownershipSince"); err != nil {
					return c, err
				}
				inherited, e := reservationDNS(connection, pool, family)
				if e != nil {
					return c, errors.New("连接与预留地址的 DNS 登记不一致")
				}
				if dns == "" {
					dns = inherited
				}
			}
			if dns != "" {
				body.line("登记 DNS/PTR 名称：%s（需另行确认 DNS 已发布）", dns)
			}
		}
		body.line("")
	}
	if connections == 0 {
		body.line("连接分配：尚无已登记连接，请与交换中心确认接入安排。\n")
	}
	body.line("成员门户与账号")
	base := welcomeURL(a.mailConfig.BaseURL)
	if base != "" {
		body.line("成员门户：%s/", strings.TrimRight(base, "/"))
	} else {
		body.line("门户地址未配置，请向交换中心索取。")
	}
	admins := 0
	for _, account := range accounts {
		if account["memberAdmin"] == true {
			admins++
			body.line("当前成员管理员：%s <%s>", str(account, "name"), str(account, "email"))
		}
	}
	if admins == 0 {
		body.line("当前尚未登记本组织的成员管理员账号。")
	}
	body.line("账号邀请和密码重置须通过独立账号流程完成；本邮件不会创建账号或提供登录凭据。\n")
	body.line("交叉连接与互联")
	if base != "" {
		body.line("交叉连接资料：%s/#patching", strings.TrimRight(base, "/"))
		body.line("互联请求与状态：%s/#peering", strings.TrimRight(base, "/"))
	}
	body.line("交叉连接需另行核对机房交接与 LOA；互联请求获批不代表 BGP 已建立。\n")
	body.line("NOC 与支持")
	for _, field := range []struct{ key, label string }{{"nocEmail", "成员 NOC 邮箱"}, {"email", "成员互联邮箱"}} {
		if address := welcomeMailbox(str(member, field.key)); address != "" {
			body.line("%s：%s", field.label, address)
		}
	}
	for _, field := range []struct{ key, label string }{{"phone", "NOC 电话"}, {"noc24hPhone", "24 小时电话"}, {"nocHours", "NOC 服务时间"}} {
		if value := str(member, field.key); value != "" {
			label, e := welcomeLabel(value)
			if e != nil {
				return c, e
			}
			body.line("%s：%s", field.label, label)
		}
	}
	for _, field := range []struct {
		record     Record
		key, label string
	}{{member, "nocWebsite", "成员 NOC 网站"}, {member, "website", "成员网站"}, {s.Settings, "website", "交换中心网站"}} {
		if value := welcomeURL(str(field.record, field.key)); value != "" {
			body.line("%s：%s", field.label, value)
		}
	}
	if support != "" {
		body.line("交换中心支持：%s", support)
	}
	if ccOmitted {
		body.line("\n默认抄送仅选择了可编辑的账号子集，并非向全部关联账号发送；请核对收件人，每封最多 %d 个地址。", notify.MaxRecipients)
	}
	if body.err != nil {
		return c, body.err
	}
	c.Draft.Text = body.String()
	keys := make([]string, 0, len(evidence))
	for key := range evidence {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	rows := make([]Record, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, Record{"kind": strings.SplitN(key, ":", 2)[0], "record": evidence[key]})
	}
	c.Draft.Revision = a.coreHMAC("member-welcome-context-v1", Record{"memberId": memberID, "evidence": rows, "settings": welcomeFields(s.Settings, "name asn website supportEmail"), "from": a.mailConfig.From, "origin": a.mailConfig.BaseURL, "deliveryEnabled": a.mailConfig.Enabled, "draft": c.Draft, "recipients": c.RecipientOptions, "blockingReasons": c.BlockingReasons})
	return c, nil
}

func welcomeFields(record Record, fields string) Record {
	out := Record{}
	for _, field := range strings.Fields(fields) {
		if value, found := record[field]; found {
			out[field] = value
		}
	}
	return out
}
func welcomeLabel(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > 200 || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
		return "", errors.New("成员运营资料包含过长或无效的显示字段")
	}
	return value, nil
}
func welcomeMailbox(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) > 254 {
		return ""
	}
	if normalized, ok := noteMailbox(value); ok {
		return normalized
	}
	return ""
}
func welcomeURL(value string) string {
	if value == "" || len(value) > 2048 || strings.TrimSpace(value) != value || strings.ContainsFunc(value, unicode.IsControl) {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return ""
	}
	return value
}
func welcomeMemberStatus(status string) string {
	switch status {
	case "active":
		return "活跃"
	case "pending":
		return "待开通"
	case "suspended":
		return "暂停"
	case "closed":
		return "关闭"
	}
	return "未确认"
}
func welcomeConnectionStatus(status string) string {
	switch status {
	case "active":
		return "启用（登记）"
	case "pending":
		return "待开通"
	case "disabled":
		return "停用"
	case "quarantine":
		return "隔离"
	}
	return "未确认"
}
