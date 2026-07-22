// Package node is the client runtime (M1.B, ADR-011): one process that owns
// the encrypted data root, the user's identity, disk-backed space replicas,
// and LAN sync. The CLI, the local API, and future shells are all thin
// layers over this type — none of them contain protocol logic.
package node

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/kernel/storage"
	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/human"
	"github.com/drrainlab/quiet_places/transports"
	"github.com/drrainlab/quiet_places/transports/lan"
	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

// Runtime is one running node.
type Runtime struct {
	mu sync.Mutex

	root      *storage.Root
	ks        *storage.Keystore
	Principal *identity.Principal
	Device    *identity.Device
	Self      *terminals.Participant

	spaces   map[id.TerminalID]*spaceState
	assetIdx *assetIndex

	lanNode *lan.Node
	lanPort int
	mesh    *meshtastic.Radio
	stop    chan struct{}
	wg      sync.WaitGroup
}

// link is any live transport connection the runtime can pump.
type link interface {
	transports.Endpoint
	Closed() (bool, error)
}

type spaceState struct {
	space *terminals.Space
	eng   *kernelsync.Engine
	conns []link
}

// Open unlocks the data root and reconstructs the node: identity from the
// keystore (created on first run), every known space from its log, chains
// resumed so writing continues seamlessly.
func Open(dataDir string, passphrase []byte, displayName string) (*Runtime, error) {
	root, err := storage.Open(dataDir, passphrase)
	if err != nil {
		return nil, err
	}
	r := &Runtime{root: root, spaces: map[id.TerminalID]*spaceState{},
		assetIdx: newAssetIndex(), stop: make(chan struct{})}

	ks, err := root.LoadKeystore()
	switch {
	case err == nil:
		r.ks = ks
	case errors.Is(err, os.ErrNotExist):
		p, err := identity.NewPrincipal(identity.NewRand())
		if err != nil {
			return nil, err
		}
		d, err := identity.NewDevice(identity.NewRand())
		if err != nil {
			return nil, err
		}
		r.ks = storage.NewKeystore(p, d)
	default:
		return nil, err
	}
	r.Principal, r.Device, err = r.ks.Identity()
	if err != nil {
		return nil, err
	}

	// Self participant: stable terminal key across restarts.
	self, seed, err := terminals.NewParticipantFrom(r.Principal, r.Device,
		r.ks.SelfTerminalSeed, human.Template(displayName))
	if err != nil {
		return nil, err
	}
	r.Self = self
	if r.ks.SelfTerminalSeed == nil {
		r.ks.SelfTerminalSeed = seed
	}

	// Reopen every known space: keys first, then the log, so the replay
	// can decrypt (terminals.AttachLog contract). The block hook is set
	// before replay so asset indexes rebuild from the log (plan §10).
	for tid, meta := range r.ks.Spaces {
		s := terminals.Replica(tid)
		s.OnBlock = r.onBlockEvent(tid)
		s.EnablePrivate(r.Device)
		s.RestoreEpochs(r.ks.Epochs[tid])
		if meta.Owned {
			if err := s.RestoreController(r.ks.TerminalSeeds[tid], meta.ManifestFrame, meta.Members); err != nil {
				return nil, err
			}
		}
		log, replayed, err := eventlog.Open(tid, root.EventsDir(tid), nil)
		if err != nil {
			return nil, fmt.Errorf("node: reopening space %s: %w", tid, err)
		}
		s.AttachLog(log, replayed)
		r.Self.ResumeChain(s)
		r.attach(tid, s)
		// Idempotent: publishes only if the registry lacks our revision
		// (spaces created before manifests traveled, or a bumped manifest).
		if _, _, err := r.Self.PublishManifest(s); err != nil {
			return nil, fmt.Errorf("node: publishing manifest into %s: %w", tid, err)
		}
	}
	if err := r.saveKeystore(); err != nil {
		return nil, err
	}
	return r, nil
}

// manifestTitle extracts the first declared label from a manifest frame.
func manifestTitle(frame []byte) string {
	m, err := manifest.Decode(frame)
	if err != nil || len(m.DeclaredLabels) == 0 {
		return ""
	}
	return m.DeclaredLabels[0]
}

