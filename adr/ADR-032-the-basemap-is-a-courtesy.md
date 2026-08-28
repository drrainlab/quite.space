# ADR-032: The basemap is a courtesy; claims stay sovereign

Status: accepted (SP-3.1, 2026-08-28)

## Context

ADR-031 made the Field a map of signed claims, drawn on its own paper: a
metre grid and a scale bar. Real cartography was named there as the next
wave, and this is it — OpenStreetMap raster tiles under the claims,
restyled to the app's dark palette.

The reason this needs an ADR rather than a commit message is that a tile
request is the most location-revealing thing this product can do. Every
other outbound byte says *that* this device is awake; a tile request says
**which three hundred metres of the world are on its screen right now**,
to a third party, unprompted, sixteen times per pan. A basemap is
therefore not a rendering detail. It is a disclosure with a picture
attached, and the laws below are what make it one somebody chose.

## Laws

### 1. The map of claims is complete without tiles

Nothing operational may depend on the basemap. Positions, markers,
routes, and check-ins mean exactly what ADR-031 says they mean with the
basemap off, unreachable, or refused by policy. The basemap is context
for a human eye, never a source of truth, and never a precondition for
reading the field.

### 2. Tiles never ride the protocol

Not in events, not in bundles, not over radio, not in backups. They are
**refetchable world, not history**: a backup is a person's own record,
and shipping hundreds of megabytes of OpenStreetMap inside it would both
dwarf what it exists to save and hand whoever holds it a map of where its
owner has been looking. `skipFromBackup` enforces this, with a test.

Radio is not a maintenance detail here but the same law: the airwaves
carry claims. A basemap that could ask for bytes over LoRa would spend a
search team's link on scenery.

### 3. Viewport disclosure is consented, and the dial outranks the switch

The basemap toggle is per-space, device-local, and **default off**. Its
label is the disclosure, not a name: "Map background — fetches the viewed
area from the tile server". The first time it is enabled on a device, a
dialog says which server, what it learns, and that tiles are cached
encrypted for offline use.

The connectivity policy is **above** the toggle. `internetGate()` runs
before any socket opens (the connectivity doctrine: the dial itself is
the disclosure); in offline and radio-only modes the map lives on cache
and paper no matter what the toggle says. There is no "just one tile"
exemption.

*Named inconsistency:* link unfurling predates this gate and does not yet
consult it, so a radio-only node will still dial out to preview a pasted
link. That is a real hole, and `internetGate()` is where its fix belongs.

### 4. The cache is a location diary, so it is encrypted at rest

Tiles are stored through the sealed store (XChaCha20, ADR-014), keyed by
a hash of the server template so switching servers can never serve
another provider's stale pixels. Eviction is a cap with an LRU sweep:
offline-first is a promise about *what you have seen*, not a licence to
fill the disk forever.

### 5. Paper is the honest fallback

A tile that is missing, refused, or unreachable renders as the metre grid
with a plain sentence — "no map tiles for this area — grid only". Never a
spinner (which promises work nobody started), never the previous area's
pixels held over (which would be a map lying about where you are).

### 6. Style is a view decision; the cache holds originals

The dark restyle is applied at draw time, not baked into stored bytes.
Flipping the app's theme restyles instantly and refetches nothing — and
an honest cache stays comparable with what the tile server actually sent.

### 7. Attribution is part of rendering

Whenever OpenStreetMap pixels are on screen, "© OpenStreetMap
contributors" is drawn on the canvas. ODbL requires the credit; putting
it in the drawing code rather than in a settings screen means it cannot
drift away from the pixels it credits.

### 8. We keep the upstream's rules because they are somebody's donated capacity

The OSM tile usage policy is honored structurally, not by intention: a
distinctive User-Agent naming the app (never a browser impersonation), at
most two parallel upstream fetches (a semaphore, not a guideline), and
**no bulk area prefetch**. Cache-as-you-browse is the whole offline story
in v1 — pan the valley you are heading into and it stays.

*Named future work:* a deliberate "take this area with you" download is
worth having, and it may only exist against a tile server the person
configured themselves.

## Consequences

- The page's CSP (`img-src 'self'`) already forbade talking to a tile
  server directly. That constraint is kept deliberately: it forces every
  tile through the node, which is the only place consent, the gate, and
  the encrypted cache can be enforced at once.
- The Field's viewport became absolute (centre + Mercator zoom) rather
  than a multiplier over fit-to-content. A raster basemap made the old
  form's flaw visible — the ten-second poll re-fit the view and moved the
  map under the person's hand — so fitting is now an act ("fit"), not a
  side effect of new data arriving.
- The node grew its first persistent internet-sourced cache. That is a
  new category of state in this codebase, and laws 2 and 4 exist to keep
  it from quietly becoming something else.
