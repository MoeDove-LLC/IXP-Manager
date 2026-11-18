package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const contactExportLimit = 10000
const contactExportBytes = 16 << 20

// ContactExportRow preserves the old contact:export-group field names and order.
// No resource metadata, account credentials or other member fields are included.
type ContactExportRow struct {
	Name       string `json:"name"`
	Position   string `json:"position"`
	Email      string `json:"email"`
	Phone      string `json:"phone"`
	Mobile     string `json:"mobile"`
	Notes      string `json:"notes"`
	CustStatus string `json:"cust_status"`
}
type contactExportQuery struct {
	Options                      bool
	Name, Type, MemberID, Format string
}
type contactExportGroupOption struct {
	Value  string `json:"value"`
	Groups int    `json:"groups"`
}
type contactExportMemberOption struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Eligible bool   `json:"eligible"`
}
type contactExportOptions struct {
	Date     string                      `json:"date"`
	Names    []contactExportGroupOption  `json:"names"`
	Types    []contactExportGroupOption  `json:"types"`
	Members  []contactExportMemberOption `json:"members"`
	MaxRows  int                         `json:"maxRows"`
	MaxBytes int                         `json:"maxBytes"`
}

func parseContactExportQuery(raw string) (contactExportQuery, error) {
	q := contactExportQuery{Format: "json"}
	if len(raw) > 32768 {
		return q, errors.New("contact export query exceeds bound")
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return q, errors.New("invalid contact export query")
	}
	for key, v := range values {
		if !contains([]string{"options", "name", "type", "memberId", "format"}, key) || len(v) != 1 || len(v[0]) > 10000 || strings.ContainsAny(v[0], "\x00\r\n") {
			return q, errors.New("unknown, duplicate or invalid contact export filter")
		}
	}
	if v, exists := values["options"]; exists {
		if v[0] != "1" || len(values) != 1 {
			return q, errors.New("options=1 cannot be combined with export filters")
		}
		q.Options = true
		return q, nil
	}
	q.Name, q.Type, q.MemberID = values.Get("name"), values.Get("type"), values.Get("memberId")
	if v, exists := values["format"]; exists {
		q.Format = v[0]
	}
	_, name := values["name"]
	_, kind := values["type"]
	if name == kind || name && strings.TrimSpace(q.Name) == "" || kind && strings.TrimSpace(q.Type) == "" || q.Format != "json" && q.Format != "csv" || q.MemberID != "" && !identifier.MatchString(q.MemberID) {
		return q, errors.New("select exactly one nonempty group name or type, optional memberId, and json or csv format")
	}
	return q, nil
}

func contactMemberCurrent(member Record, date string) bool {
	joined, left := str(member, "joinedAt"), str(member, "leftAt")
	if _, err := time.Parse("2006-01-02", joined); err != nil {
		return false
	}
	if left != "" {
		if _, err := time.Parse("2006-01-02", left); err != nil {
			return false
		}
	}
	return joined <= date && (left == "" || left >= date)
}
func contactStatusLabel(status string) string {
	switch status {
	case "active":
		return "Normal"
	case "pending":
		return "Not Connected"
	case "suspended":
		return "Suspended"
	default:
		return "Unknown"
	}
}
func contactRows(s *State, actor Record, q contactExportQuery, date string) ([]ContactExportRow, error) {
	groups := map[string]bool{}
	for _, group := range visible(s, "contact-groups", actor) {
		if q.Name != "" && str(group, "name") == q.Name || q.Type != "" && str(group, "type") == q.Type {
			groups[str(group, "id")] = true
		}
	}
	selected := map[string]bool{}
	for _, link := range visible(s, "contact-group-memberships", actor) {
		if groups[str(link, "groupId")] {
			selected[str(link, "contactId")] = true
		}
	}
	members := map[string]Record{}
	for _, member := range visible(s, "members", actor) {
		if (q.MemberID == "" || str(member, "id") == q.MemberID) && contactMemberCurrent(member, date) {
			members[str(member, "id")] = member
		}
	}
	contacts := visible(s, "contacts", actor)
	sort.Slice(contacts, func(i, j int) bool { return str(contacts[i], "id") < str(contacts[j], "id") })
	rows := []ContactExportRow{}
	for _, contact := range contacts {
		member := members[str(contact, "memberId")]
		if !selected[str(contact, "id")] || member == nil {
			continue
		}
		if len(rows) == contactExportLimit {
			return nil, errors.New("contact export exceeds 10000 rows; select a narrower group or member")
		}
		rows = append(rows, ContactExportRow{str(contact, "name"), str(contact, "position"), str(contact, "email"), str(contact, "phone"), str(contact, "mobile"), str(contact, "notes"), contactStatusLabel(str(member, "status"))})
	}
	return rows, nil
}
func contactOptions(s *State, actor Record, date string) (contactExportOptions, error) {
	result := contactExportOptions{Date: date, Names: []contactExportGroupOption{}, Types: []contactExportGroupOption{}, Members: []contactExportMemberOption{}, MaxRows: contactExportLimit, MaxBytes: contactExportBytes}
	names, types := map[string]int{}, map[string]int{}
	for _, group := range visible(s, "contact-groups", actor) {
		if name := str(group, "name"); name != "" {
			names[name]++
		}
		if kind := str(group, "type"); kind != "" {
			types[kind]++
		}
	}
	for value, count := range names {
		result.Names = append(result.Names, contactExportGroupOption{value, count})
	}
	for value, count := range types {
		result.Types = append(result.Types, contactExportGroupOption{value, count})
	}
	for _, member := range visible(s, "members", actor) {
		result.Members = append(result.Members, contactExportMemberOption{str(member, "id"), str(member, "name"), contactMemberCurrent(member, date)})
	}
	if len(result.Names) > contactExportLimit || len(result.Types) > contactExportLimit || len(result.Members) > contactExportLimit {
		return result, errors.New("contact export selection exceeds bound")
	}
	sort.Slice(result.Names, func(i, j int) bool { return result.Names[i].Value < result.Names[j].Value })
	sort.Slice(result.Types, func(i, j int) bool { return result.Types[i].Value < result.Types[j].Value })
	sort.Slice(result.Members, func(i, j int) bool {
		if result.Members[i].Name == result.Members[j].Name {
			return result.Members[i].ID < result.Members[j].ID
		}
		return result.Members[i].Name < result.Members[j].Name
	})
	return result, nil
}

