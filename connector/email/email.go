// Email connector (TR-0d): the reference adapter of the External Terminal
// epic. It speaks JMAP to a mail server the operator runs (Stalwart) and
// hands NORMALIZED, POLICY-GATED envelopes to the node's connector seam —
// it is not a mail server, it never will be, and the node behind it never
// learns SMTP exists.
//
// THE POLICY GATE SITS BEFORE THE BYTES (plan rev 4). The default — and for
// now only — profile is text-only: the poller asks JMAP for the message's
// STRUCTURE and its text parts bounded to the protocol's own text limit,
// and it never requests an attachment blob. Rejected parts are not
// downloaded, not stored, not decided about — they simply never cross this
// boundary, and the projection says so ("N attachments omitted") with
// loss_flags carrying the machine-readable half. The original stays intact
// in the operator's mailbox.
//
// LIMITS ARE ENFORCED WHILE READING, never after: the JMAP response rides a
// bounded reader, the body values are requested pre-truncated
// (maxBodyValueBytes) and re-clamped locally because a server's manners are
// not a security boundary.
package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/node"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// Config is one email connector's configuration.
type Config struct {
	// URL is the JMAP session endpoint (Stalwart: https://host/.well-known/jmap).
	URL string
	// Account is the address this connector serves (hello@quite.space).
	Account string
	// Token authenticates to the mail server (Bearer).
	Token string
	// PollSeconds is the fetch cadence; 0 = 15s.
	PollSeconds int
	// QueryWindow bounds how many recent messages each poll asks about;
	// the journal's idempotency makes overlap free. 0 = 20.
	QueryWindow int
}

// maxJMAPResponse bounds one JMAP response read. A mail server is OUTSIDE
// the trust boundary: a 2 GB "session object" must cost us nothing.
const maxJMAPResponse = 4 << 20

// Sink is where gated envelopes go — the node's ConnectorIngest, curried
// with the connector id.
type Sink func(node.ExternalEnvelope) error

// Outbox hands back the replies authorized to leave right now (already
// claimed in the journal), and Settle records each send's fate. Both are
// the node's methods, curried with the connector id.
type Outbox func() []node.OutboundEnvelope
type Settle func(eid id.EventID, sent bool, outcome string)

