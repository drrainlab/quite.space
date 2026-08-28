// Package node is the client runtime (M1.B, ADR-011): one process that owns
// the encrypted data root, the user's identity, disk-backed space replicas,
// and LAN sync. The CLI, the local API, and future shells are all thin
// layers over this type — none of them contain protocol logic.
package node

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/human"
	"github.com/drrainlab/quiet_places/transports"
	"github.com/drrainlab/quiet_places/transports/bundle"
	"github.com/drrainlab/quiet_places/transports/lan"
	"github.com/drrainlab/quiet_places/transports/meshtastic"
	"github.com/drrainlab/quiet_places/transports/radiotransfer"
	"github.com/drrainlab/quiet_places/transports/rnode"
)

// Runtime is one running node.
type Runtime struct {
	mu sync.Mutex

	// notify is AR-1b's notification plane. Its own lock, never r.mu: it is
	// read on the absorb path, which already holds enough of the runtime, and
	// a host arming it must never be able to deadlock against a sync in
	// flight.
	notify notifySink

	// notifyLedger is what survives the process (AR-1b.5.3). It holds no
	// events — only how far the host has confirmed it holds them — because
	// the log is already the durable record and a second copy would be a
	// second source of truth about what happened.
	notifyLedger *notifyLedger

	// previews is the transient post-preview store (PS-3). Its own lock,
	// never r.mu — a preview touches no runtime state by design.
	previews previewStore

	root *storage.Root
	ks   *storage.Keystore
	// PrincipalID is who this node acts as. Always present, on every device.
	PrincipalID id.PrincipalID
	// Principal is the ROOT KEYPAIR, and it is nil on a secondary device
	// (MD-0). Only the authority device can certify a device or revoke one,
	// so anything reaching for this field must first say why it needs to
	// SIGN rather than merely to name — and must handle nil, because on a
	// paired laptop or phone there is nothing here to reach for.
	Principal *identity.Principal
	Device    *identity.Device
	Self      *terminals.Participant
	// ident is the device-certification gate (MD-0). Built in one scan
	// across every local log before any admission decision is taken.
	ident *identityState
	// selfCert is this device's own certificate; certPublished records, per
	// space, WHICH owned devices' certificates that log already carries
	// (MD-0). Keyed by the CERTIFIED device rather than a bool, because this
	// node can own several writers — the person's device, the assistant, the
	// gateway — and each needs its proof in every space it speaks in.
	selfCert      []byte
	certPublished map[id.TerminalID]map[id.DeviceID]bool

	// identityGate arms the certification check (MD-0b). OFF until the hold
	// can act on a Hold verdict, because a gate that precedes what makes it
	// satisfiable refuses every peer — measured, not feared.
	identityGate bool

	// hold is local durable custody for destructively collected ingress
	// (MD-0b). holdMu guards it and the two records beside it rather than
	// r.mu, because the custody phase must not be serialised behind the
	// space map it does not touch.
	holdMu          sync.Mutex
	hold            *storage.IngressHold
	custodyLost     bool
	ingressRefusals []IngressRefusal
	// wantHolds: media answers this node could not route yet (relay.go).
	wantHolds []WantHold
	// routeKnowledgeGen ticks when a stated route displaces a legacy
	// guess (routes.go); the sync loop re-offers legacy-basis deliveries.
	routeKnowledgeGen uint64
	// grants: the identity plane's in-memory half (grants.go, ADR-024).
	grants *grantState
	// knocks: requests for a conversation waiting for this person to
	// answer (knock.go, ADR-027). In memory by design — a knock is a
	// question, not a record, and one nobody answers simply expires.
	knocks *knockState
	// Reconsideration is COALESCED, never recursive: a held frame may itself
	// be the control event that changes admission state again.
	// ingressArmed stays false throughout Open, so every change applied
	// during replay collapses into the one startup pass.
	reconsiderDirty   bool
	reconsiderRunning bool
	// lastPassReleased is what earns one overshooting drain when the hold is
	// full: progress, and nothing else, keeps a destructive collect allowed.
	lastPassReleased    bool
	ingressArmed        bool
	startupReconsidered chan struct{}

	spaces    map[id.TerminalID]*spaceState
	assetIdx  *assetIndex
	passes    *passRegistry
	joins     map[string]*joinAttempt
	llmClient *llm.Client // nil → default; injectable for tests

	// agent is the local assistant's participant (AI-0). Its own terminal
	// and device keys, the person's principal as controller.
	agent *terminals.Participant
	// gateway is the external-boundary participant (TR-0); nil until the
	// first connector projection mints it. gatewayShown tracks which spaces
	// already carry its manifest this process. connectors holds each
	// connector's journal + projector latch, keyed by connector id.
	gateway      *terminals.Participant
	gatewayShown map[id.TerminalID]bool
	connectors   map[string]*connState
	// aiPending and aiError are DEVICE-LOCAL. A provider failure is a
	// diagnostic like a delivery state, not something that happened
	// between participants, so it never becomes an event.
	aiPending string
	aiError   string

	// attention is the QuietRank state (lazily loaded, device-local).
	attention *attentionState

	lanNode *lan.Node
	lanPort int
	// lanStop ends the LAN lifecycle started by StartLAN without touching
	// the rest of the runtime (StopLAN; the room emptied, the node lives).
	lanStop chan struct{}
	// lanPeers is the OBSERVED route table (T6-LAN): device → the live
	// local link that device is authenticated on. Ephemeral by doctrine —
	// bound to an authenticated DeviceID, valid only while the link lives,
	// never written to the keystore. The delivery loop consults it to
	// suppress relay copies that a live local wire makes redundant.
	lanPeers map[id.DeviceID]link
	// mesh is a SUPERVISED link (RB-2): it outlives the device behind it and
	// redials with backoff, so a radio that is unplugged and plugged back in
	// does not leave the node permanently deaf.
	mesh           *meshtastic.Supervised
	meshSupervised bool
	// rnodeRadio and rnodeEP are set when the attached radio is an RNode
	// modem rather than a Meshtastic node. One radio per node either way.
	rnodeRadio *rnode.Radio
	rnodeEP    *radiotransfer.Endpoint
	// rnodeLink is the adopted link itself, kept so DetachRadio can take it
	// back out of the registry. Without it a detached radio would keep a
	// corpse wired into every space's connection list.
	rnodeLink link
	// tooLarge is what a carrier declined to carry, so a person can be told
	// rather than left watching a message that never goes.
	tooLarge tooLargeSet
	// radioRestoreErr is why the remembered radio did not come back on start.
	// Kept so the status can SAY it: an unplugged board and a board that was
	// never configured look identical without this.
	radioRestoreErr error
	// radioHost is set only where the node cannot open hardware itself.
	radioHost RadioHost
	// meshNetworkID scopes which segment's gateway beacons this node listens
	// to; gateways is what it has heard (RB-2). Presence is advisory — it is
	// what a person is SHOWN, never a gate on the queue.
	meshNetworkID string
	// meshChannel is the Meshtastic channel index this node transmits on.
	// 0 is a node's PRIMARY channel, usually the shared public one.
	meshChannel uint32
	// meshReliable asks the radio for retransmission (want_ack). Set true
	// in Open; see SetMeshReliable for the measurement behind that.
	meshReliable bool
	// meshSeed is the SEGMENT SEED for Radio Transfer: every radio in a
	// segment derives the same frame-authentication key from it. Held here
	// rather than in the keystore because MR-2 replaces it with the seed
	// carried in the ordinary Quiet invite, and persisting an interim shape
	// is how an interim shape becomes permanent.
	meshSeed []byte
	// meet is what has been heard on the radio segment: neighbours who
	// announced themselves, and invitations waiting for an answer. Memory
	// only and bounded — this is a room somebody walked into, not a
	// directory.
	meet           *radioMeet
	gateways       map[string]*GatewayPresence
	foreignBeacons int
	// radioProfile is what this segment's radios are expected to be set to,
	// so a mismatch can be named rather than merely displayed.
	radioProfile *meshtastic.Profile
	stop         chan struct{}
	// syncKick wakes the relay sync loop out of turn. Buffered one deep:
	// a kick while one is pending is the same kick. Sent when somebody
	// opens media — the want should leave NOW, not on the next tick; the
	// measured cost of politely waiting was the first third of every
	// "фото ооочень медленно стартовало".
	syncKick chan struct{}
	// backgrounded is 1 while no person is looking (node/foreground.go).
	// An atomic rather than a field under r.mu: read on every loop tick,
	// including ticks that deliberately avoid the runtime lock.
	backgrounded atomic.Int64
	// rideAhead: assets whose BYTES should ride the next background push,
	// once. Armed at the moment of sending — the one moment the sender is
	// certainly awake — so a recipient's fetch finds the bytes already in
	// its mailbox instead of having to wake a phone that has gone back
	// into a pocket. One-shot and bounded (rideAheadMaxBytes); anything
	// bigger, and any re-offer, travels on demand as before.
	rideAhead map[id.TerminalID]map[AssetKey]struct{}
	// instruments are the attached instrument participants (QI-1),
	// keyed by InstrumentID.
	instruments map[id.TerminalID]*instrumentRuntime
	// DevIngest opens the dev-only frame ingest for EXTERNAL instruments
	// (QI-M3 stand). Off by default; a bearer decision is not made here.
	DevIngest bool
	stopOnce  sync.Once
	wg        sync.WaitGroup

	// lock is this process's exclusive hold on the data directory. Two nodes
	// on one directory both hold a Keystore and both write it back through
	// tmp+rename: last writer wins, and what it wins is somebody's epoch
	// keys. Held from before the store is unlocked until shutdown completes.
	lock *storage.DirLock

	// relayChunkAt is where each relay's next drain begins (RR). A cycle that
	// always started at the first chunk would re-serve the same spaces forever
	// and let the tail starve behind one that keeps failing.
	relayChunkAt map[string]int

	// relayWaitUntil is when each relay may be asked again, from its own
	// retry-after. Asking sooner is precisely what it refused.
	relayWaitUntil map[string]time.Time

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

	// links are the adopted links THEMSELVES, with the filter each was
	// adopted under — so a space created after adoption can be wired into
	// them (see attach). Without this a radio, which is adopted exactly
	// once at startup, carried only the spaces that already existed then.
	// r.mu-guarded.
	links []liveLink

	// relaySync is the background relay push/pull loop (nil until first
	// configured). r.mu guards the pointer; the state has its own lock.
	relaySync *relaySyncState

	// relayPool holds the persistent relay connections (RR-2): two lanes
	// per address, health, backoff. Created lazily under poolOnce; the
	// pointer is atomic because Close reads it without the Once (see pool).
	relayPoolV atomic.Pointer[relayPool]
	poolOnce   sync.Once

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
	// throttled counts frames DEFERRED by the owner's contribution limit
	// (IC-1), in memory, r.mu-guarded.
	//
	// Deliberately not PolicyStats.IgnoredTotal, which means "refused for
	// good": a throttled frame is coming back, so it would be counted again
	// every cycle it waits, and one loud contributor would flush the 64-entry
	// rejection ring of every real policy refusal. An owner who sets a limit
	// needs to see it working; they do not need it to look like censorship.
	throttled uint64

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
		notifyLedger: newNotifyLedger(dataDir),
		assetIdx:     newAssetIndex(), passes: newPassRegistry(),
		joins: map[string]*joinAttempt{}, stop: make(chan struct{}),
		syncKick:            make(chan struct{}, 1),
		startupReconsidered: make(chan struct{}),
		relayWants:          map[id.TerminalID]map[id.Hash]struct{}{},
		// Radios ask for retransmission unless told otherwise. See
		// SetMeshReliable: without it, nothing arrived at all on a shared
		// channel, and a person should not have to find a setting before
		// their messages can land.
		meshReliable: true}
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
	r.PrincipalID, r.Principal, r.Device, err = r.ks.Identity()
	if err != nil {
		return nil, err
	}
	r.ident = newIdentityState()
	// The authority device certifies itself once, so that everything else
	// in ADR-002 has something to stand on. A secondary never reaches here:
	// Keystore.Identity refuses to open a keystore with no root seed and no
	// certificate rather than let one proceed uncertified.
	selfCert, selfCertNew, err := selfCertify(r.ks, r.Principal, r.Device,
		uint64(time.Now().Unix()))
	if err != nil {
		return nil, err
	}
	if c, er := identity.DecodeCertificate(selfCert); er == nil {
		_ = r.ident.store.AddCertificate(c)
	}
	// AND EVERY CERTIFICATE THE KEYSTORE HOLDS — not only our own. The
	// store used to be fed from self plus whatever the space logs replayed,
	// which quietly meant a paired child knew its sibling only after they
	// met inside some shared space. A child whose parent owned no spaces
	// never met it at all: ks.Certs said "this is my sibling" and the
	// identity store said "never heard of it", so ADR-024's grants could
	// not be sealed and convergence died at the first offline window —
	// caught by the 1B gate, step 4. The keystore's records ARE the
	// person's certified set; the store loads them like it loads the logs'.
	for _, rec := range r.ks.Certs {
		if c, er := identity.DecodeCertificate(rec.Frame); er == nil {
			_ = r.ident.store.AddCertificate(c)
		}
	}
	for _, rec := range r.ks.Revs {
		if rv, er := identity.DecodeRevocation(rec.Frame); er == nil {
			_ = r.ident.store.AddRevocation(rv)
		}
	}
	_ = selfCertNew
	r.selfCert = selfCert
	r.certPublished = map[id.TerminalID]map[id.DeviceID]bool{}
	identSeen := map[storage.LegacyBinding]bool{}
	var identLogs []*eventlog.Log

	// Self participant. A persisted manifest frame (rev chain intact) is
	// authoritative; otherwise mint a fresh one. The persisted display name
	// wins over the launch flag; empty name means onboarding is pending.
	if r.ks.DisplayName != "" {
		displayName = r.ks.DisplayName
	}
	if r.ks.SelfManifestFrame != nil && r.ks.SelfTerminalSeed != nil {
		self, err := terminals.NewParticipantFromManifest(r.PrincipalID, r.Device,
			r.ks.SelfTerminalSeed, r.ks.SelfManifestFrame)
		if err != nil {
			return nil, err
		}
		r.Self = self
	} else {
		self, seed, err := terminals.NewParticipantFrom(r.PrincipalID, r.Device,
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
	if err := r.loadGatewayLocked(); err != nil {
		return nil, fmt.Errorf("node: loading gateway terminal: %w", err)
	}
	if err := r.loadAgentLocked(); err != nil {
		return nil, err
	}
	// The assistant and the gateway each hold their OWN device key and their
	// own chain while sharing the person's principal — precisely the
	// multi-device case ADR-002 describes. The gate found them the moment it
	// went on, which is the gate working: an uncertified device is refused
	// however local it is.
	nowSec := uint64(time.Now().Unix())
	if r.agent != nil {
		certifyOwnDevice(r.ks, r.ident, r.Principal, r.agent.Device, nowSec)
	}
	if r.gateway != nil {
		certifyOwnDevice(r.ks, r.ident, r.Principal, r.gateway.Device, nowSec)
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
			s.RestoreInstrumentEpochs(r.ks.InstrEpochs[tid])
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
		// PASS 1 (MD-0). No identity gate is installed yet, anywhere: the
		// scan must finish across EVERY local log before one admission
		// decision is taken, or the answer depends on which space opened
		// first. See node/identity_admit.go for the failure that shape has.
		for _, a := range replayed {
			r.ident.observe(a.Env)
			identSeen[storage.LegacyBinding{
				Principal: a.Env.Principal, Device: a.Env.Device,
			}] = true
			if a.Env.Schema == schemas.DeviceCertified {
				// Mark by the CERTIFIED device, read from the payload. An
				// older sealed certificate does not decode here and counts
				// as absent — re-published once, in plaintext, which is the
				// migration this wants anyway.
				if c, er := identity.DecodeCertificate(a.Env.Payload); er == nil {
					r.markCertPublished(tid, c.Device)
				}
			}
		}
		identLogs = append(identLogs, log)
		s.AttachLog(log, replayed)
		if !reader {
			r.Self.ResumeChain(s)
			// The assistant writes with its own device, so it keeps its own
			// chain. Forgetting to resume it forks the log on the first
			// answer after a restart and quarantines the agent forever.
			if r.agent != nil {
				r.agent.ResumeChain(s)
			}
			// The gateway is a third writer with the same obligation.
			if r.gateway != nil {
				r.gateway.ResumeChain(s)
			}
		}
		r.attach(tid, s)
		if !reader {
			// Idempotent: publishes only if the registry lacks our revision
			// (spaces created before manifests traveled, or a bumped manifest).
			//
			// A HOLD-CLASS REFUSAL DOES NOT STOP THE OPEN. Measured on the
			// first paired phone: a curated space keys its writers on the
			// (principal, DEVICE) pair, a fresh secondary is not in that list
			// yet, and one such space bricked the whole device at its first
			// open. Not being allowed to WRITE somewhere is an ordinary state
			// for a device — reading works, and the authorising revision
			// arrives like any other — so it is a diagnostic here, never a
			// refusal to start.
			if _, _, err := r.Self.PublishManifest(s); err != nil {
				if !holdClassRefusal(err) {
					return nil, fmt.Errorf("node: publishing manifest into %s: %w", tid, err)
				}
				r.noteIngressRefusal(IngressRefusal{
					Space: tid, Reason: "self_manifest_deferred", Detail: err.Error(),
				})
			}
			r.publishCertLocked(s)
		}
		// Repair the visibility cache from the verified manifest.
		if manifestKnown {
			if v := string(pol.Effective()); meta.Visibility != v {
				meta.Visibility = v
				r.ks.Spaces[tid] = meta
			}
		}
	}
	// PASS 2 (MD-0). The scan is complete, so admission is now a decision
	// nothing about ordering can change. The legacy allowlist is frozen here
	// ONCE — on the first open of a keystore that predates certification —
	// and never appended to afterwards.
	//
	// A SECONDARY NEVER FREEZES ITS OWN. It inherits the authority's list in
	// the pairing freight; a secondary that could freeze one from whatever
	// history it happens to have replayed would be a device minting trust for
	// strangers — the open door the freeze exists to close, one hop away.
	if r.Principal != nil && len(r.ks.LegacyBindings) == 0 && len(identSeen) > 0 {
		r.ks.LegacyBindings = r.ident.freezeLegacy(identSeen)
	} else {
		r.ident.loadLegacy(r.ks.LegacyBindings)
	}
	// THE GATE GOES ON HERE, and only here: the scan is complete, the legacy
	// allowlist is frozen, the authority has certified itself, and every space
	// this node writes to carries that certificate. Enforcement never precedes
	// the thing that makes it satisfiable — measured the hard way, see the git
	// history of this line.
	//
	// The sixth precondition, the one that kept this off, is now met too:
	// nothing ORDERS a peer's certificate before its first message, and a
	// refused frame used to be lost rather than delayed because the relay's
	// Collect is destructive. MD-0b closed that — local durable custody holds
	// the bytes and re-judges them when the prerequisite is applied — so the
	// refusal is a delay on every path again, which is the condition this line
	// was waiting for.
	//
	// It is a FLAG rather than an install because the log gate is wired at
	// attach, long before this point, and must stay open through the replay
	// above: PASS 2 replays local history whose allowlist is only frozen a few
	// lines up.
	//
	// The seventh precondition was found BY the sixth, and it is why this
	// line stayed off for one more round: a deadlock inside one chain. A
	// device's certificate used to ride sealed at seq 3, behind the epoch at
	// seq 1 that needed it — held forever, because nothing else was ever
	// going to arrive. Decision C resolved it: the certificate is one object
	// in two roles. As ADMISSION PROOF it travels plaintext (sealForEmit) and
	// is learned at the log's door the moment it arrives, free of chain
	// ordering; as a LOG RECORD it still applies in ordinary order for
	// convergence and audit. Chain position is now canonical tidiness, never
	// the thing holding the security model up.
	r.identityGate = true
	_ = identLogs
	// SD-0: finish any deletion the last process did not live to finish.
	// Before the saga is restored and before anything is served: events on
	// disk for a space nobody lists are the messages somebody asked to be
	// rid of, still readable by anything holding the key.
	r.sweepForgotten()
	// Restore both halves of the join saga AND the intent to act on them:
	// a durable journal whose doors nobody watches is the same bug wearing
	// a different coat (QL-0, ADR-012 invariant 7).
	r.restoreSagaLocked()
	// RT-0 legacy migration, exactly once: a node upgraded from the
	// single-relay era gets one recorded route per known member device —
	// the relay everybody genuinely shared — so yesterday's installations
	// keep talking. Runs before the save below so it rides the same write;
	// a fresh install has no spaces and records nothing.
	r.backfillLegacyRoutesLocked()
	if err := r.saveKeystore(); err != nil {
		return nil, err
	}
	r.ensurePassPolling()
	r.resumeJoinPolling()
	// Connector journals restore their intent to act the same way the join
	// saga does: reopened and re-driven, never merely present on disk.
	r.resumeConnectors()
	r.restoreInstruments()
	// The radio this device was last attached to, brought back on its own.
	// Best effort by design — see restoreRadio: a radio that is unplugged, or
	// that came back under a different serial path, must never stop somebody
	// opening their own data.
	r.restoreRadio()
	// MD-0b STARTUP RECONSIDER, and its position is the whole point: identity
	// is built, every space is replayed and attached, membership and policy
	// projections are all reconstructed — and no destructive collection has
	// started yet. Running it any earlier would judge a held frame against
	// half a world (its space not replayed yet) and there would be no later
	// trigger, because the prerequisite it waits for is already in the log
	// rather than still on its way.
	r.armIngressReconsider()
	// Resume background relay sync. Automatic mode (RR-3) resolves its
	// primary from measurements in the background — unlock never waits on
	// a probe; custom mode uses exactly the configured address.
	if s := r.GetSettings(); relayIsAutomatic(s) {
		r.startAutomaticRelay(relayInterval(s))
	} else if s.Relay != "" {
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
	// AR-1b.5.6: a space the notification plane has never seen starts from
	// where it is now. Without this, a room joined after activation has no
	// watermark, and the first restart redelivers its entire imported history
	// as if nobody had ever been told.
	if r.notifyLedger != nil {
		r.notifyLedger.baselineIfUnknown(tid, s.Log.Summary())
	}
	// Links adopted BEFORE this space existed still carry it: media fetch
	// and the peer count read st.conns, and a space created during the
	// session must not look peerless on a radio that is right there.
	r.wireLiveLinksLocked(tid, st)
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
		// MD-0: a certificate that ARRIVES must become trust, not merely
		// storage. The scan at Open builds the store from history and then
		// never runs again, so without this line a peer's certificate lands
		// in the log, is never learned, and every message from that peer is
		// refused by a node that is holding the proof it says it lacks.
		// Found by TR-0's headless terminal: it joined, published its
		// certificate, restarted, spoke — and the message never arrived.
		r.ident.observe(a.Env)
		// MD-0b: the state has now actually CHANGED, so whatever waits in the
		// ingress hold may have become admissible. Hooked HERE rather than in
		// RevisePolicy or Certify because this funnel sees the REMOTE path too
		// — a revision arriving over a relay calls no local authoring
		// function, and that is exactly the reorder that puts a message ahead
		// of the event authorising it.
		if admissionRelevantSchema(a.Env.Schema) {
			r.admissionStateChanged()
		}
		// AR-1b: the notification plane, and it is DISARMED during Open's
		// replays — a host cannot arm it until Open has returned, so history
		// is unable to notify by construction rather than by a filter.
		r.notifyAbsorbed(tid, s, a)
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
	// A refusal must never be a silence.
	//
	// Sync now declines to put an event on a carrier that says it will not
	// carry it — which is the whole point of this gate — but a message that
	// silently does not go is WORSE than the jam it replaces: a jam ends
	// eventually, and a silence is indistinguishable from a message nobody
	// sent. So the refusal is recorded where the ledger already has the right
	// word for it, and the honest surface (task #145) reads it from there.
	st.eng.OnTooLarge = func(eid id.EventID, size, ceiling int) {
		log.Printf("radio: %s is %d bytes and this carrier carries %d — "+
			"it waits for a wider path", eid, size, ceiling)
		r.noteTooLargeForCarrier(eid, tid, size, ceiling)
	}
	// Bridge custody ACKs: honored only under a pinned custodian key for
	// the ingress link (custodian.go).
	st.eng.OnCustodyReceipt = func(raw []byte) { r.handleCustodyReceipt(tid, raw) }
	// MD-0b decision C, on the SYNC path (LAN, radio): the batch's own proofs
	// become trust before its frames are judged, and a hold-class refusal is
	// taken into durable custody instead of dropped. The second half matters
	// most on radio — a completed transfer is remembered by the receiver and
	// never re-delivered upward, so a dropped refusal there is a LOSS, the
	// same shape the relay's destructive Collect had.
	st.eng.OnBatch = r.learnBundleProofs
	st.eng.OnRefused = func(f []byte, err error) {
		if !errors.Is(err, ErrAdmissionHold) {
			return // permanent refusals stay refused; ordering stays the log's
		}
		hold, herr := r.ingressHold()
		if herr != nil {
			return // custody already lost or unopenable; latched elsewhere
		}
		raw := append([]byte(nil), f...)
		if _, perr := hold.Put(bundle.Encode(tid, [][]byte{raw}),
			storage.HeldIngressMeta{ReceivedAt: time.Now().Unix()}); perr != nil {
			// SAME catastrophic class as a failed Put after a relay Collect:
			// the radio receiver has marked this transfer complete and will
			// never deliver it upward again, so bytes we cannot durably keep
			// are bytes nobody holds. Latch, do not shrug.
			r.noteCustodyLost()
			r.noteIngressRefusal(IngressRefusal{
				Space: tid, Reason: "ingress_custody_lost", Detail: perr.Error(),
			})
		}
	}
	// Asset exchange: serve only wire ids this space legitimately
	// publishes; accept only what we requested (kernel/sync enforces both).
	st.eng.Blobs = r.root
	st.eng.BlobAllowed = func(h id.Hash) bool { return r.assetIdx.allowed(h, tid) }
	st.eng.OnBlobStored = r.onBlobStored
	// MD-0b: authorisation state, at the ONE point where it becomes current.
	// A policy revision is a signed manifest frame rather than a log event on
	// the local path, so watching the absorb funnel alone would catch a
	// revision arriving over a relay and silently miss the owner's own —
	// measured, ingress_reconsider_test.go failed exactly that way.
	s.OnPolicyChanged = r.admissionStateChanged
	// MD-0: the certification gate, on its OWN layer rather than SetAdmit —
	// curated policy owns that slot and rewrites it on every policy change,
	// so a space going curated → community would silently switch certificate
	// checking off. This one runs first: "this device may not speak" is a
	// stronger and more general answer than "not here".
	//
	// EVERY PATH, not just the relay. LAN and radio ingress reach the log
	// without passing through the ingress hold, and a gate that only covers
	// one transport is not a gate — an attacker would simply choose the other.
	// It is safe here because at the LOG a refusal is a delay (measured:
	// kernel/eventlog/refusal_test.go), and the one path where it was a LOSS
	// is the destructive relay drain, which is exactly what MD-0b now holds.
	//
	// The flag is read dynamically so the replay inside Open — which runs
	// before the allowlist is frozen — is not judged by a half-built store.
	st.space.Log.SetIdentityAdmit(func(env *signal.Envelope) error {
		// BOOTSTRAP PROOF, LEARNED AT THE DOOR (MD-0b, decision C). A
		// certificate has two roles carried by one byte-identical object:
		// admission proof, available BEFORE encrypted admission and free of
		// chain ordering; and a replicated log record, applied in ordinary
		// order for convergence and audit. This is the first role. The frame
		// signature is already verified when this gate runs, the payload is
		// plaintext by design (see sealForEmit), and observe() verifies the
		// ROOT signature before any of it becomes trust — so learning here
		// grants nothing the certificate's own signature does not grant, and
		// it breaks the measured deadlock: trust no longer waits behind the
		// chain position of its own proof. A sealed certificate from an older
		// peer simply fails to decode here and is still learned at absorb,
		// exactly as before.
		switch env.Schema {
		case schemas.DeviceCertified, schemas.DeviceRevoked:
			r.ident.observe(env)
			r.admissionStateChanged()
		}
		if !r.identityGate {
			return nil
		}
		return r.ident.admit(env)
	})
	r.spaces[tid] = st
	// MD-0b: MEMBERSHIP IS ADMISSION STATE TOO, and it does not arrive as an
	// event — it is this map gaining an entry. A bundle held because we were
	// not in that space becomes admissible the moment we are, whether that
	// came from a join saga, a create, or a replay. Measured:
	// ingress_membership_probe_test.go.
	r.admissionStateChanged()
}

// saveKeystore persists key material; callers hold r.mu or are in setup.
func (r *Runtime) saveKeystore() error {
	// A DYING RUNTIME DOES NOT WRITE. Close waits closeGrace for the
	// background loops, then leaves — and a sync pass stuck in a dial to
	// a dead relay outlives that grace, finishes its round, and used to
	// write the keystore straight under whoever was removing the data
	// directory (the suite's TempDir cleanup caught it in the act).
	// Everything a skipped save loses — a learned route, a noted deadline
	// — is knowledge the next run re-earns; a write racing teardown is a
	// corruption nobody re-earns anything from.
	select {
	case <-r.stop:
		return nil
	default:
	}
	return r.root.SaveKeystore(r.ks)
}

// stopped reports whether Close has been asked for. Long passes consult
// it at section boundaries so a shutdown does not wait out a full round.
func (r *Runtime) stopped() bool {
	select {
	case <-r.stop:
		return true
	default:
		return false
	}
}

func (r *Runtime) persistEpochsLocked(tid id.TerminalID, s *terminals.Space) {
	r.ks.Epochs[tid] = s.ExportEpochs()
	if ie := s.ExportInstrumentEpochs(); len(ie) > 0 {
		r.ks.InstrEpochs[tid] = ie
	}
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
	// Preview fetchers watch r.stop too; closeAll additionally releases
	// their memory budgets so a long-lived process (tests, the desktop
	// shell reopening) does not leak the global cap.
	r.previews.closeAll()
	if p := r.relayPoolV.Load(); p != nil {
		p.closeAll()
	}

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
	// Journals close only after every projector goroutine has stopped.
	r.closeConnectors()

	// The notification watermark is written back here rather than on every
	// acknowledgement: acks arrive as often as events during a catch-up, and
	// a late one costs a redelivery the host deduplicates away, while an
	// early file rewrite would cost the sync path.
	if r.notifyLedger != nil {
		r.notifyLedger.flush()
	}

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
	// LocalOnly marks a space that must never be announced on a LAN,
	// published to a relay, mirrored, or made the target of a link — the
	// local Agent Terminal's room (AI-0).
	//
	// It is an OPTION rather than a later write on purpose. Stamping it
	// afterwards left a window: CreateSpaceWithOptions attaches the space
	// to r.spaces, and the sync and announce loops read that map, so a
	// tick landing between the attach and the stamp would have seen the
	// AI's space without its flag and derived a mailbox hint for it.
	// Sealed content, so at most the hint's existence leaked — but a
	// negative guarantee with a window in it is not one. LocalOnly from
	// birth, in the same critical section that creates the space.
	LocalOnly bool
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
			Principal: r.PrincipalID, Device: r.Device.ID,
		})
	}
	s, err := terminals.NewSpaceWithPolicy(title, r.PrincipalID, o.Character, o.Policy)
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
		r.expandMembersLocked(s)
		if _, err := r.Self.RotateEpoch(s); err != nil {
			return id.TerminalID{}, err
		}
		if _, err := r.Self.RotateInstrumentEpoch(s); err != nil {
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
		// Before attach, deliberately — see CreateOptions.LocalOnly.
		LocalOnly: o.LocalOnly,
	}
	r.attach(s.ID, s)
	if _, _, err := r.Self.PublishManifest(s); err != nil {
		return id.TerminalID{}, err
	}
	r.publishCertLocked(s)
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
	// The invitee's siblings ride in on the same rotation (ADR-024): the
	// person was invited, and the person is all of their devices.
	r.expandMembersLocked(st.space)
	if _, err := r.Self.RotateEpoch(st.space); err != nil {
		return "", err
	}
	// The instrument lineage turns with every membership change
	// (owner's amendment 5) — a new member must read the greenhouse too,
	// and a removed one must stop.
	if _, err := r.Self.RotateInstrumentEpoch(st.space); err != nil {
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
	r.publishCertLocked(s)
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

// IsFirstRun reports whether there is NOTHING HERE YET — no chosen name and no
// spaces. It is deliberately a different question from NeedsOnboarding, and
// keeping them apart is the fix for a trap AR-0d found on a phone.
//
// The client used "no name chosen" to decide whether to throw up the modal
// first-run wall. But a node can hold an identity, eight spaces and an open
// conversation and still have no display name — every node opened by a shell
// or an embedding host does, because a display name is chosen in the UI and
// nowhere else. Those people got the whole welcome flow dropped over a working
// interface, and on a phone, where there is no Esc, they could not get out of
// it.
//
// So: "has this person chosen a name" is a nudge, and "is there nothing here
// yet" is a welcome. Only the second is allowed to take the screen.
func (r *Runtime) IsFirstRun() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ks.DisplayName == "" && len(r.spaces) == 0
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
		r.publishCertLocked(st.space)
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
			if w.Principal == r.PrincipalID && w.Device == r.Device.ID {
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
	// ObjectRefs: the domain objects this message is about (SP-2.1).
	ObjectRefs [][16]byte
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
		human.SayOptions{ReplyTo: opt.ReplyTo, Mentions: opt.Mentions,
			ObjectRefs: opt.ObjectRefs},
		uint64(time.Now().Unix()))
	if err != nil {
		return id.EventID{}, err
	}
	return a.ID, nil
}

