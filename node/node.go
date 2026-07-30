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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/kernel/storage"
	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/node/llm"
	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/contract"
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

	spaces    map[id.TerminalID]*spaceState
	assetIdx  *assetIndex
	passes    *passRegistry
	joins     map[string]*joinAttempt
	llmClient *llm.Client // nil → default; injectable for tests

	// agent is the local assistant's participant (AI-0). Its own terminal
	// and device keys, the person's principal as controller.
	agent *terminals.Participant
	// aiPending and aiError are DEVICE-LOCAL. A provider failure is a
	// diagnostic like a delivery state, not something that happened
	// between participants, so it never becomes an event.
	aiPending string
	aiError   string

	// attention is the QuietRank state (lazily loaded, device-local).
	attention *attentionState

	lanNode *lan.Node
	lanPort int
	// mesh is a SUPERVISED link (RB-2): it outlives the device behind it and
	// redials with backoff, so a radio that is unplugged and plugged back in
	// does not leave the node permanently deaf.
	mesh           *meshtastic.Supervised
	meshSupervised bool
	// meshNetworkID scopes which segment's gateway beacons this node listens
	// to; gateways is what it has heard (RB-2). Presence is advisory — it is
	// what a person is SHOWN, never a gate on the queue.
	meshNetworkID string
	// meshChannel is the Meshtastic channel index this node transmits on.
	// 0 is a node's PRIMARY channel, usually the shared public one.
	meshChannel    uint32
	gateways       map[string]*GatewayPresence
	foreignBeacons int
	// radioProfile is what this segment's radios are expected to be set to,
	// so a mismatch can be named rather than merely displayed.
	radioProfile *meshtastic.Profile
	stop         chan struct{}
	stopOnce     sync.Once
	wg           sync.WaitGroup

	// lock is this process's exclusive hold on the data directory. Two nodes
	// on one directory both hold a Keystore and both write it back through
	// tmp+rename: last writer wins, and what it wins is somebody's epoch
	// keys. Held from before the store is unlocked until shutdown completes.
	lock *storage.DirLock

	// relayClk is the SyncClock calibration against a common relay (LR-2).
	relayClk relayClock

	// curLink labels the link currently being pumped (set under r.mu by the
	// pumping goroutine); carried is the bounded eventID→links projection
	// feeding the delivery-route line (ADR-015 §5). Both r.mu-guarded.
	curLink      string
	carried      map[id.EventID][]string
	carriedOrder []id.EventID

	// custodians: pinned bridge custodian keys per link domain (ADR-015
	// §7, TOFU forbidden). r.mu-guarded. custodianPins is the same trust
	// with the metadata a person needs to audit it; custodianWarn holds
	// pins that could not be read, which are NOT trusted but must be seen.
	custodians    map[string][]byte
	custodianPins map[string]CustodianPin
	custodianWarn []string
	// dataDir is where plain, auditable state lives beside the encrypted
	// store — the delivery ledger and the custodian pins.
	dataDir string
	// custodyLapses holds gateways' withdrawals of custody claims —
	// device-local diagnostics, never part of any log or projection.
	custodyLapses map[id.EventID]CustodyLapse
	// ledger tracks outstanding RESPONSIBILITY for events this device
	// authored: who has taken them on, and what has been proven. Never
	// emitted, bundled or relayed.
	ledger *Ledger
	// receiptAudits keeps verified receipts that named a hand-off no longer
	// current, so a stale acknowledgement is debuggable rather than silent.
	receiptAudits map[id.EventID][]ReceiptAudit
	// transportFlap damps Auto-mode transport switching.
	transportFlap map[TransportKind]transportState
	// liveLinks counts adopted links per transport, so route selection can
	// only choose a road that exists.
	liveLinks map[TransportKind]int

	// relaySync is the background relay push/pull loop (nil until first
	// configured). r.mu guards the pointer; the state has its own lock.
	relaySync *relaySyncState

	// relayWants holds blob hashes this node is trying to fetch over the relay
	// (media on-demand when there is no direct peer). The auto-sync push rides
	// these to peers as a request; a holder answers into our inbox. r.mu-guarded.
	relayWants map[id.TerminalID]map[id.Hash]struct{}

	// replyBoxes are PH-1 media reply capabilities, one per space, rotated
	// with the relay bucket. DELIBERATELY NOT PERSISTED: losing one costs a
	// single re-ask, while writing a drain secret into the keystore would buy
	// nothing and widen what a stolen keystore hands over. r.mu-guarded.
	replyBoxes map[id.TerminalID]*replyBox

	// resolvedLinks remembers entrances this device already opened, so
	// backing out of a preview does not lose the way in. Memory only: the
	// pass inside is short-lived, and persisting it would keep a bearer
	// secret for no benefit.
	resolvedLinks map[string]QuickLinkPreview
}