// Run polls until ctx ends — inbound and outbound on one cadence. Errors
// are paced, never fatal: a mail server rebooting must not take the
// poller with it, and an unsettled send is retried by the journal's own
// resume list (at-least-once for outbound, stated honestly).
func Run(ctx context.Context, cfg Config, sink Sink, outbox Outbox, settle Settle) {
	every := time.Duration(cfg.PollSeconds) * time.Second
	if every <= 0 {
		every = 15 * time.Second
	}
	c := &client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}
	t := time.NewTicker(every)
	defer t.Stop()
	var lastErr, lastSendErr string
	for {
		// A REFUSAL MUST NEVER BE A SILENCE. The journal makes retries free,
		// so an error here is not fatal — but a poller that cannot reach its
		// mailbox and says nothing is indistinguishable from a quiet inbox,
		// which is exactly the failure this project keeps removing. Logged
		// once per DISTINCT reason, so a server that is down for an hour
		// costs one line rather than two hundred and forty.
		if err := c.PollOnce(ctx, sink); err != nil {
			if msg := err.Error(); msg != lastErr {
				lastErr = msg
				log.Printf("connector email: cannot read %s: %v", cfg.Account, err)
			}
		} else if lastErr != "" {
			lastErr = ""
			log.Printf("connector email: reading %s again", cfg.Account)
		}
		if outbox != nil {
			for _, out := range outbox() {
				if err := c.SubmitReply(ctx, out); err != nil {
					// In flight, not lost: the journal's resume list offers
					// it again. Said out loud once per distinct reason —
					// a reply that never leaves must not be silent either.
					if msg := err.Error(); msg != lastSendErr {
						lastSendErr = msg
						log.Printf("connector email: reply to %s not sent: %v", out.To, err)
					}
					continue
				}
				lastSendErr = ""
				log.Printf("connector email: replied to %s", out.To)
				settle(out.EventID, true, "")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---- JMAP client, exactly as much of it as the profile needs ----

type client struct {
	cfg  Config
	http *http.Client

	apiURL     string
	accountID  string
	identityID string
	draftsID   string
	inboxID    string
}

type jmapRequest struct {
	Using       []string `json:"using"`
	MethodCalls []any    `json:"methodCalls"`
}

type jmapResponse struct {
	MethodResponses []json.RawMessage `json:"methodResponses"`
}

func (c *client) authGet(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("email: %s → %s", url, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxJMAPResponse)).Decode(out)
}

// authorize picks the credential shape from what the operator configured.
//
// An ACCOUNT plus a secret is a mailbox login — RFC 8620 says JMAP servers
// accept it as HTTP Basic, and that is what every mail server means by "the
// account's password". A bare secret with no account is an OAuth bearer
// token. Sending the wrong one produces a 401 that looks exactly like a bad
// password, which is what the first live deployment spent its time on.
func (c *client) authorize(req *http.Request) {
	if c.cfg.Account != "" {
		req.SetBasicAuth(c.cfg.Account, c.cfg.Token)
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
}

// session resolves the API url and the mail account id, once.
func (c *client) session(ctx context.Context) error {
	if c.apiURL != "" {
		return nil
	}
	var sess struct {
		APIURL          string                     `json:"apiUrl"`
		PrimaryAccounts map[string]string          `json:"primaryAccounts"`
		Accounts        map[string]json.RawMessage `json:"accounts"`
	}
	if err := c.authGet(ctx, c.cfg.URL, &sess); err != nil {
		return err
	}
	if sess.APIURL == "" {
		return errors.New("email: session carries no apiUrl")
	}
	// THE SESSION'S apiUrl IS A PATH, NOT A DESTINATION. A mail server
	// publishes the address its OUTSIDE clients should use — Stalwart
	// returns https://mx1.example/jmap/ once a hostname is configured — and
	// a connector that followed it would leave the machine, cross the
	// internet, and arrive at whatever answers 443 on that name. Measured
	// on the first live deployment: the poller silently talked to the
	// public web server instead of the mailbox one metre away.
	//
	// It is also the SSRF shape: a compromised or misconfigured server
	// could redirect an authenticated poller — Bearer token and all — at
	// any host it likes. So we keep the origin WE dialled and take only
	// the path the server named.
	c.apiURL = resolveAgainst(c.cfg.URL, sess.APIURL)
	acc := sess.PrimaryAccounts["urn:ietf:params:jmap:mail"]
	if acc == "" {
		for id := range sess.Accounts {
			acc = id
			break
		}
	}
	if acc == "" {
		return errors.New("email: session names no mail account")
	}
	c.accountID = acc
	return nil
}

// resolveAgainst keeps base's scheme and host and adopts ref's path — for a
// relative ref the ordinary join, for an absolute one a deliberate refusal
// to be redirected off the host we authenticated to.
func resolveAgainst(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	out := *b
	out.Path = r.Path
	out.RawQuery = r.RawQuery
	out.Fragment = ""
	return out.String()
}

func (c *client) call(ctx context.Context, calls []any, out *jmapResponse) error {
	// EVERY capability this connector's methods belong to, declared up
	// front. A JMAP server refuses a method whose capability is missing
	// from `using` — and the refusal names the capability, not the call,
	// so a submission that never left looked like a broken mailbox on the
	// first live deployment.
	body, err := json.Marshal(jmapRequest{
		Using: []string{
			"urn:ietf:params:jmap:core",
			"urn:ietf:params:jmap:mail",
			"urn:ietf:params:jmap:submission",
		},
		MethodCalls: calls,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.authorize(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("email: api → %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxJMAPResponse)).Decode(out)
}

// jmapEmail is the slice of Email/get this profile reads. NOTHING here
// fetches an attachment: bodyStructure is metadata, bodyValues carries only
// the text parts the request named, pre-truncated by the server.
type jmapEmail struct {
	ID            string              `json:"id"`
	MessageID     []string            `json:"messageId"`
	InReplyTo     []string            `json:"inReplyTo"`
	From          []jmapAddr          `json:"from"`
	Subject       string              `json:"subject"`
	ReceivedAt    string              `json:"receivedAt"`
	HasAttachment bool                `json:"hasAttachment"`
	TextBody      []jmapPart          `json:"textBody"`
	HTMLBody      []jmapPart          `json:"htmlBody"`
	Attachments   []jmapPart          `json:"attachments"`
	BodyValues    map[string]jmapBody `json:"bodyValues"`
}

type jmapAddr struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type jmapPart struct {
	PartID string `json:"partId"`
	Type   string `json:"type"`
}

type jmapBody struct {
	Value       string `json:"value"`
	IsTruncated bool   `json:"isTruncated"`
}

// inbox resolves the INBOX mailbox id, once.
func (c *client) inbox(ctx context.Context) error {
	if c.inboxID != "" {
		return nil
	}
	if err := c.session(ctx); err != nil {
		return err
	}
	var resp jmapResponse
	if err := c.call(ctx, []any{
		[]any{"Mailbox/query", map[string]any{
			"accountId": c.accountID,
			"filter":    map[string]any{"role": "inbox"},
		}, "m"},
	}, &resp); err != nil {
		return err
	}
	for _, raw := range resp.MethodResponses {
		var probe []json.RawMessage
		if err := json.Unmarshal(raw, &probe); err != nil || len(probe) < 2 {
			continue
		}
		var name string
		_ = json.Unmarshal(probe[0], &name)
		if name != "Mailbox/query" {
			continue
		}
		var body struct {
			IDs []string `json:"ids"`
		}
		if err := json.Unmarshal(probe[1], &body); err == nil && len(body.IDs) > 0 {
			c.inboxID = body.IDs[0]
			return nil
		}
	}
	return errors.New("email: the account has no inbox")
}

// PollOnce fetches the recent window and hands every message through the
// gate to the sink. Overlap between polls is free: the journal dedups on
// the JMAP id.
func (c *client) PollOnce(ctx context.Context, sink Sink) error {
	if err := c.session(ctx); err != nil {
		return err
	}
	window := c.cfg.QueryWindow
	if window <= 0 {
		window = 20
	}
	// READ THE INBOX, NOT THE ACCOUNT. An account holds Sent and Drafts
	// too, and querying all of it makes the connector import its own
	// replies back into the space as fresh mail — an echo that arrives
	// with foreign provenance and looks, to a person, exactly like the
	// correspondent writing again. Measured on the first live reply.
	if err := c.inbox(ctx); err != nil {
		return err
	}
	var resp jmapResponse
	err := c.call(ctx, []any{
		[]any{"Email/query", map[string]any{
			"accountId": c.accountID,
			"filter":    map[string]any{"inMailbox": c.inboxID},
			"sort":      []map[string]any{{"property": "receivedAt", "isAscending": false}},
			"limit":     window,
		}, "q"},
		[]any{"Email/get", map[string]any{
			"accountId": c.accountID,
			"#ids": map[string]any{
				"resultOf": "q", "name": "Email/query", "path": "/ids",
			},
			"properties": []string{
				"id", "messageId", "inReplyTo", "from", "subject",
				"receivedAt", "hasAttachment", "textBody", "htmlBody",
				"attachments", "bodyValues",
			},
			// The gate, expressed as the REQUEST: text parts only, already
			// truncated to the protocol's own text budget.
			"fetchTextBodyValues": true,
			"fetchHTMLBodyValues": true,
			"maxBodyValueBytes":   schemas.MaxTextLen,
		}, "g"},
	}, &resp)
	if err != nil {
		return err
	}
	list, err := extractEmailList(resp)
	if err != nil {
		return err
	}
	for i := range list {
		env, ok := Gate(&list[i])
		if !ok {
			continue // no acceptable text; the poller has nothing to say yet
		}
		_ = sink(env)
	}
	return nil
}

func extractEmailList(resp jmapResponse) ([]jmapEmail, error) {
	for _, raw := range resp.MethodResponses {
		var probe []json.RawMessage
		if err := json.Unmarshal(raw, &probe); err != nil || len(probe) < 2 {
			continue
		}
		var name string
		if err := json.Unmarshal(probe[0], &name); err != nil || name != "Email/get" {
			continue
		}
		var body struct {
			List []jmapEmail `json:"list"`
		}
		if err := json.Unmarshal(probe[1], &body); err != nil {
			return nil, err
		}
		return body.List, nil
	}
	return nil, errors.New("email: response carries no Email/get")
}

// Gate is the text-only policy gate: one observed message in, one bounded
// envelope out — or nothing, honestly. Exported for the tests that pin the
// profile's promises.
func Gate(m *jmapEmail) (node.ExternalEnvelope, bool) {
	var loss []string
	text, flags := extractText(m)
	loss = append(loss, flags...)
	if m.HasAttachment || len(m.Attachments) > 0 {
		n := len(m.Attachments)
		if n == 0 {
			n = 1
		}
		plural := ""
		if n > 1 {
			plural = "s"
		}
		note := fmt.Sprintf("[%d attachment%s omitted by Mailbox policy]", n, plural)
		if text == "" {
			text = note
		} else {
			text = text + "\n\n" + note
		}
		loss = append(loss, "attachments_omitted")
	}
	if text == "" {
		return node.ExternalEnvelope{}, false
	}
	if m.Subject != "" {
		text = m.Subject + "\n\n" + text
	}
	// Re-clamp locally: the request asked the server to truncate, and a
	// server's manners are not a boundary. Cut on a rune edge.
	if len(text) > schemas.MaxTextLen {
		text = truncateRunes(text, schemas.MaxTextLen)
		loss = appendUnique(loss, "text_truncated")
	}
	addr := ""
	if len(m.From) > 0 {
		addr = m.From[0].Email
	}
	var ref, thread string
	if len(m.MessageID) > 0 {
		ref = m.MessageID[0]
	}
	if len(m.InReplyTo) > 0 {
		thread = m.InReplyTo[0]
	}
	observed := time.Now().Unix()
	if t, err := time.Parse(time.RFC3339, m.ReceivedAt); err == nil {
		observed = t.Unix()
	}
	return node.ExternalEnvelope{
		ExternalID:  m.ID,
		Kind:        "email",
		Address:     addr,
		ExternalRef: ref,
		ThreadRef:   thread,
		Subject:     m.Subject,
		Text:        text,
		LossFlags:   loss,
		ObservedAt:  observed,
	}, true
}

// extractText prefers text/plain; an HTML-only message goes through the
// stripper — a document parser's OUTPUT, never a renderer's input.
func extractText(m *jmapEmail) (string, []string) {
	for _, p := range m.TextBody {
		if b, ok := m.BodyValues[p.PartID]; ok && strings.TrimSpace(b.Value) != "" {
			var loss []string
			if b.IsTruncated {
				loss = append(loss, "text_truncated")
			}
			return strings.TrimSpace(b.Value), loss
		}
	}
	for _, p := range m.HTMLBody {
		if b, ok := m.BodyValues[p.PartID]; ok && strings.TrimSpace(b.Value) != "" {
			loss := []string{"html_extracted"}
			if b.IsTruncated {
				loss = append(loss, "text_truncated")
			}
			return strings.TrimSpace(StripHTML(b.Value)), loss
		}
	}
	return "", nil
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	for len(cut) > 0 && !isRuneStart(cut[len(cut)-1]) {
		cut = cut[:len(cut)-1]
	}
	if len(cut) > 0 {
		cut = cut[:len(cut)-1]
	}
	return cut
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
