package llm

import (
	"strings"
	"testing"
)

// The provider URL is configured by the person, so it legitimately points
// at a cloud API, at Ollama on localhost, or at a model server on the LAN.
// A general "no internal addresses" rule would therefore be wrong here: it
// would break the local-provider case — the one this project cares about —
// to defend against a caller who already holds the session token.
//
// Link-local is the exception, and the reason is specific: the cloud
// metadata service. Nobody runs a model at 169.254.169.254, and on a node
// hosted on a VPS that address hands back credentials for the account.
func TestALinkLocalProviderIsRefused(t *testing.T) {
	for _, bad := range []string{
		"http://169.254.169.254/latest/meta-data/",      // AWS, GCP, Azure
		"http://169.254.169.254:80/computeMetadata/v1/", //
		"http://[fe80::1]/v1/chat/completions",          // IPv6 link-local
		"http://169.254.1.1/api/chat",                   // the rest of the block
	} {
		if err := refuseLinkLocal(bad); err == nil {
			t.Errorf("%q accepted — that is a metadata probe, not a provider", bad)
		} else if !strings.Contains(err.Error(), "link-local") {
			t.Errorf("%q refused for the wrong reason: %v", bad, err)
		}
	}
}

func TestEveryRealProviderStillWorks(t *testing.T) {
	// Including the local ones. Breaking these to close an SSRF the token
	// already gates would be a bad trade, and it is the trade a blanket
	// internal-address filter makes.
	for _, ok := range []string{
		"https://api.anthropic.com/v1/messages",
		"https://api.openai.com/v1/chat/completions",
		"http://localhost:11434/api/chat", // Ollama, the common case
		"http://127.0.0.1:1234/v1/chat/completions",
		"http://192.168.1.50:11434/api/chat", // a model server on the LAN
		"http://10.0.0.5:8080/v1/chat/completions",
		"http://[::1]:11434/api/chat",
	} {
		if err := refuseLinkLocal(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
}

func TestANameIsNotResolvedHere(t *testing.T) {
	// Resolving would be a TOCTOU check — the dial resolves again and may
	// get a different answer — and a check that looks stronger than it is
	// costs more than it gives. Pinned so nobody "improves" it later.
	if err := refuseLinkLocal("http://metadata.google.internal/computeMetadata/v1/"); err != nil {
		t.Errorf("a name was resolved and refused: %v — see the comment on refuseLinkLocal", err)
	}
}
