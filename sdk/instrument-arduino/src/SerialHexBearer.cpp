#include "SerialHexBearer.h"
#include "QuietInstrument.h"
extern "C" {
#include "qi/ids.h"
}
#include <string.h>
#include <stdlib.h>

bool SerialHexBearer::send(const uint8_t *frame, size_t n) {
  if (!attended() || !qi_) return false;  // nobody is listening: the frame stays owed
  uint8_t eid[32];
  qi_event_id(frame, n, eid);
  // The same frame, offered again too soon: the stand had its chance and
  // has not answered — wait, do not flood.
  if (haveAwaiting_ && memcmp(eid, awaiting_, 32) == 0 && !acked_ &&
      (uint32_t)(millis() - sentMs_) < kResendMs) return false;
  memcpy(awaiting_, eid, 32);
  haveAwaiting_ = true;
  acked_ = false;
  sentMs_ = millis();
  s_.print("QI FRAME ");
  for (size_t i = 0; i < n; i++) { if (frame[i] < 16) s_.print('0'); s_.print(frame[i], HEX); }
  s_.println();
  // Wait for THIS frame's acknowledgement, feeding every other line to
  // the instrument meanwhile.
  uint32_t t0 = millis();
  while ((uint32_t)(millis() - t0) < kAckWaitMs) {
    pump();
    if (acked_) return true;
    delay(2);
  }
  return false;
}

void SerialHexBearer::poll(QuietInstrument &qi) {
  qi_ = &qi;
  pump();
}

void SerialHexBearer::pump() {
  if (!qi_) return;
  while (s_.available()) {
    char c = (char)s_.read();
    if (c == '\r') continue;
    if (c == '\n') { line_[lineLen_] = 0; handle(*qi_, line_); lineLen_ = 0; continue; }
    if (lineLen_ + 1 < sizeof line_) line_[lineLen_++] = c;
    else lineLen_ = 0;  // overlong line: drop it, never overflow
  }
}

void SerialHexBearer::handle(QuietInstrument &qi, char *line) {
  if (strncmp(line, "QI ", 3) != 0) return;
  lastHeardMs_ = millis();  // a stand spoke: the wire is attended
  char *cmd = line + 3;
  char *arg = strchr(cmd, ' ');
  if (arg) *arg++ = 0;
  if (!strcmp(cmd, "ACK") && arg) {
    // The stand names the event id it applied; only OUR frame's id counts.
    uint8_t got[32]; size_t k = 0;
    for (; k < 32 && arg[2 * k] && arg[2 * k + 1]; k++) {
      char h[3] = {arg[2 * k], arg[2 * k + 1], 0};
      got[k] = (uint8_t)strtoul(h, NULL, 16);
    }
    if (k == 32 && haveAwaiting_ && memcmp(got, awaiting_, 32) == 0) acked_ = true;
  }
  else if (!strcmp(cmd, "PRINCIPAL") && arg) qi.setPrincipalHex(arg);
  else if (!strcmp(cmd, "PROVISION") && arg) qi.provisionHex(arg);
  else if (!strcmp(cmd, "EPOCH") && arg) qi.ingestHex(arg);
  else if (!strcmp(cmd, "ENROLL?")) qi.printEnrollment();
  else if (!strcmp(cmd, "TIME") && arg) qi.setUnixTime(strtoull(arg, NULL, 10));
  else if (!strcmp(cmd, "WIPE")) qi.wipe();
}
