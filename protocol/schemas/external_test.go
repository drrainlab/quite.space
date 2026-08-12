// TR-0b — ExternalOrigin, key 7 of message.text.v1, red first.
//
// The wire pattern is ADR-019's, third increment: provenance rides an
// append-only key on the schema everyone already renders, because a new
// non-block schema lands in the reducer's Unsupported bucket and becomes
// invisible. Computed inner arity (the key-6 card pattern), bounds enforced
// on encode AND decode — a bound only on encoding is a bound only for
// honest senders.
package schemas

import (
	"strings"
	"testing"
)

func TestExternalOriginRoundTrip(t *testing.T) {
	in := &TextMessage{
		Text: "Привет! Посмотри, пожалуйста, документ.",
		External: &ExternalOrigin{
			ConnectorKind: "email",
			Address:       "alice@example.org",
			ExternalRef:   "<CAF-xyz@mail.example.org>",
			ThreadRef:     "<parent-abc@mail.example.org>",
			LossFlags:     []string{"attachments_omitted", "html_extracted"},
		},
	}
	payload, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeTextMessage(payload)
	if err != nil {
		t.Fatal(err)
	}
	if out.External == nil {
		t.Fatal("external origin did not survive the wire")
	}
	e := out.External
	if e.ConnectorKind != "email" || e.Address != "alice@example.org" ||
		e.ExternalRef != "<CAF-xyz@mail.example.org>" ||
		e.ThreadRef != "<parent-abc@mail.example.org>" ||
		len(e.LossFlags) != 2 || e.LossFlags[0] != "attachments_omitted" {
		t.Fatalf("round trip mangled the origin: %+v", e)
	}
	if out.Text != in.Text {
		t.Fatal("the text itself must ride key 1, untouched by key 7")
	}
}

// Sparse origins carry only what they have — computed arity, like the card.
func TestExternalOriginSparseFields(t *testing.T) {
	in := &TextMessage{
		Text:     "no ids at all",
		External: &ExternalOrigin{ConnectorKind: "email", Address: "b@c.d"},
	}
	payload, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeTextMessage(payload)
	if err != nil {
		t.Fatal(err)
	}
	if out.External == nil || out.External.ExternalRef != "" ||
		out.External.ThreadRef != "" || out.External.LossFlags != nil {
		t.Fatalf("sparse origin grew fields: %+v", out.External)
	}
}

// Bounds hold in both directions.
func TestExternalOriginBounds(t *testing.T) {
	over := func(mut func(o *ExternalOrigin)) *TextMessage {
		o := &ExternalOrigin{ConnectorKind: "email", Address: "a@b.c"}
		mut(o)
		return &TextMessage{Text: "x", External: o}
	}
	cases := map[string]*TextMessage{
		"kind":    over(func(o *ExternalOrigin) { o.ConnectorKind = strings.Repeat("k", MaxExternalKind+1) }),
		"address": over(func(o *ExternalOrigin) { o.Address = strings.Repeat("a", MaxExternalAddress+1) }),
		"ref":     over(func(o *ExternalOrigin) { o.ExternalRef = strings.Repeat("r", MaxExternalRef+1) }),
		"thread":  over(func(o *ExternalOrigin) { o.ThreadRef = strings.Repeat("t", MaxExternalRef+1) }),
		"flags": over(func(o *ExternalOrigin) {
			o.LossFlags = make([]string, MaxLossFlags+1)
			for i := range o.LossFlags {
				o.LossFlags[i] = "f"
			}
		}),
		"flaglen": over(func(o *ExternalOrigin) { o.LossFlags = []string{strings.Repeat("f", MaxLossFlagLen+1)} }),
	}
	for name, msg := range cases {
		if _, err := msg.Encode(); err == nil {
			t.Errorf("%s: an over-bound origin encoded", name)
		}
	}
}
