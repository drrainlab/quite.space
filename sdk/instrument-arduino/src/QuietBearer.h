// A bearer carries frames out and epoch frames in. The core never knows
// which one; the integration phase decides (LAN, relay, BLE, LoRa). The
// library ships ONE bearer, for the dev stand, and says so in its name.
#pragma once
#include <stdint.h>
#include <stddef.h>

class QuietInstrument;

class QuietBearer {
 public:
  virtual ~QuietBearer() {}
  // Deliver one frame. true = the bearer has taken it (the instrument
  // clears its owed frame). false = keep it owed, try again later.
  virtual bool send(const uint8_t *frame, size_t n) = 0;
  // Give the bearer a chance to feed inbound bytes to the instrument.
  virtual void poll(QuietInstrument &qi) = 0;
  // NOT READY IS NOT REFUSED. A road that is still joining, dialing or
  // unattended has not failed a frame — it is simply not there yet. The
  // chain skips it without a strike; only a road that was ready and
  // still refused counts against itself.
  virtual bool ready() const { return true; }
};
