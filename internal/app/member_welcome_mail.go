package app

import (
	"errors"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/notify"
)

// MemberWelcomeReceipt binds an encrypted outbox entry to the reviewed
// organization, sender and idempotency request. The message stays in the outbox.
type MemberWelcomeReceipt struct {
	ID              string            `json:"id"`
	OutboxID        string            `json:"outboxId"`
	Member          NoteEntityBinding `json:"member"`
	Actor           NoteEntityBinding `json:"actor"`
	Generation      uint64            `json:"generation"`
	ContextRevision string            `json:"contextRevision"`
	ReviewDigest    string            `json:"reviewDigest"`
	RequestDigest   string            `json:"requestDigest"`
	SessionBinding  string            `json:"sessionBinding"`
	CreatedAt       time.Time         `json:"createdAt"`
	RecipientCount  int               `json:"recipientCount"`
	BCCCount        int               `json:"bccCount"`
}

const memberWelcomeMailPrefix = "welcome:"

func validMemberWelcomeReceipt(receipt MemberWelcomeReceipt, entry notify.Entry) bool {
	return identifier.MatchString(receipt.ID) && identifier.MatchString(receipt.OutboxID) && receipt.OutboxID == entry.ID &&
		entry.Key == memberWelcomeMailPrefix+receipt.ID && validNoteEntity(receipt.Member) && validNoteEntity(receipt.Actor) &&
		externalDigest(receipt.ContextRevision) && externalDigest(receipt.ReviewDigest) && externalDigest(receipt.RequestDigest) &&
		externalDigest(receipt.SessionBinding) && !receipt.CreatedAt.IsZero() && receipt.CreatedAt.Equal(entry.CreatedAt) &&
		entry.ExpiresAt.After(entry.CreatedAt) && !entry.ExpiresAt.After(entry.CreatedAt.Add(memberWelcomeDeliveryLifetime)) &&
		receipt.RecipientCount > 0 && receipt.RecipientCount <= notify.MaxRecipients &&
		receipt.BCCCount >= 0 && receipt.BCCCount < receipt.RecipientCount
}

func memberWelcomeMessageMatches(receipt MemberWelcomeReceipt, message notify.Message) bool {
	return len(message.Attachments) == 0 && len(message.Bcc) == receipt.BCCCount &&
		1+len(message.ToAdditional)+len(message.Cc)+len(message.Bcc) == receipt.RecipientCount
}

// validateMemberWelcomeMail also runs when opening an offline backup or restore.
// Superseded actor/member facts remain valid historical receipts and are checked
// for current delivery eligibility by the worker.
func validateMemberWelcomeMail(s *State, key []byte) error {
	if len(s.WelcomeMail) > notify.MaxEntries {
		return errors.New("welcome mail receipt capacity exceeded")
	}
	byEntry := map[string]notify.Entry{}
	keys := map[string]bool{}
	entryCounts := make(map[string]int, len(s.Outbox.Entries))
	for _, entry := range s.Outbox.Entries {
		entryCounts[entry.ID]++
		if !strings.HasPrefix(entry.Key, memberWelcomeMailPrefix) {
			continue
		}
		if _, exists := byEntry[entry.ID]; exists || keys[entry.Key] {
			return errors.New("duplicate welcome mail outbox binding")
		}
		byEntry[entry.ID], keys[entry.Key] = entry, true
	}
	for _, receipt := range s.WelcomeMail {
		entry, exists := byEntry[receipt.OutboxID]
		if !exists || entryCounts[receipt.OutboxID] != 1 || !validMemberWelcomeReceipt(receipt, entry) {
			return errors.New("invalid welcome mail receipt")
		}
		message, err := notify.OpenMessage(key, entry)
		if err != nil || !memberWelcomeMessageMatches(receipt, message) {
			return errors.New("invalid encrypted welcome mail or receipt counts")
		}
		delete(byEntry, receipt.OutboxID)
	}
	if len(byEntry) != 0 {
		return errors.New("welcome mail receipt is missing")
	}
	return nil
}

func pruneMemberWelcomeMail(s *State) {
	if len(s.WelcomeMail) == 0 {
		return
	}
	entries := make(map[string]bool, len(s.WelcomeMail))
	for _, entry := range s.Outbox.Entries {
		if strings.HasPrefix(entry.Key, memberWelcomeMailPrefix) {
			entries[entry.ID] = true
		}
	}
	kept := s.WelcomeMail[:0]
	for _, receipt := range s.WelcomeMail {
		if entries[receipt.OutboxID] {
			kept = append(kept, receipt)
		}
	}
	s.WelcomeMail = kept
}

func memberWelcomeReceipt(s *State, id string) *MemberWelcomeReceipt {
	for i := range s.WelcomeMail {
		if s.WelcomeMail[i].OutboxID == id {
			return &s.WelcomeMail[i]
		}
	}
	return nil
}

func (a *App) memberWelcomeMailApplicable(s *State, entry notify.Entry, now time.Time) bool {
	receipt := memberWelcomeReceipt(s, entry.ID)
	if receipt == nil || !validMemberWelcomeReceipt(*receipt, entry) || !now.Before(entry.ExpiresAt) {
		return false
	}
	member := find(s, "members", receipt.Member.ID)
	if noteEntity(member) != receipt.Member || (str(member, "status") != "active" && str(member, "status") != "pending") {
		return false
	}
	actor := find(s, "users", receipt.Actor.ID)
	if str(actor, "role") != "admin" || noteEntity(actor) != receipt.Actor || s.Security[receipt.Actor.ID].Generation != receipt.Generation {
		return false
	}
	ctx, err := a.buildMemberWelcomeContext(s, receipt.Member.ID)
	return err == nil && len(ctx.BlockingReasons) == 0 && ctx.Draft.Revision == receipt.ContextRevision
}

func (a *App) memberWelcomeTransportApplicable(s *State, opened notify.Entry, msg notify.Message, now time.Time) bool {
	receipt := memberWelcomeReceipt(s, opened.ID)
	if receipt == nil || !memberWelcomeMessageMatches(*receipt, msg) || msg.From != a.mailConfig.From {
		return false
	}
	for _, current := range s.Outbox.Entries {
		if current.ID == opened.ID {
			return current.Status == "sending" && current.Lease == opened.Lease && current.LeaseUntil.After(now) &&
				current.Key == opened.Key && current.Sealed == opened.Sealed && current.To == msg.To && current.Subject == msg.Subject &&
				current.ExpiresAt.Equal(opened.ExpiresAt) && a.memberWelcomeMailApplicable(s, current, now)
		}
	}
	return false
}
