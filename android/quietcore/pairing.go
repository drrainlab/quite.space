// The child half of pairing, for the Android host (MD-1/MD-2): the phone is
// usually the SECOND device, and its first run must be able to say "I am the
// same person" instead of minting a stranger. Runs strictly BEFORE Start —
// against an empty data dir — and mirrors the lock screen's flow on desktop:
// start with the offer, surface six digits, wait for the human, write the
// keystore, and then the ordinary Start opens it as a secondary.
package quietcore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/kernel/passcode"
	"github.com/drrainlab/quiet_places/node"
	"github.com/drrainlab/quiet_places/transports/lan"
)

var (
	pairMu      sync.Mutex
	pairStage   string // "" | running | digits | done | failed
	pairDigits  string
	pairFail    string
	pairApprove chan struct{}
)

// HasIdentity reports whether the data dir already holds a keystore — the
// question the first screen asks before offering to pair.
func HasIdentity(dir string) bool { return node.Inspect(dir).HasIdentity }

// PairStart begins the child ceremony against an EMPTY data dir. The offer
// is base64 in any of its dialects, exactly as a human pastes it.
func PairStart(dir, passphrase, offerB64 string) error {
	pairMu.Lock()
	defer pairMu.Unlock()
	if pairStage == "running" || pairStage == "digits" {
		return errors.New("quietcore: a pairing is already in progress")
	}
	offer, err := decodeOfferLoose(offerB64)
	if err != nil {
		return err
	}
	approve := make(chan struct{})
	pairStage, pairDigits, pairFail, pairApprove = "running", "", "", approve
	go func() {
		err := node.JoinAsPairedDeviceVia(dir, []byte(passphrase), offer,
			lan.MulticastAddr, func(digits string) bool {
				pairMu.Lock()
				pairStage, pairDigits = "digits", digits
				pairMu.Unlock()
				<-approve
				return true
			}, uint64(time.Now().Unix()))
		pairMu.Lock()
		if err != nil {
			pairStage, pairFail = "failed", err.Error()
		} else {
			pairStage = "done"
		}
		pairMu.Unlock()
	}()
	return nil
}

// PairState reports the flow as JSON: {stage, digits, error}.
func PairState() string {
	pairMu.Lock()
	defer pairMu.Unlock()
	b, _ := json.Marshal(map[string]string{
		"stage": pairStage, "digits": pairDigits, "error": pairFail,
	})
	return string(b)
}

// PairApprove is the human on THIS screen saying the digits match.
func PairApprove() {
	pairMu.Lock()
	defer pairMu.Unlock()
	if pairStage == "digits" {
		pairStage = "running"
		close(pairApprove)
	}
}

func decodeOfferLoose(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer("\n", "", "\r", "", " ", "", "\t", "").Replace(s)
	if s == "" {
		return nil, errors.New("quietcore: empty offer")
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("quietcore: the offer does not read as base64")
}

// StartOver erases the data dir so this phone can pair as somebody's second
// device — the tool a tester reaches for when an earlier identity is in the
// way. THE GUARDS ARE THE FEATURE:
//
//   - refused while the node is running: a live runtime owns those files,
//     and deleting them under it is corruption with extra steps;
//   - refused mid-ceremony: a pairing in flight holds the very dir;
//   - it deletes CONTENTS, keeping the dir itself, so the caller's next
//     step needs no new paths.
//
// What it destroys is destroyed forever: the identity this phone held, its
// spaces' local history, its remembered passcode. The screen that calls
// this owes the person that sentence BEFORE the tap, not after.
func StartOver(dir string) error {
	openMu.Lock()
	defer openMu.Unlock()
	stateMu.Lock()
	running := rt != nil
	stateMu.Unlock()
	if running {
		return errors.New("quietcore: refusing to erase under a running node — stop it first")
	}
	pairMu.Lock()
	busy := pairStage == "running" || pairStage == "digits"
	if !busy {
		// A finished or failed flow is history; clear it so the next
		// ceremony starts from a clean slate.
		pairStage, pairDigits, pairFail = "", "", ""
	}
	pairMu.Unlock()
	if busy {
		return errors.New("quietcore: a pairing is using this directory — cancel it first")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to erase is the goal state
		}
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// SuggestPassphrase offers one from the project's frozen wordlist — the
// same words the desktop's lock screen suggests, chosen to be read down a
// telephone and typed off paper years later. It only ever OFFERS.
func SuggestPassphrase() string {
	p, err := passcode.Generate()
	if err != nil {
		return ""
	}
	return p
}
