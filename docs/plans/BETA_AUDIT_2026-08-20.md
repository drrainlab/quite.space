# Beta audit — 2026-08-20 (rev 2, forensic)

Every claim below carries its evidence level:

- **OBSERVED** — reported from production, screenshots or a live device.
- **PROVEN** — reproduced under instruments on a stand; the instrument
  and its output are named.
- **CODE ABSENCE** — verified by inspection that no code path exists;
  the absence itself is the finding.
- **DESIGN CANDIDATE** — a proposed shape, not yet decided or built.

Rev 1 of this document claimed one root cause and was corrected by the
owner's review: there were two independent invariants broken, and the
voice anomaly had to be explained by measurement before any conviction.
Both corrections are now closed with instruments. The doctrine this
week kept teaching is ADR-023: *inability is never success, and never
silence.*

## Headlines (owner's formulation)

1. ~~Images and file-like media fail across the production relay path
   while text and voice remain healthy. The divergence point is not yet
   isolated.~~ → **isolated, convicted, and fixed on branch
   `stream-1-media-routing`** — see Stream 1A.
2. **Paired devices of one principal do not converge on spaces joined
   after pairing.** Open; design-first; see Stream 1B.

## Stream 1A — media across relays

**OBSERVED:** with everyone online, text and voice crossed the internet
relay path; images and files hung at "fetching…" forever. A space's
diagnostics read healthy on all sides.

**PROVEN (Phase 0, `node/media_matrix_test.go`, two relays, pass-joined
friend, paired phone):** five kinds from 100KB voice to 6MB video —
every kind identical, every kind dying at the same stage:

    kind          frame  want  who-saw-the-want                 state
    voice         ✓      ✓     friend:NEVER  laptop:answer→B    fetching
    photo-small   ✓      ✓     friend:NEVER  laptop:answer→B    fetching
    photo-large   ✓      ✓     friend:NEVER  laptop:answer→B    fetching
    video         ✓      ✓     friend:NEVER  laptop:answer→B    fetching
    file          ✓      ✓     friend:NEVER  laptop:answer→B    fetching

**PROVEN — voice was never special.** The production "voice works" was
the sibling cache: `TestSiblingCacheDiagnosis` showed the kind the
laptop had fetched reaching the phone while the kind it never touched
starved. The owner's insistence on explaining the anomaly before
conviction was correct twice: the first probe table LIED (a global
probe showed "answer→B" without saying WHO answered — the laptop, into
its own emptiness), and the split-by-holder column was the load-bearing
cell.

**PROVEN — the mechanism** (branch commits 7ff8467 → 31a53b5):
1. The zero-knowledge bootstrap put copies at the SENDER's own relay,
   recorded that guess into the route book as the peer's stated route
   (poison that outlived the mistake and satisfied the known>0 gate
   forever), advanced `lastLen`, and reported reached. *Transport
   acceptance at some endpoint ≠ delivery to the intended recipient.*
2. The private wire had no way to say "I listen at X": the bundle knew
   seven keys, `RouteAdvertised` was dead code, the pairing freight
   carried no routes (CODE ABSENCE, all three verified). Honesty alone
   could therefore only make the failure visible — nothing could teach
   the routes.
3. `answerWantsRouted` returned bare when it had no route: the true
   holder of every starving asset stood at that line wanting to answer,
   saying nothing.

**FIXED, gate green** (same branch):
- *Phase 1* — `WantHold`: no-route is HELD/RETRYABLE and visible;
  refusals stay terminal (41a8a08).
- *Honesty* — the guess is used, never recorded, never final:
  `heldTentative`, displacement of recorded guesses by any statement,
  legacy-basis re-offer on stronger knowledge (25ea86e).
- *Convergence* — the freight carries the person's route book (doctrine
  amendment, the allowlist's argument), and a want carries up to three
  stated return routes (bundle key 8 → `RouteAdvertised`, cert-gated;
  both wire changes verified append-compatible before writing) (31a53b5).

