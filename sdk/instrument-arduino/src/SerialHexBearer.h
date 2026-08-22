// DEV-ONLY bearer: frames as hex lines over USB serial, for the stand
// that proves the contract (cmd/instrument-serial on the Go side). It is
// not how instruments will reach a space; it is how we check that an
// instrument's bytes are right before any real bearer exists.
//
//   out:  QI FRAME <hex>            a reading
//         QI ENROLLMENT <hex>       on request / when unprovisioned
//         QI NOTE <text>            diagnostics, human-readable
//   in:   QI PRINCIPAL <hex32>      the owner's principal (before enrollment)
//         QI PROVISION <hex>        the provision from the owner's node
//         QI EPOCH <hex>            a membership.instrument_epoch.v1 frame
//         QI ENROLL?                print the enrollment again
//         QI TIME <unix>            the host's clock (a board has none)
//         QI WIPE                   forget everything (dev convenience)
#pragma once
#include <Arduino.h>
#include "QuietBearer.h"

class SerialHexBearer : public QuietBearer {
 public:
  explicit SerialHexBearer(Stream &s = Serial) : s_(s) {}
  bool send(const uint8_t *frame, size_t n) override;
  void poll(QuietInstrument &qi) override;

 private:
  Stream &s_;
  char line_[4600];
  size_t lineLen_ = 0;
  void handle(QuietInstrument &qi, char *line);
};
