package node

import (
	"bytes"
	"os"
	"testing"

	"github.com/drrainlab/quiet_places/node/llm"
)

// Settings persist across restart with the key encrypted at rest; the API view
// redacts the key; sending an empty key keeps the stored one.
func TestSettingsPersistAndRedact(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	err := rt.SetSettings(Settings{
		Theme: "light", Preset: "daylight", RenderMode: "auto",
		LLM: llm.Config{Provider: "anthropic", Model: "claude", APIKey: "sk-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Close()

	// The key must not sit in cleartext in the on-disk keystore file.
	blob := readKeystoreBytes(t, dir)
	if bytes.Contains(blob, []byte("sk-secret")) {
		t.Fatal("api key found in cleartext on disk")
	}

	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()
	got := rt2.GetSettings()
	if got.Theme != "light" || got.Preset != "daylight" || got.LLM.Provider != "anthropic" {
		t.Fatalf("settings did not persist: %+v", got)
	}
	if got.LLM.APIKey != "sk-secret" {
		t.Fatal("key did not survive restart")
	}
	// API view redacts.
	view := settingsJSON(got)
	llmView := view["llm"].(map[string]any)
	if llmView["has_key"] != true {
		t.Fatal("has_key should be true")
	}
	if _, leaked := llmView["api_key"]; leaked {
		t.Fatal("api view leaked the key")
	}

	// Empty key on update keeps the stored one.
	if err := rt2.SetSettings(Settings{Theme: "dark", LLM: llm.Config{Provider: "anthropic", Model: "claude"}}); err != nil {
		t.Fatal(err)
	}
	if rt2.GetSettings().LLM.APIKey != "sk-secret" {
		t.Fatal("empty-key update wiped the stored key")
	}
}

func readKeystoreBytes(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := osReadFile(dir + "/keys/keystore.enc")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func osReadFile(p string) ([]byte, error) { return os.ReadFile(p) }