// replyBox is the address a media answer comes back to. We mint the
// capability, publish only its HINT in the want bundle, and drain the box
// ourselves — so a holder can deliver into it and nobody, including the
// relay, can take from it.
type replyBox struct {
	cap    []byte
	prev   []byte // the box we were advertising before the last rotation
	bucket uint64
}

// maxCarried bounds the delivery-route projection (a UI hint, not state).
const maxCarried = 4096

// trackCarried records that an event was handed to a link. Caller holds r.mu.
func (r *Runtime) trackCarried(eid id.EventID, link string) {
	if link == "" {
		return
	}
	if r.carried == nil {
		r.carried = map[id.EventID][]string{}
	}
	links, ok := r.carried[eid]
	for _, l := range links {
		if l == link {
			return
		}
	}
	if !ok {
		r.carriedOrder = append(r.carriedOrder, eid)
		if len(r.carriedOrder) > maxCarried {
			evict := r.carriedOrder[0]
			r.carriedOrder = r.carriedOrder[1:]
			delete(r.carried, evict)
		}
	}
	r.carried[eid] = append(links, link)
}

// CarriedBy reports which links carried an event (delivery-route line).
func (r *Runtime) CarriedBy(eid id.EventID) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.carried[eid]...)
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
	// rejected remembers ingress frames that failed admission so a
	// re-pushed copy is dropped cheaply (PA-1.3). Lazily created.
	rejected *rejectedRing

	// The last projection this replica verified and installed, kept VERBATIM
	// (PH-2/PH-3): a mirror republishes the owner's exact signed bytes, which
	// cannot be reconstructed from the decoded struct without the space key.
	// ingressHints is where this space says contributions go — published by
	// the owner, never derived locally.
	projWire     []byte
	projSeq      uint64
	ingressHints [][]byte
}

