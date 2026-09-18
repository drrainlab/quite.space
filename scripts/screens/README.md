# Screenshots that can be retaken

The pictures in the top-level README are not mock-ups: they are taken from
a real two-node stand by a headless browser, so after an interface change
they are retaken, not redrawn.

```sh
go build -o ~/.quiet-stand/qp-terminal ./cmd/terminal
mkdir -p ~/.quiet-stand/{fa,fb,shots}
QP_PASSPHRASE=fa-pass-long-enough ~/.quiet-stand/qp-terminal ui --data ~/.quiet-stand/fa --port 8501 --no-browser --token fatok &
QP_PASSPHRASE=fb-pass-long-enough ~/.quiet-stand/qp-terminal ui --data ~/.quiet-stand/fb --port 8502 --no-browser --token fbtok &
python3 scripts/screens/mkstand.py        # once: the space, the people, the conversation
node scripts/screens/shoot.mjs            # Node 22+, Google Chrome; writes ~/.quiet-stand/shots/*.png
for f in ~/.quiet-stand/shots/*.png; do cwebp -q 86 "$f" -o "docs/screens/$(basename "${f%.png}").webp"; done
```

`shoot.mjs` drives Chrome over the DevTools protocol with no dependencies
(Node 22 ships `WebSocket` and `fetch`). The posts, the article and the map
come from the public demo spaces, which the stand's first node has to have
opened once from **discover** (Field — demo). The stand lives in the home
directory on purpose: a fixture kept in /tmp lost its keys to the OS's
three-day sweep.
