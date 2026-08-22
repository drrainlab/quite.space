#!/usr/bin/env python3
"""Seed libFuzzer corpora from the golden vectors, so fuzzing starts from
genuine frames/payloads/records and mutates INTO the parsers' depth
instead of bouncing off the first head byte.
usage: seed_corpus.py instrument_v1.json protocol_v0.json out_dir"""
import json, os, sys
v1, v0, out = sys.argv[1:4]
a = json.load(open(v1)); b = json.load(open(v0))
seeds = {
  'fuzz_cbor': [a['instrument_envelope_frame'], a['manifest_frame'], a['epoch_payload_cbor'], a['enrollment_v1'], b['envelope_frame']],
  'fuzz_envelope': [a['instrument_envelope_frame'], b['envelope_frame']],
  'fuzz_epoch': [a['epoch_payload_cbor']],
  'fuzz_provision': [a['enrollment_v1'], a['epoch_payload_cbor']],
}
for name, hexes in seeds.items():
    d = os.path.join(out, 'corpus_' + name); os.makedirs(d, exist_ok=True)
    for i, h in enumerate(hexes):
        open(os.path.join(d, f'seed{i}'), 'wb').write(bytes.fromhex(h))
print('seeded', out)
