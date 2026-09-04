#include "SerialHexBearer.h"
#include "QuietInstrument.h"

bool SerialHexBearer::send(const uint8_t *frame, size_t n) {
  if (!attended()) return false;  // nobody is listening: the frame stays owed
  s_.print("QI FRAME ");
  for (size_t i = 0; i < n; i++) { if (frame[i] < 16) s_.print('0'); s_.print(frame[i], HEX); }
  s_.println();
  return true;
}

void SerialHexBearer::poll(QuietInstrument &qi) {
  while (s_.available()) {
    char c = (char)s_.read();
    if (c == '\r') continue;
    if (c == '\n') { line_[lineLen_] = 0; handle(qi, line_); lineLen_ = 0; continue; }
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
  if (!strcmp(cmd, "PRINCIPAL") && arg) qi.setPrincipalHex(arg);
  else if (!strcmp(cmd, "PROVISION") && arg) qi.provisionHex(arg);
  else if (!strcmp(cmd, "EPOCH") && arg) qi.ingestHex(arg);
  else if (!strcmp(cmd, "ENROLL?")) qi.printEnrollment();
  else if (!strcmp(cmd, "TIME") && arg) qi.setUnixTime(strtoull(arg, NULL, 10));
  else if (!strcmp(cmd, "WIPE")) qi.wipe();
}
