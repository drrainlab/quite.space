// BearerChain — several roads, one honest answer (QI-B1 Ф4).
//
// The chain tries its links in the order they were added (add the best
// road first: wifi, then serial). Its laws, numbered for the gate:
//
//   1. THREE STRIKES IS DOWN. A link that refuses kFailThreshold sends
//      in a row is marked down and skipped — one flaky send must not
//      exile a road, and a dead one must not eat every frame's timeout.
//   2. A DOWN ROAD IS PROBED, NOT FORGOTTEN. After kProbeMs the next
//      real frame is offered to the downed link once; success revives
//      it fully, failure re-arms the wait. No synthetic pings — the
//      probe is work that had to happen anyway.
//   3. FALSE ONLY WHEN EVERY ROAD REFUSED. The owed law belongs to the
//      core: it clears a frame only when send() returns true, and the
//      chain returns true the moment ANY link takes the frame.
//   4. EVERY ROAD LISTENS. poll() feeds all links, downed ones included
//      — epochs and time may arrive on a road we would not choose for
//      sending, and hearing is free.
//   5. NOT READY IS NOT A STRIKE. A road still joining, dialing or
//      unattended is skipped, not punished: the first live board struck
//      Wi-Fi out for a minute at every boot for the crime of not having
//      dialed yet, and readings waited on a probe that need not exist.
//
// Pure machine: the clock is injected, so the native suite drives years
// in microseconds and the sketch passes millis.
#pragma once
#include "QuietBearer.h"

class BearerChain : public QuietBearer {
 public:
  static const int kMaxLinks = 4;
  static const uint8_t kFailThreshold = 3;
  static const uint32_t kProbeMs = 60000;

  typedef uint32_t (*ClockMs)();
  explicit BearerChain(ClockMs clock) : clock_(clock) {}

  bool add(QuietBearer *b, const char *label) {
    if (n_ >= kMaxLinks || !b) return false;
    links_[n_].b = b;
    links_[n_].label = label;
    links_[n_].fails = 0;
    links_[n_].down = false;
    links_[n_].downSince = 0;
    n_++;
    return true;
  }

  bool send(const uint8_t *frame, size_t n) override {
    uint32_t now = clock_();
    for (int i = 0; i < n_; i++) {
      Link &l = links_[i];
      if (!l.b->ready()) continue;  // not there yet: no strike (see QuietBearer)
      if (l.down && (uint32_t)(now - l.downSince) < kProbeMs) continue;
      if (l.b->send(frame, n)) {
        l.fails = 0;
        l.down = false;
        last_ = i;
        return true;
      }
      if (l.down) {
        l.downSince = now;  // law 2: a failed probe re-arms the wait
      } else if (++l.fails >= kFailThreshold) {
        l.down = true;
        l.downSince = now;
      }
    }
    return false;  // law 3: every road refused — the frame stays owed
  }

  void poll(QuietInstrument &qi) override {
    for (int i = 0; i < n_; i++) links_[i].b->poll(qi);  // law 4
  }

  // For the face: who carried the last delivered frame ("via wifi").
  const char *lastLabel() const { return last_ >= 0 ? links_[last_].label : ""; }
  bool linkDown(int i) const { return i >= 0 && i < n_ && links_[i].down; }

 private:
  struct Link {
    QuietBearer *b;
    const char *label;
    uint8_t fails;
    bool down;
    uint32_t downSince;
  };
  ClockMs clock_;
  Link links_[kMaxLinks];
  int n_ = 0;
  int last_ = -1;
};
