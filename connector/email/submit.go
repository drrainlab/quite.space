// Outbound: one reply becomes one RFC message through JMAP submission.
// Text only, exactly as the profile promises in both directions — no
// attachments, no HTML, and the thread headers come from the journal's own
// record of what was imported, never from anything the space said.
package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/node"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// identity resolves (once) the JMAP identity and drafts mailbox used for
// submission.
func (c *client) identity(ctx context.Context) error {
	if c.identityID != "" && c.draftsID != "" {
		return nil
	}
	if err := c.session(ctx); err != nil {
		return err
	}
	var resp jmapResponse
	err := c.call(ctx, []any{
		[]any{"Identity/get", map[string]any{"accountId": c.accountID}, "i"},
		[]any{"Mailbox/query", map[string]any{
			"accountId": c.accountID,
			"filter":    map[string]any{"role": "drafts"},
		}, "m"},
	}, &resp)
	if err != nil {
		return err
	}
	for _, raw := range resp.MethodResponses {
		var probe []json.RawMessage
		if err := json.Unmarshal(raw, &probe); err != nil || len(probe) < 2 {
			continue
		}
		var name string
		_ = json.Unmarshal(probe[0], &name)
		switch name {
		case "Identity/get":
			var body struct {
				List []struct {
					ID    string `json:"id"`
					Email string `json:"email"`
				} `json:"list"`
			}
			if err := json.Unmarshal(probe[1], &body); err == nil {
				for _, ident := range body.List {
					if c.identityID == "" || ident.Email == c.cfg.Account {
						c.identityID = ident.ID
					}
				}
			}
		case "Mailbox/query":
			var body struct {
				IDs []string `json:"ids"`
			}
			if err := json.Unmarshal(probe[1], &body); err == nil && len(body.IDs) > 0 {
				c.draftsID = body.IDs[0]
			}
		}
	}
	if c.identityID == "" {
		return errors.New("email: no submission identity on the account")
	}
	if c.draftsID == "" {
		return errors.New("email: no drafts mailbox to stage a reply in")
	}
	return nil
}

// SubmitReply stages the reply as a draft and submits it, one batch. The
// outbound gate mirrors the inbound one: text clamped to the protocol
// budget, nothing else in the body, thread headers only from the record.
func (c *client) SubmitReply(ctx context.Context, out node.OutboundEnvelope) error {
	if out.To == "" {
		return errors.New("email: reply has no destination")
	}
	if err := c.identity(ctx); err != nil {
		return err
	}
	text := out.Text
	if len(text) > schemas.MaxTextLen {
		text = truncateRunes(text, schemas.MaxTextLen)
	}
	create := map[string]any{
		"mailboxIds": map[string]any{c.draftsID: true},
		"keywords":   map[string]any{"$draft": true, "$seen": true},
		"from":       []map[string]any{{"email": c.cfg.Account}},
		"to":         []map[string]any{{"email": out.To}},
		"subject":    out.Subject,
		"bodyValues": map[string]any{"b1": map[string]any{"value": text}},
		"textBody":   []map[string]any{{"partId": "b1", "type": "text/plain"}},
	}
	if out.InReplyTo != "" {
		create["inReplyTo"] = []string{out.InReplyTo}
		create["references"] = []string{out.InReplyTo}
	}
	var resp jmapResponse
	err := c.call(ctx, []any{
		[]any{"Email/set", map[string]any{
			"accountId": c.accountID,
			"create":    map[string]any{"e1": create},
		}, "e"},
		[]any{"EmailSubmission/set", map[string]any{
			"accountId": c.accountID,
			"create": map[string]any{"s1": map[string]any{
				"emailId":    "#e1",
				"identityId": c.identityID,
			}},
		}, "s"},
	}, &resp)
	if err != nil {
		return err
	}
	// The submission must have been CREATED; anything else is a failure the
	// caller retries (the claim stays in flight).
	for _, raw := range resp.MethodResponses {
		var probe []json.RawMessage
		if err := json.Unmarshal(raw, &probe); err != nil || len(probe) < 2 {
			continue
		}
		var name string
		_ = json.Unmarshal(probe[0], &name)
		if name != "EmailSubmission/set" {
			continue
		}
		var body struct {
			Created    map[string]any `json:"created"`
			NotCreated map[string]any `json:"notCreated"`
		}
		if err := json.Unmarshal(probe[1], &body); err != nil {
			return err
		}
		if len(body.NotCreated) > 0 {
			return fmt.Errorf("email: submission refused: %v", body.NotCreated)
		}
		if _, ok := body.Created["s1"]; ok {
			return nil
		}
	}
	return errors.New("email: submission not confirmed")
}