// CardOptions carries a new task's optional edges. A struct, not more
// positional parameters — addressing grows, call sites should not.
type CardOptions struct {
	Origin   *id.EventID
	ObjectID *[16]byte // SP-1: the domain object this task belongs to
	Assignee *id.PrincipalID
}

// MakeCard creates a task card (vision §8.3; SP-1 object edge).
func (r *Runtime) MakeCard(tid id.TerminalID, title string, opt CardOptions) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	if err := r.canWrite(st); err != nil {
		return id.EventID{}, err
	}
	payload, err := (&schemas.Card{Title: title, Status: "open",
		Origin: opt.Origin, ObjectID: opt.ObjectID, Assignee: opt.Assignee}).Encode()
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

// SetCardStatus updates a card. card.updated.v1 is a WHOLE-RECORD LWW
// register, so this is read-modify-write: fields the caller omitted are
// preserved from the projection — a status toggle must never strip the
// task off its object or its assignee.
func (r *Runtime) SetCardStatus(tid id.TerminalID, card id.EventID, title, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return errors.New("node: unknown space")
	}
	if err := r.canWrite(st); err != nil {
		return err
	}
	next := &schemas.Card{Title: title, Status: status, Card: &card}
	for _, c := range st.space.State.Cards() {
		if c.ID != card {
			continue
		}
		if next.Title == "" {
			next.Title = c.Title
		}
		next.Assignee = c.Assignee
		next.ObjectID = c.ObjectID
		break
	}
	payload, err := next.Encode()
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
			}, st.space, r.PrincipalID),
			Dyad:          isDisplayDyad(cards, r.PrincipalID),
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