func (r *Runtime) attach(tid id.TerminalID, s *terminals.Space) {
	if s.OnBlock == nil {
		s.OnBlock = r.onBlockEvent(tid)
	}
	st := &spaceState{space: s, eng: kernelsync.NewEngine(s.Log)}
	st.eng.OnApplied = func(a eventlog.Applied) {
		s.AttachSyncApply(a)
		// New epochs may arrive over sync; keep the keystore current.
		if a.Env.Schema == schemas.MembershipEpoch {
			r.persistEpochsLocked(tid, s)
		}
	}
	// Asset exchange: serve only wire ids this space legitimately
	// publishes; accept only what we requested (kernel/sync enforces both).
	st.eng.Blobs = r.root
	st.eng.BlobAllowed = func(h id.Hash) bool { return r.assetIdx.allowed(h, tid) }
	st.eng.OnBlobStored = r.onBlobStored
	r.spaces[tid] = st
}

// saveKeystore persists key material; callers hold r.mu or are in setup.
func (r *Runtime) saveKeystore() error { return r.root.SaveKeystore(r.ks) }

func (r *Runtime) persistEpochsLocked(tid id.TerminalID, s *terminals.Space) {
	r.ks.Epochs[tid] = s.ExportEpochs()
	if meta, ok := r.ks.Spaces[tid]; ok && meta.Owned {
		meta.Members = s.Members()
		r.ks.Spaces[tid] = meta
	}
	_ = r.saveKeystore()
}

// Close stops background work.
func (r *Runtime) Close() {
	close(r.stop)
	if r.lanNode != nil {
		r.lanNode.Close()
	}
	r.wg.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, st := range r.spaces {
		st.space.Log.Close()
	}
}

// ---- Space operations ----

// CreateSpace mints a private space owned by this node (default character).
func (r *Runtime) CreateSpace(title string) (id.TerminalID, error) {
	return r.CreateSpaceWithCharacter(title, terminals.DefaultCharacter("campfire"))
}

// CreateSpaceWithCharacter mints a private space with a declared character.
func (r *Runtime) CreateSpaceWithCharacter(title string, c terminals.Character) (id.TerminalID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := terminals.NewSpaceWithCharacter(title, r.Principal.ID, c)
	if err != nil {
		return id.TerminalID{}, err
	}
	// Rebind the fresh space onto a disk-backed log.
	log, _, err := eventlog.Open(s.ID, r.root.EventsDir(s.ID), nil)
	if err != nil {
		return id.TerminalID{}, err
	}
	s.Log = log
	s.EnablePrivate(r.Device)
	s.AddMember(r.Device.ID, r.Device.X25519Pub)
	if _, err := r.Self.RotateEpoch(s); err != nil {
		return id.TerminalID{}, err
	}
	seed, err := s.TerminalSeed()
	if err != nil {
		return id.TerminalID{}, err
	}
	r.ks.TerminalSeeds[s.ID] = seed
	r.ks.Spaces[s.ID] = storage.SpaceMeta{
		Title: title, Owned: true,
		ManifestFrame: s.ManifestFrame, Members: s.Members(),
	}
	r.attach(s.ID, s)
	if _, _, err := r.Self.PublishManifest(s); err != nil {
		return id.TerminalID{}, err
	}
	r.persistEpochsLocked(s.ID, s)
	return s.ID, nil
}

// MintInvite registers the invitee as a member, produces the invite string,
// and rotates the epoch (ADR-005: every membership change).
func (r *Runtime) MintInvite(tid id.TerminalID, dev id.DeviceID, xpub [32]byte) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return "", errors.New("node: unknown space")
	}
	// The memory policy decides whether newcomers receive history keys:
	// "private_history" invites carry only the current epoch.
	_, character := st.space.Character()
	invite, err := st.space.NewInviteWithHistory(dev, xpub, character.Memory != "private_history")
	if err != nil {
		return "", err
	}
	st.space.AddMember(dev, xpub)
	if _, err := r.Self.RotateEpoch(st.space); err != nil {
		return "", err
	}
	r.persistEpochsLocked(tid, st.space)
	return base64.StdEncoding.EncodeToString(invite), nil
}

// JoinInvite accepts an invite string and opens the space as a disk-backed
// replica. Content arrives later via sync.
func (r *Runtime) JoinInvite(inviteB64 string) (id.TerminalID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := base64.StdEncoding.DecodeString(inviteB64)
	if err != nil {
		return id.TerminalID{}, errors.New("node: invite is not valid base64")
	}
	spaceID, manifestFrame, keys, err := terminals.DecodeInvite(raw, r.Device)
	if err != nil {
		return id.TerminalID{}, err
	}
	if _, exists := r.spaces[spaceID]; exists {
		return spaceID, nil // idempotent join
	}
	s, err := terminals.OpenReplicaAt(spaceID, r.root.EventsDir(spaceID))
	if err != nil {
		return id.TerminalID{}, err
	}
	s.ManifestFrame = manifestFrame
	s.EnablePrivate(r.Device)
	s.RestoreEpochs(keys)
	r.Self.ResumeChain(s)
	title := "joined space"
	if m := manifestTitle(manifestFrame); m != "" {
		title = m
	}
	r.ks.Spaces[spaceID] = storage.SpaceMeta{Title: title}
	r.attach(spaceID, s)
	if _, _, err := r.Self.PublishManifest(s); err != nil {
		return id.TerminalID{}, err
	}
	r.persistEpochsLocked(spaceID, s)
	return spaceID, nil
}

