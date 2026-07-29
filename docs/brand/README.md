# quite.space — brand assets

Final exports, fitted against the real UI on 2026-07-30 (dark header,
first-run hero, favicon sizes, macOS menubar, light site) — all contexts
verified visually before landing.

    glyph-color.png   the Q glyph, transparent, colored glow.
                      THE application mark: header, first-run, favicon,
                      dock. Reads down to 16px.
    glyph-mono.png    monochrome glyph (black + alpha) for the macOS
                      menubar — a template image the system recolors;
                      a colored icon there would not adapt to the bar.
    lockup-light.png  the full wordmark with the nebula trail. Designed
                      for LIGHT backgrounds — this is the quite.space
                      website asset, not an app asset: on the dark app
                      theme its halo reads as a smear (measured, not
                      guessed).

Masters live with the source artwork (1024/1536px originals); these are
web-sized. When wiring into the app, COPY the needed file into
clients/web-ui/assets — nothing here is embedded, so unused art never
ships inside the binary.
