package app

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/moedove/ixp-manager/internal/notify"
)

func noteMailExcerpt(value string) string {
	value = strings.ReplaceAll(value, "\x00", "")
	if len(value) <= 8192 {
		return value
	}
	value = value[:8192]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "\n[正文较长，仅显示摘录]"
}
func noteMailbox(raw string) (string, bool) {
	address, err := mail.ParseAddress(raw)
	return strings.ToLower(raw), err == nil && address.Address == raw && !strings.ContainsAny(raw, "\x00\r\n")
}
func noteMailText(a *App, s *State, actor, old, next Record) string {
	n, action := next, "已更新"
	if old == nil {
		action = "已新建"
	}
	if next == nil {
		n, action = old, "已删除"
	}
	member := find(s, "members", str(n, "memberId"))
	text := fmt.Sprintf("成员备注%s\n成员：%s\n操作人：%s\n备注：%s\n可见范围：%s\n", action, noteMailExcerpt(str(member, "name")), noteMailExcerpt(str(actor, "name")), noteMailExcerpt(str(n, "name")), str(n, "visibility"))
	if old != nil {
		text += "\n变更前：\n" + noteMailExcerpt(str(old, "body")) + "\n"
	}
	if next != nil {
		text += "\n当前内容：\n" + noteMailExcerpt(str(next, "body")) + "\n"
	}
	text += "\n这是提交时的备注快照；之后的更新不会改写本邮件。\n"
	if next != nil {
		text += "登录查看：" + strings.TrimSuffix(a.mailConfig.BaseURL, "/") + "/#notes?" + url.Values{"id": {str(n, "id")}}.Encode() + "\n"
	}
	return text
}
func (a *App) enqueueNoteMail(s *State, actor, old, next Record) error {
	n := next
	if n == nil {
		n = old
	}
	member := noteEntity(find(s, "members", str(n, "memberId")))
	members := []NoteEntityBinding{member}
	if old != nil && str(old, "memberId") != member.ID {
		members = append(members, noteEntity(find(s, "members", str(old, "memberId"))))
	}
	l := noteState(s)
	users := noteSortedRecords(s, "users")
	seenEmail := map[string]bool{}
	now := time.Now().UTC()
	for _, u := range users {
		if !noteStaff(u) {
			continue
		}
		p := notePreferences(s, u)
		if p.Revision == 0 || !noteWatched(p, noteEntity(n), members) {
			continue
		}
		email, valid := noteMailbox(str(u, "email"))
		if !valid || seenEmail[email] {
			continue
		}
		if !validNoteEntity(member) {
			return errors.New("note member entity identity is unavailable")
		}
		for _, m := range members {
			if !validNoteEntity(m) {
				return errors.New("previous note member identity is unavailable")
			}
		}
		seenEmail[email] = true
		// Sequence is allocated in this transaction, once for the logical change.
		key := fmt.Sprintf("note:%d:%s", l.Sequence, digest(str(u, "id"))[:24])
		entry, err := s.Outbox.Enqueue(key, notify.Message{From: a.mailConfig.From, To: str(u, "email"), Subject: "成员备注变更通知", Text: noteMailText(a, s, actor, old, next)}, a.store.securityKey, now, now.Add(7*24*time.Hour))
		if err != nil {
			return err
		}
		l.Mail = append(l.Mail, NoteMailBinding{OutboxID: entry.ID, Key: key, UserID: str(u, "id"), UserCreatedAt: str(u, "createdAt"), Generation: s.Security[str(u, "id")].Generation, PreferenceRevision: p.Revision, Email: entry.To, Note: noteEntity(n), Member: member, WatchMembers: members, Deleted: next == nil})
	}
	pruneNoteState(s)
	return nil
}

func noteMailApplicable(s *State, e notify.Entry) bool {
	b := noteMailBinding(s, e.ID)
	if b == nil || b.Key != e.Key || b.Email != e.To {
		return false
	}
	u := find(s, "users", b.UserID)
	if !noteStaff(u) || str(u, "createdAt") != b.UserCreatedAt || str(u, "email") != b.Email || s.Security[b.UserID].Generation != b.Generation {
		return false
	}
	p := notePreferences(s, u)
	if p.Revision != b.PreferenceRevision || !noteWatched(p, b.Note, b.WatchMembers) {
		return false
	}
	if !noteEntityCurrent(s, "members", b.Member) {
		return false
	}
	for _, m := range b.WatchMembers {
		if !noteEntityCurrent(s, "members", m) {
			return false
		}
	}
	n := find(s, "notes", b.Note.ID)
	if b.Deleted {
		return n == nil
	}
	return noteEntityCurrent(s, "notes", b.Note) && str(n, "memberId") == b.Member.ID
}

// Retrying a stable message never changes its original recipient or snapshot.
func noteRetryApplicable(s *State, id string) error {
	for _, e := range s.Outbox.Entries {
		if e.ID == id && strings.HasPrefix(e.Key, "note:") && !noteMailApplicable(s, e) {
			return errors.New("note recipient, subscription or entity changed; delivery cannot be retried")
		}
	}
	return nil
}

func noteSortedBindings(values []NoteEntityBinding) []NoteEntityBinding {
	out := append([]NoteEntityBinding{}, values...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