// Open unlocks the data root and reconstructs the node: identity from the
// keystore (created on first run), every known space from its log, chains
// resumed so writing continues seamlessly.
func Open(dataDir string, passphrase []byte, displayName string) (rt *Runtime, err error) {
	// All contract registrations happen in package inits, which have run by
	// now — freeze the runtime contract registry (idempotent, LR-0a).
	contract.Freeze()

	// BEFORE the store: a person who launched the app twice should hear
	// "already running" at once, not after a hundred milliseconds of scrypt
	// and a passphrase prompt they never needed to answer.
	lock, err := storage.Lock(dataDir)
	if err != nil {
		return nil, err
	}
	// Every failure below unwinds completely. Without this, Open leaked the
	// store, the ledger and every opened event log on thirteen error paths —
	// invisible until the lock existed, and then instantly the most common
	// bug in the product: mistype your passphrase once, and you are locked
	// out of your own data until you restart the process.
	defer func() {
		if err != nil {
			if rt != nil {
				rt.Close()
			} else {
				_ = lock.Release()
			}
		}
	}()

	root, err := storage.Open(dataDir, passphrase)
	if err != nil {
		return nil, err
	}
	r := &Runtime{root: root, lock: lock, dataDir: dataDir, spaces: map[id.TerminalID]*spaceState{},
		assetIdx: newAssetIndex(), passes: newPassRegistry(),
		joins: map[string]*joinAttempt{}, stop: make(chan struct{}),
		relayWants: map[id.TerminalID]map[id.Hash]struct{}{}}
	rt = r // from here the abort defer unwinds through Close

	// The delivery ledger lives beside the store, not inside the encrypted
	// keystore: it is written on every hand-off and must survive a crash on
	// its own terms.
	lg, err := OpenLedger(dataDir+"/delivery", 0)
	if err != nil {
		return nil, err
	}
	r.ledger = lg
	// Trust in gateways is restored before anything can act on a receipt.
	r.loadCustodians()

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

	// Self participant. A persisted manifest frame (rev chain intact) is
	// authoritative; otherwise mint a fresh one. The persisted display name
	// wins over the launch flag; empty name means onboarding is pending.
	if r.ks.DisplayName != "" {
		displayName = r.ks.DisplayName
	}
	if r.ks.SelfManifestFrame != nil && r.ks.SelfTerminalSeed != nil {
		self, err := terminals.NewParticipantFromManifest(r.Principal, r.Device,
			r.ks.SelfTerminalSeed, r.ks.SelfManifestFrame)
		if err != nil {
			return nil, err
		}
		r.Self = self
	} else {
		self, seed, err := terminals.NewParticipantFrom(r.Principal, r.Device,
			r.ks.SelfTerminalSeed, human.Template(displayName))
		if err != nil {
			return nil, err
		}
		r.Self = self
		if r.ks.SelfTerminalSeed == nil {
			r.ks.SelfTerminalSeed = seed
		}
		r.ks.SelfManifestFrame = self.ManifestFrame
	}
	// The assistant, if this device has one. Before the spaces are opened,
	// so its chain is resumed with the person's (AI-0).
	if err := r.loadAgentLocked(); err != nil {
		return nil, err
	}

	// Reopen every known space: keys first, then the log, so the replay
	// can decrypt (terminals.AttachLog contract). The block hook is set
	// before replay so asset indexes rebuild from the log (plan §10).
	//
	// PA-0 (I1): the VERIFIED MANIFEST is the authority for the
	// private/plaintext decision — SpaceMeta.Visibility is only a repaired
	// cache and can never flip a space's cryptographic mode. A reader
	// replica without a manifest yet stays in bootstrap: no crypto
	// decision, no emits, until the first verified manifest arrives.
	for tid, meta := range r.ks.Spaces {
		s := terminals.Replica(tid)
		s.OnBlock = r.onBlockEvent(tid)
		pol := terminals.SpacePolicy{} // default: private semantics
		manifestKnown := false
		if len(meta.ManifestFrame) > 0 && verifySpaceManifest(tid, meta.ManifestFrame) == nil {
			s.SetManifestFrame(meta.ManifestFrame)
			pol = s.Policy()
			manifestKnown = true
		}
		reader := meta.Role == storage.RoleReader
		s.ReadOnly = reader
		if pol.Effective() == terminals.VisibilityPrivate && !reader {
			// Effective-private (verified private manifest, or the legacy
			// no-manifest member replica): today's encrypted runtime.
			s.EnablePrivate(r.Device)
			s.RestoreEpochs(r.ks.Epochs[tid])
		}
		if meta.Owned {
			if err := s.RestoreController(r.ks.TerminalSeeds[tid], meta.ManifestFrame, meta.Members); err != nil {
				return nil, err
			}
			pol = s.Policy()
			manifestKnown = true
		}
		log, replayed, err := eventlog.Open(tid, root.EventsDir(tid), nil)
		if err != nil {
			return nil, fmt.Errorf("node: reopening space %s: %w", tid, err)
		}
		s.AttachLog(log, replayed)
		if !reader {
			r.Self.ResumeChain(s)
			// The assistant writes with its own device, so it keeps its own
			// chain. Forgetting to resume it forks the log on the first
			// answer after a restart and quarantines the agent forever.
			if r.agent != nil {
				r.agent.ResumeChain(s)
			}
		}
		r.attach(tid, s)
		if !reader {
			// Idempotent: publishes only if the registry lacks our revision
			// (spaces created before manifests traveled, or a bumped manifest).
			if _, _, err := r.Self.PublishManifest(s); err != nil {
				return nil, fmt.Errorf("node: publishing manifest into %s: %w", tid, err)
			}
		}
		// Repair the visibility cache from the verified manifest.
		if manifestKnown {
			if v := string(pol.Effective()); meta.Visibility != v {
				meta.Visibility = v
				r.ks.Spaces[tid] = meta
			}
		}
	}
	// Restore both halves of the join saga AND the intent to act on them:
	// a durable journal whose doors nobody watches is the same bug wearing
	// a different coat (QL-0, ADR-012 invariant 7).
	r.restoreSagaLocked()
	if err := r.saveKeystore(); err != nil {
		return nil, err
	}
	r.ensurePassPolling()
	r.resumeJoinPolling()
	// Resume background relay sync if a relay was configured.
	if s := r.GetSettings(); s.Relay != "" {
		r.applyRelaySync(s.Relay, relayInterval(s))
	}
	return r, nil
}