After: the forever-red two-relay fetch passes in 3.8s; the five-kind
matrix is all-complete in 14s with the friend answering at the wanter's
stated relay; `TestNoSiblingCacheDependency` enforces, in both
directions, that a receiver never depends on what its sibling opened.

**Scope honesty:** the fix covers pass/quick-link relationships — the
paths the UI offers. A LEGACY direct invite records no routes on either
side (CODE ABSENCE) and remains unsupported across relays; the UI does
not offer it (the invite button runs the pass flow), pasted legacy
invites still work on a shared relay, and the follow-up is either
carrying the minter's routes in the invite string or retiring the path.

## Stream 1B — principal convergence (open, design-first)

**OBSERVED (both directions):** a space joined by quick link on one of
the person's devices never appears on the other.

**CODE ABSENCE:** the freight carries spaces only at pairing time;
nothing propagates a membership acquired afterwards to sibling devices.

**PROVEN by inspection, the deeper split:** cryptographic membership is
per-DEVICE (epochs wrap to device X25519 keys; no principal appears in
any wrap list), while admission is per-PRINCIPAL (cert chains; private
spaces check nothing at admission — holding the key IS membership). The
freight already ships other people's space keys to a new device, so a
sibling-sealed SpaceGrant is consistent with — indeed stricter than —
shipped behaviour.

**The load-bearing edge (CODE ABSENCE, latent beta blocker):** the
owner's members map never learns a sibling device — no path calls
AddMember on seeing DeviceCertified — so the owner's next rotation
(inviting anyone, revoking anything) silently deafens the paired phone:
`Undecryptable++`, no test, no mitigation. Access granted by freight or
grant is *valid until the owner's next rotation and no longer.*

**DESIGN CANDIDATE (the note must choose):** either sibling
certificates reach the owner's AddMember (a "my devices" announcement
the controller folds in — `identity_admit.go` already anticipates it),
or RotateEpoch expands principals to their certified devices at wrap
time. The invariant, exit criteria, and the ten-step red test (offline
window, speaks-as-principal, rotation, revocation) are in the approved
plan. Implementation is v0.1.6.

## Streams 2–3 (unchanged from rev 1, one addition)

Reply+pasted-image loses the reply (blocks cannot carry `reply_to` —
message.text.v1 only; the composer neither sends nor clears the
target); grouped media; Esc closes a post; the zoom cursor. **Decide
reply+image with the envelope direction in mind:** the grouped-media
request suggests `reply_to` eventually belongs to a composition/message
envelope, not to `message.text.v1` — do not cement a caption-hack that
fights that future. **New (PROVEN by inspection):** the web UI sends
voice as `kind:'audio'` with a transcript that AudioBlock cannot carry —
the typed transcript is silently discarded and `block.voice.v1` is
never produced from the web UI.

## Exit criteria for Stream 1 (owner's gate)

    1A MEDIA: photo/video/file/voice ✓ · different relays ✓ · paired
    devices ✓ · no sibling-cache dependency ✓        ← green on branch
    1B IDENTITY: join anywhere → siblings converge · works after an
    offline window · current + future epochs · sibling speaks as the
    same principal · revocation stops convergence     ← v0.1.6

Release cut: v0.1.5 = media trust restored (this branch, after review);
v0.1.6 = principal convergence. Stream 1 is DONE only after both. The
two sentences the whole stream answers to: *"I am one person on my Mac
and my phone — wherever I make a connection, it is mine everywhere"*
and *"I sent media — it can be fetched directly, regardless of file
type, relay choice, or what my other device happened to do."*

## Method note

Ten of this week's convictions came from instruments; zero from reading
code. Two probe tables lied until the instrument itself was
instrumented (who-saw-the-want; the global wantsProbe). Three bugs were
caught by the stand before any user saw them — a self-deadlock, a
duplicated freight encoder silently dropping the newest field, a test
joining by a flow production never used. The doctrine and the method
are now written where the next person will read them: ADR-023, and the
comments of the tests that measured everything above.