// SetPresence publishes a presence state with a TTL (plan §8.3): after the
// TTL every replica projects it as "last known + age", never as current.
// States that impersonate system properties are refused (character rule).
func (r *Runtime) SetPresence(tid id.TerminalID, state string, ttlSeconds uint64) error {
	if err := terminals.ValidatePresenceState(state); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return errors.New("node: unknown space")
	}
	return human.SetPresence(r.Self, st.space, state, uint64(time.Now().Unix()), ttlSeconds)
}

// DisplayName is this node's own self-declared name (first manifest label).
func (r *Runtime) DisplayName() string {
	if len(r.Self.Manifest.DeclaredLabels) > 0 {
		return r.Self.Manifest.DeclaredLabels[0]
	}
	return "me"
}

// Members projects the member cards of a space.
func (r *Runtime) Members(tid id.TerminalID) ([]terminals.MemberCard, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return nil, errors.New("node: unknown space")
	}
	return st.space.MemberCards(uint64(time.Now().Unix())), nil
}

// Say posts a text message into a space.
func (r *Runtime) Say(tid id.TerminalID, text string) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	a, err := human.Say(r.Self, st.space, text, uint64(time.Now().Unix()))
	if err != nil {
		return id.EventID{}, err
	}
	return a.ID, nil
}

// MakeCard turns a message into a card (vision §8.3, message-to-object).
func (r *Runtime) MakeCard(tid id.TerminalID, title string, origin *id.EventID) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	payload, err := (&schemas.Card{Title: title, Status: "open", Origin: origin}).Encode()
	if err != nil {
		return id.EventID{}, err
	}
	a, err := r.Self.Emit(st.space, schemas.CardCreated, payload,
		r.Self.DefaultAuthorship(), uint64(time.Now().Unix()))
	if err != nil {
		return id.EventID{}, err
	}
	return a.ID, nil
}

// EmitBlock publishes a pre-encoded block payload into a space.
func (r *Runtime) EmitBlock(tid id.TerminalID, schema string, payload []byte) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	a, err := r.Self.Emit(st.space, schema, payload,
		r.Self.DefaultAuthorship(), uint64(time.Now().Unix()))
	if err != nil {
		return id.EventID{}, err
	}
	return a.ID, nil
}

// ReactionSet publishes the DESIRED reaction state (state-based, plan §4).
func (r *Runtime) ReactionSet(tid id.TerminalID, target id.EventID, emoji string, active bool) error {
	payload, err := (&schemas.ReactionBlock{Target: target, Emoji: emoji, Active: active}).Encode()
	if err != nil {
		return err
	}
	_, err = r.EmitBlock(tid, schemas.BlockReaction, payload)
	return err
}

// SetCardStatus updates a card.
func (r *Runtime) SetCardStatus(tid id.TerminalID, card id.EventID, title, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return errors.New("node: unknown space")
	}
	payload, err := (&schemas.Card{Title: title, Status: status, Card: &card}).Encode()
	if err != nil {
		return err
	}
	_, err = r.Self.Emit(st.space, schemas.CardUpdated, payload,
		r.Self.DefaultAuthorship(), uint64(time.Now().Unix()))
	return err
}

// Space returns a replica for read-side projections.
func (r *Runtime) Space(tid id.TerminalID) (*terminals.Space, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return nil, false
	}
	return st.space, true
}

// SpaceInfo is the list projection.
type SpaceInfo struct {
	ID            id.TerminalID
	Title         string
	Owned         bool
	Events        int
	Messages      int
	Undecryptable int
	Peers         int
}

// Spaces lists known spaces.
func (r *Runtime) Spaces() []SpaceInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SpaceInfo, 0, len(r.spaces))
	for tid, st := range r.spaces {
		meta := r.ks.Spaces[tid]
		out = append(out, SpaceInfo{
			ID: tid, Title: meta.Title, Owned: meta.Owned,
			Events:        st.space.Log.Len(),
			Messages:      len(st.space.State.Messages()),
			Undecryptable: st.space.Undecryptable,
			Peers:         len(st.conns),
		})
	}
	return out
}