// manifestTitleOf distinguishes the three cases the old manifestTitle
// flattened into one empty string:
//
//	decode fails             → ("", false)  we do not know
//	decode ok, no labels     → ("", false)  malformed or pre-character
//	decode ok, labels[0]==""  → ("", true)   DELIBERATELY unnamed
//
// Only the third is a space nobody named. Treating the first two as
// "unnamed" would project a member list over a space whose title we simply
// failed to read.
func manifestTitleOf(frame []byte) (string, bool) {
	m, err := manifest.Decode(frame)
	if err != nil || len(m.DeclaredLabels) == 0 {
		return "", false
	}
	return m.DeclaredLabels[0], true
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
	// Delivery ladder (ADR-015 §5): the absorb funnel sees local emits AND
	// synced frames — our OWN events start their honest climb here:
	// created_local always, queued once any transport is live. Both are
	// local claims, never destination proof (ADR-007).
	s.OnAbsorb = func(a eventlog.Applied) {
		if a.Env.Device == r.Device.ID {
			_ = s.Trust.RecordLocal(a.ID, tid, claims.DeliveryCreatedLocal)
			if r.lanNode != nil || r.mesh != nil {
				_ = s.Trust.RecordLocal(a.ID, tid, claims.DeliveryQueued)
			}
			// Responsibility starts here, for events WE authored. Idempotent
			// on the event id, so replaying the log after a restart cannot
			// multiply it or reset an attempt still in flight.
			r.trackOutbound(a.ID, tid, len(a.Frame), time.Now())
		}
	}
	// handed_to_transport: fired by the sync engine when a frames batch is
	// in the endpoint's hands. The link label is set by the pumping
	// goroutine under r.mu (the engine is caller-serialized).
	st.eng.OnSent = func(ids []id.EventID) {
		for _, eid := range ids {
			_ = s.Trust.RecordLocal(eid, tid, claims.DeliveryHandedToTransport)
			r.trackCarried(eid, r.curLink)
		}
		// Bytes in an adapter are not custody. The ledger records that the
		// hand-off left the machine and stops there; only a signed receipt
		// moves it further.
		r.markHandedToTransport(ids, r.curLink, time.Now())
	}
	// Bridge custody ACKs: honored only under a pinned custodian key for
	// the ingress link (custodian.go).
	st.eng.OnCustodyReceipt = func(raw []byte) { r.handleCustodyReceipt(tid, raw) }
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

// abandonForTest models what the operating system does when a process dies:
// it releases the data-directory lock and nothing else. No flush, no graceful
// stop, no ledger close beyond what the caller does itself.
//
// It exists because "simulate a power cut by simply not calling Close" stopped
// being an accurate simulation once the lock existed — in a real power cut the
// PROCESS dies and the kernel drops the lock, whereas a leaked Runtime inside
// a live test process still holds it, correctly. Tests that model a crash use
// this so they model the right thing.
func (r *Runtime) abandonForTest() {
	r.stopOnce.Do(func() { close(r.stop) })
	_ = r.lock.Release()
}

// closeGrace bounds how long shutdown waits for background work. macOS and
// systemd both kill a process that takes too long to exit, and being killed
// mid-keystore-write is precisely the outcome the data-directory lock exists
// to prevent — so we would rather give up on a straggler goroutine than be
// terminated in the middle of a rename.
const closeGrace = 5 * time.Second

// Close stops background work and releases the data directory.
//
// Idempotent: `close(r.stop)` panics on a second call, and shutdown paths run
// twice more often than anyone expects — a window-close callback and an
// application-quit callback, a deferred Close and an explicit one, an aborted
// Open unwinding through the same path a healthy shutdown uses.
func (r *Runtime) Close() {
	r.stopOnce.Do(func() { close(r.stop) })
	if r.lanNode != nil {
		r.lanNode.Close()
	}
	if r.mesh != nil {
		r.mesh.Close() // stop supervising and let go of the device
	}

	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(closeGrace):
		// A loop did not notice the stop signal. Leave the lock HELD and let
		// the process exit: the kernel drops it when our descriptors close,
		// which is atomic with our death. Releasing it here would admit a
		// second instance while a goroutine of ours is still writing.
		return
	}

	r.mu.Lock()
	for _, st := range r.spaces {
		st.space.Log.Close()
	}
	if r.ledger != nil {
		_ = r.ledger.Close()
	}
	r.mu.Unlock()

	// Last, and only on a clean shutdown: everything that writes has stopped.
	_ = r.lock.Release()
}

