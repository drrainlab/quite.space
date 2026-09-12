# LT-2 — why a message can take minutes, and where the minutes hide

Status: findings, 2026-09-12. LT-1 measured the fast path on a stand:
relay acceptance in 10–21 ms, ✓✓ in 128–144 ms. The owner's report is
about the field, where "the whole cycle takes an unusually long time".
This document lists every place a minute can hide, from the code, so the
next wave fixes causes rather than symptoms. Numbers are the shipped
constants; nothing here is measured on a phone yet — that is the first
item of the wave.

## The shape of one delivery

```
sender node ──put──▶ recipient's mailbox at recipient's RELAY
                                     │
                       doorbell (parked listener) ── or ── the poll
                                     │
                          recipient node collects ──▶ folds ──▶ receipt
```

Every minute below is a place where the middle line fails to ring, or
rings into a socket nobody is holding.

## Where the minutes hide

1. **A parked listener whose socket died in silence.** The listener is
   slept on: it wakes for packets and for the socket's death. A NAT or a
   carrier that drops the mapping produces neither — the socket stays
   "open" on the phone and the relay's notify goes nowhere. The only
   probe is the keepalive, `ListenPing = 12 min`, and its `Send` succeeds
   into the kernel buffer regardless; the failure surfaces when the
   kernel gives up retransmitting, minutes later. Meanwhile the poll that
   is meant to be the net runs at `listenedMultiplier = 300` × 2 s =
   **10 minutes** because the listener "is healthy". Worst case: a
   message sits in the mailbox for ~10 min on a phone that believes it is
   listening. This is the prime suspect.

2. **Relay restarts.** Every parked session is cut; the manager re-parks
   on a ladder `listenRetryMin = 1 min` doubling to `16 min` per failure.
   A deploy of three relays in a row (today: four times) makes each
   phone's listeners come back over one to sixteen minutes, and the 10
   min poll is the net meanwhile. A restart is not a flapping route and
   should not pay a flapping route's backoff.

3. **The bootstrap guess.** A device with no stated route gets its copy
   at the SENDER'S relay (tentative). If the recipient never polls that
   relay, the copy waits until the recipient's ingress statement reaches
   the sender (identity plane, `planesEvery = 5` ticks in the foreground,
   every cycle in the background) and the space re-offers. Honest, and
   slow for the first exchange after a route change — automatic relay
   selection moves a phone and its peers learn only when it next speaks.

4. **The background cadence itself.** Nobody looking + no listener =
   `backgroundMultiplier = 30` × 2 s = 60 s poll. Correct by the energy
   survey, and one minute is the price named there. Fine — as long as the
   listener is real (see 1).

5. **The sender's turn.** Outgoing is kicked at once (`kickRelaySync` on
   Say), and per-endpoint pushes run in parallel since LT-1. Not a
   suspect — unless the sender is a headless node that was pretending to
   be watched (fixed today: attention from the API, node/foreground.go).

6. **Receipts (✓✓) ride the recipient's own heartbeat.** The receipt is
   sent on the recipient's next cycle after folding — in the background
   that is up to a minute after the message was read by the machine.
   Cosmetic: the message itself was already there.

## What the wave should do, in order

1. **Measure on the phone first.** The latency ledger (`/api/relay/
   diagnostics` → `latency`) exists on every node; add a field per parked
   listener — `parked_since`, `last_pong`, `wakes` — so a listener that
   died in silence is visible as one whose last pong is old.

2. **A ping that expects an answer.** After `MsgPing`, wait a bounded
   time for `MsgPong` (10 s); no answer → the session is dead, end it,
   re-park now. And shorten the ping to what carrier NATs tolerate
   (5 min is the common floor) while the app is backgrounded on cellular;
   12 min is right on Wi-Fi.

3. **Re-park at once on connectivity change and on foreground.** Android
   already tells the core about foreground; the connectivity callback
   should call `BounceListeners` so a new network gets a new park instead
   of a ghost.

4. **A restart is not a flap.** First retry after a clean relay close in
   5–15 s with jitter; the ladder only for repeated failures.

5. **Shorten the net under the doorbell.** `listenedMultiplier` 300 → 90
   (3 min): the doorbell still carries the news, and the worst case for a
   silent death drops from ten minutes to three at a cost the energy
   survey's arithmetic tolerates (one burst in three minutes is still a
   tenth of the always-on bill).

6. **Guess at every official relay, not one.** A recipient with no stated
   route is more likely to be found at *some* official relay than at the
   sender's; the delta book (node/offers.go) makes the extra copies cheap
   after the first offer. To be weighed against relay storage.

Each item is one evening; 1 and 2 together are the bet.