// A visible literal prefix survives spreadsheet save/reopen better than relying
// on a leading quote or invisible control. JSON is the exact-value interchange.
func contactCSVCell(value string) string {
	trimmed := strings.TrimLeftFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.In(r, unicode.Cf) })
	if trimmed != value && strings.ContainsAny(value[:len(value)-len(trimmed)], "\t\r\n") || strings.HasPrefix(trimmed, "=") || strings.HasPrefix(trimmed, "+") || strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "@") || strings.HasPrefix(trimmed, "＝") || strings.HasPrefix(trimmed, "＋") || strings.HasPrefix(trimmed, "－") || strings.HasPrefix(trimmed, "＠") {
		value = "text: " + value
	}
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
func encodeContactExport(rows []ContactExportRow, format string) ([]byte, error) {
	var output bytes.Buffer
	if format == "csv" {
		output.WriteString("\"name\",\"position\",\"email\",\"phone\",\"mobile\",\"notes\",\"cust_status\"\r\n")
		for _, row := range rows {
			fields := []string{row.Name, row.Position, row.Email, row.Phone, row.Mobile, row.Notes, row.CustStatus}
			// Keep telephone '+' signs and leading zeroes as visible text when
			// opened in a spreadsheet; exact original values remain in JSON.
			for _, i := range []int{3, 4} {
				if fields[i] != "" {
					fields[i] = "text: " + fields[i]
				}
			}
			for i, value := range fields {
				fields[i] = contactCSVCell(value)
			}
			line := strings.Join(fields, ",") + "\r\n"
			if output.Len()+len(line) > contactExportBytes {
				return nil, errors.New("contact export exceeds 16 MiB; select a narrower group or member")
			}
			output.WriteString(line)
		}
	} else {
		output.WriteByte('[')
		for i, row := range rows {
			raw, err := json.Marshal(row)
			if err != nil {
				return nil, err
			}
			if output.Len()+len(raw)+3 > contactExportBytes {
				return nil, errors.New("contact export exceeds 16 MiB; select a narrower group or member")
			}
			if i > 0 {
				output.WriteByte(',')
			}
			output.Write(raw)
		}
		output.WriteString("]\n")
	}
	return output.Bytes(), nil
}

type contactExportPayload struct {
	body  []byte
	count int
	date  string
}

func contactExportAuthorized(actor Record, session Session) bool {
	if !session.API {
		return noteStaff(actor)
	}
	role := str(actor, "role")
	return (role == "admin" || role == "operator") && contains(session.Scopes, "contacts:export")
}

// Prepare one consistent, bounded snapshot under the Store lock. Recheck the
// live session at the end, then release the lock before any response is written.
func (a *App) prepareContactExport(user Record, session Session, q contactExportQuery) (contactExportPayload, int, error) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	s := &a.store.state
	payload := contactExportPayload{date: time.Now().UTC().Format("2006-01-02")}
	actor, err := a.atlasActor(s, user, session, "admin", "operator", "viewer")
	if err != nil || a.store.closed || str(actor, "createdAt") != str(user, "createdAt") || !contactExportAuthorized(actor, session) {
		return payload, 403, errors.New("contact export authority changed; sign in again")
	}
	if q.Options && session.API {
		return payload, 403, errors.New("contact automation must request an explicit group name or type")
	}
	if q.Options {
		options, err := contactOptions(s, actor, payload.date)
		if err != nil {
			return payload, 413, err
		}
		payload.body, err = json.Marshal(options)
		if err != nil || len(payload.body) > contactExportBytes {
			return contactExportPayload{}, 413, errors.New("contact export selection exceeds bound")
		}
	} else {
		rows, err := contactRows(s, actor, q, payload.date)
		if err != nil {
			return payload, 413, err
		}
		payload.body, err = encodeContactExport(rows, q.Format)
		if err != nil {
			return contactExportPayload{}, 413, err
		}
		payload.count = len(rows)
	}
	if actor, err := a.atlasActor(s, user, session, "admin", "operator", "viewer"); err != nil || !contactExportAuthorized(actor, session) {
		return contactExportPayload{}, 403, errors.New("contact export session changed; sign in again")
	}
	return payload, 200, nil
}

func (a *App) contactExportRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) bool {
	if r.URL.Path != "/api/contact-export" {
		return false
	}
	if !contactExportAuthorized(user, session) {
		problem(w, 403, "contact group exports require staff browser access or a contacts:export token")
		return true
	}
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		problem(w, 405, "contact export is read-only")
		return true
	}
	q, err := parseContactExportQuery(r.URL.RawQuery)
	if err != nil {
		problem(w, 400, err.Error())
		return true
	}
	payload, code, err := a.prepareContactExport(user, session, q)
	if err != nil {
		problem(w, code, err.Error())
		return true
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if !q.Options {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="contacts-by-group.%s"`, q.Format))
		w.Header().Set("X-Contact-Export-Count", strconv.Itoa(payload.count))
		w.Header().Set("X-Contact-Export-Date", payload.date)
		if q.Format == "csv" {
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		}
	}
	w.Write(payload.body)
	return true
}