// ---- Space operations ----

// verifySpaceManifest checks a space manifest frame against the space id:
// decode, signature by the terminal key, and the key IS the id (ADR-001).
func verifySpaceManifest(tid id.TerminalID, frame []byte) error {
	m, err := manifest.Decode(frame)
	if err != nil {
		return err
	}
	if m.Terminal != tid {
		return errors.New("node: manifest belongs to another space")
	}
	if m.Kind != manifest.KindSpace {
		return errors.New("node: not a space manifest")
	}
	return manifest.VerifyFrame(frame, m)
}

// CreateOptions parameterizes space creation (PA-0). The zero value is a
// private space — unchanged pre-PA-0 behavior.
type CreateOptions struct {
	Character terminals.Character
	Policy    terminals.SpacePolicy
}

// CreateSpace mints a private space owned by this node (default character).
func (r *Runtime) CreateSpace(title string) (id.TerminalID, error) {
	return r.CreateSpaceWithCharacter(title, terminals.DefaultCharacter("campfire"))
}

// CreateSpaceWithCharacter mints a private space with a declared character.
func (r *Runtime) CreateSpaceWithCharacter(title string, c terminals.Character) (id.TerminalID, error) {
	return r.CreateSpaceWithOptions(title, CreateOptions{Character: c})
}

