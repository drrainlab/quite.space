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

//
// DELIVERY IS A CLAIM, AND A CLAIM NEEDS A LISTENER. Printing a frame to
// a UART proves nothing about who reads it: with no stand on the wire —
// detached in the app, a monitor open instead, nobody at all — the bytes
// vanish, yet send() used to say true, the core cleared its one owed
// frame, the chain moved on, and every successor that later reached a
// node by another road arrived an orphan, held forever behind a
// predecessor nobody had (86 of them on the owner's desk). Now the
// bearer claims delivery only while a stand has spoken within
// kAttendedMs; stands pulse "QI TIME" every 15 s exactly so this can be
// known. Unattended, the frame stays owed and the chain waits — the
// chain's honesty over the reading's freshness, every time.
//
// DELIVERED MEANS ACKNOWLEDGED. Attendance says a stand is probably
// there; it does not say this frame arrived, and a stand that vanished
// in the last few seconds — or a node that refused the frame — made a
// hole in the chain that nothing could fill (378 held moments on the
// owner's panel). So a frame is delivered only when the stand answers
// "QI ACK <event id>" for it: the bearer prints the frame, waits a short
// while for exactly that line (still feeding every other line it hears
// to the instrument), and says false otherwise. An unanswered frame is
// offered again after kResendMs, never sooner.
class SerialHexBearer : public QuietBearer {
 public:
  static const uint32_t kAttendedMs = 45000;
  static const uint32_t kAckWaitMs = 400;
  static const uint32_t kResendMs = 5000;
  explicit SerialHexBearer(Stream &s = Serial) : s_(s) {}
  bool send(const uint8_t *frame, size_t n) override;
  void poll(QuietInstrument &qi) override;
  bool ready() const override { return attended(); }
  bool attended() const { return lastHeardMs_ && (uint32_t)(millis() - lastHeardMs_) < kAttendedMs; }

 private:
  Stream &s_;
  char line_[4600];
  size_t lineLen_ = 0;
  uint32_t lastHeardMs_ = 0;
  QuietInstrument *qi_ = nullptr;   // the instrument last polled: lines heard while waiting go to it
  uint8_t awaiting_[32];            // event id of the frame on the wire
  bool haveAwaiting_ = false;
  bool acked_ = false;
  uint32_t sentMs_ = 0;
  void handle(QuietInstrument &qi, char *line);
  void pump();
};