// CreateSpaceWithOptions mints a space with a declared character and access
// policy. Public/unlisted spaces skip the epoch machinery entirely: their
// events are signed plaintext (encrypting content whose key is public is
// theatre) — read access is the space id itself.
func (r *Runtime) CreateSpaceWithOptions(title string, o CreateOptions) (id.TerminalID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if o.Character.Archetype == "" {
		o.Character = terminals.DefaultCharacter("campfire")
	}
	// A curated policy always includes the creating device as an attested
	// writer — the owner must be able to publish from day one.
	if o.Policy.Publish == terminals.PublishCurated {
		o.Policy.Writers = append(o.Policy.Writers, terminals.WriterBinding{
			Principal: r.Principal.ID, Device: r.Device.ID,
		})
	}
	s, err := terminals.NewSpaceWithPolicy(title, r.Principal.ID, o.Character, o.Policy)
	if err != nil {
		return id.TerminalID{}, err
	}
	// Rebind the fresh space onto a disk-backed log.
	log, _, err := eventlog.Open(s.ID, r.root.EventsDir(s.ID), nil)
	if err != nil {
		return id.TerminalID{}, err
	}
	s.Log = log
	s.RefreshPolicy()
	private := o.Policy.Effective() == terminals.VisibilityPrivate
	if private {
		s.EnablePrivate(r.Device)
		s.AddMember(r.Device.ID, r.Device.X25519Pub)
		if _, err := r.Self.RotateEpoch(s); err != nil {
			return id.TerminalID{}, err
		}
	}
	seed, err := s.TerminalSeed()
	if err != nil {
		return id.TerminalID{}, err
	}
	r.ks.TerminalSeeds[s.ID] = seed
	r.ks.Spaces[s.ID] = storage.SpaceMeta{
		Title: title, Owned: true,
		ManifestFrame: s.ManifestFrame, Members: s.Members(),
		Visibility: string(o.Policy.Effective()),
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
	// Same rule as the pass path: no invented title (see adoptAccepted).
	title, known := manifestTitleOf(manifestFrame)
	// Keep the manifest with the meta (like the pass path): character and
	// policy must survive restarts on joined replicas too (I1).
	r.ks.Spaces[spaceID] = storage.SpaceMeta{Title: title,
		Unnamed: known && title == "", ManifestFrame: manifestFrame}
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
//
// It locks, because SetName swaps the r.Self.Manifest POINTER under r.mu
// (Participant.Rename copies the manifest and replaces it), and a pointer
// read racing a pointer write is a data race however harmless it looks.
// Call sites already inside a withSpace scope use displayNameLocked instead
// — sync.Mutex is not reentrant, and this is the same locked/*Locked split
// as AssetStatus/assetStatusLocked.
func (r *Runtime) DisplayName() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.displayNameLocked()
}

// displayNameLocked is DisplayName for callers already holding r.mu.
func (r *Runtime) displayNameLocked() string {
	if len(r.Self.Manifest.DeclaredLabels) > 0 {
		return r.Self.Manifest.DeclaredLabels[0]
	}
	return "me"
}

// NeedsOnboarding reports whether the user has not chosen a name yet.
func (r *Runtime) NeedsOnboarding() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ks.DisplayName == ""
}

// SetName records the user's display name (onboarding or rename): it bumps
// the self manifest revision, republishes it into every space so members
// see the new name, and persists it.
func (r *Runtime) SetName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return errors.New("node: name must be 1..64 characters")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	frame, err := r.Self.Rename(name)
	if err != nil {
		return err
	}
	r.ks.DisplayName = name
	r.ks.SelfManifestFrame = frame
	for tid, st := range r.spaces {
		if _, _, err := r.Self.PublishManifest(st.space); err != nil {
			return fmt.Errorf("node: republishing name into %s: %w", tid, err)
		}
	}
	return r.saveKeystore()
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

// canWrite gives a FRIENDLY refusal before an emit is attempted. The
// authoritative gates live below (terminals: ReadOnly emit gate + curated
// log admission) — a handler that forgets this check still cannot write.
func (r *Runtime) canWrite(st *spaceState) error {
	if st.space.ReadOnly {
		return errors.New("node: join this space to write")
	}
	pol := st.space.Policy()
	if pol.Frozen {
		return errors.New("node: this space is frozen — publication is paused")
	}
	if pol.Publish == terminals.PublishCurated {
		for _, w := range pol.Writers {
			if w.Principal == r.Principal.ID && w.Device == r.Device.ID {
				return nil
			}
		}
		return errors.New("node: only the owner and curators publish here")
	}
	return nil
}

// Say posts a text message into a space.
// SayOptions carries the optional edges of a message: a reply pointer and
// the people it addresses. A struct rather than more positional parameters —
// addressing grows, call sites should not.
type SayOptions struct {
	ReplyTo  *id.EventID
	Mentions []id.PrincipalID
}

func (r *Runtime) Say(tid id.TerminalID, text string, opt SayOptions) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	if err := r.canWrite(st); err != nil {
		return id.EventID{}, err
	}
	a, err := human.Say(r.Self, st.space, text,
		human.SayOptions{ReplyTo: opt.ReplyTo, Mentions: opt.Mentions},
		uint64(time.Now().Unix()))
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
	if err := r.canWrite(st); err != nil {
		return id.EventID{}, err
	}
	a, err := r.Self.Emit(st.space, schema, payload,
		r.Self.DefaultAuthorship(), uint64(time.Now().Unix()))
	if err != nil {
		return id.EventID{}, err
	}
	return a.ID, nil
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

// errUnknownSpace is what a scoped read reports when the space is not on this
// device. Handlers map it to 404; it keeps the old wire text.
var errUnknownSpace = errors.New("unknown space")

// withSpace runs fn against a space while r.mu is held, and reports whether
// the space is known.
//
// It replaces an accessor that handed the *terminals.Space back to the caller,
// which was a race BY CONSTRUCTION: the replica is mutated in place by the
// relay-sync goroutine (relaySyncOnce -> PullFromRelay -> AttachSyncApply ->
// Space.absorb -> registry Upsert, all under r.mu), so any projection that
// read the pointer after the lock was released was a concurrent map
// read/write — a fatal, unrecoverable Go error, not merely stale data. Auto
// sync is on by default, so that writer runs in normal operation.
//
// Scoping the pointer to a callback makes the read and the lock the same
// length, and makes an escaping pointer visible at review time rather than
// invisible at the call site.
//
// Two rules for fn, both learned from the call sites this replaced:
//
//   - fn must not call a Runtime method that takes r.mu — sync.Mutex is not
//     reentrant, so that self-deadlocks. Use the *Locked helpers instead
//     (assetStatusLocked, not AssetStatus).
//   - fn should BUILD the response, not write it. Serving JSON inside the
//     callback would hold the lock across a write to the client, letting one
//     slow reader stall relay sync and every other space.
func (r *Runtime) withSpace(tid id.TerminalID, fn func(st *spaceState) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return errUnknownSpace
	}
	return fn(st)
}

// spaceForTest hands out the live replica pointer the way the old exported
// Space() did. It is for tests, which observe a replica between synchronous
// steps; product code must use withSpace instead, since anything read through
// this pointer is read with no lock held. The name is deliberately awkward:
// this is the footgun, kept only where the test is the only goroutine.
func (r *Runtime) spaceForTest(tid id.TerminalID) (*terminals.Space, bool) {
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
	ID    id.TerminalID
	Title string
	// Display is the structured answer to "what is this space called" —
	// either a name somebody chose or a projection the interface renders in
	// the reader's own language (QL-3).
	Display SpaceDisplay
	// Dyad says this space holds exactly one other person, which is what
	// the Navigator's People section means. It is computed STRUCTURALLY,
	// never read off Display: a chosen name outranks the projection, so a
	// rename would otherwise move somebody out of People (NAV-0).
	Dyad bool
	// DisplayTitle is what to put in a list, which is not always the title.
	// A line between two people reads better as the other person's name than
	// as "my line" — and it reads that way on BOTH sides, which a stored
	// title never could, since the title is shared. See lineDisplayTitle.
	DisplayTitle  string
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
		events := st.space.Log.Len()
		if n := st.space.ProjectionFrames; n > events {
			// Reader replicas hold projection frames, not a canonical log.
			events = n
		}
		cards := memberLikes(st.space)
		out = append(out, SpaceInfo{
			ID: tid, Title: meta.Title, Owned: meta.Owned,
			Display: displayFor(spaceNaming{
				Title: meta.Title, LocalTitle: meta.LocalTitle,
				Unnamed: meta.Unnamed,
			}, st.space, r.Principal.ID),
			Dyad: isDisplayDyad(cards, r.Principal.ID),
			Events:        events,
			Messages:      len(st.space.State.Messages()),
			Undecryptable: st.space.Undecryptable,
			Peers:         len(st.conns),
		})
		// DisplayTitle stays as the English rendering: it is what sorts the
		// list (one order per process, not one per locale) and what any
		// client that cannot translate will read.
		out[len(out)-1].DisplayTitle = englishDisplay(out[len(out)-1].Display)
	}
	// Deterministic order: r.spaces is a map, so without this the list would
	// reshuffle on every poll ("jumping"). Title first, id as a stable
	// tiebreak — same order every refresh.
	sort.Slice(out, func(i, j int) bool {
		li, lj := strings.ToLower(out[i].DisplayTitle), strings.ToLower(out[j].DisplayTitle)
		if li != lj {
			return li < lj
		}
		return out[i].ID.Hex() < out[j].ID.Hex()
	})
	return out
}
