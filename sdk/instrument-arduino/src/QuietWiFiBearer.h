// QuietWiFiBearer — the courier that is not a member (QI-B1, ADR-035).
//
// The board joins the local network, hears the node announce itself,
// recognises its own space by the hint it computes from its clock, dials
// the node over TLS, knocks with a signature bound to the node's
// certificate, and from then on carries frames UP and receives epoch
// rotations and the clock floor DOWN on the same connection. Its laws,
// numbered for the gate:
//
//   1. NO TIME, NO HINT, NO DIAL. The hint is sha256 over the space and a
//      6-hour bucket of unix time; a board that does not know the time
//      cannot compute what the node announces and stays deaf — the
//      fail-closed choice (ADR-025) extended to discovery. The chain's
//      serial road is the rescue, never a hidden clock.
//   2. THE KNOCK BINDS TO THE NODE'S CERTIFICATE. sign("qp-instr-door-v0:"
//      ‖ sha256(node leaf DER) ‖ space) with the device key — the bytes
//      arduino-esp32's getFingerprintSHA256 already computes. A person in
//      the middle presents a different cert and opens nothing.
//   3. READY MEANS THE DOOR ANSWERED. send() carries frames only after
//      msgEpochs arrived on this connection; before that it says false
//      and the chain's next road gets the frame. The owed law stays with
//      the core.
//   4. TIME ONLY FORWARD. The unix clock riding every epochs push is a
//      floor: applied when it advances, ignored when it would set the
//      board back.
//   5. A LOST NODE IS SOUGHT AGAIN, QUIETLY. A dial that fails backs off
//      (5 s doubling to 60 s); three failures forget the address and go
//      back to listening for announcements. Nothing is retried in a
//      frame's send path — send() never blocks on the network.
//
// Provisioning is NOT this bearer's business: a board enrolls through
// the stand (serial) once and holds its space and certificate from then
// on. The wire below is the LAN link's own — 4-byte length-prefixed
// packets, each a single-fragment carrier envelope — so the node cannot
// tell a board from a peer until the first message says so.
#pragma once
#if defined(ESP32)
#include <Arduino.h>
#include <WiFi.h>
#include <WiFiUdp.h>
#include <WiFiClientSecure.h>
#include "QuietBearer.h"

class QuietWiFiBearer : public QuietBearer {
 public:
  QuietWiFiBearer(const char *ssid, const char *pass) : ssid_(ssid), pass_(pass) {}

  // Join the network and open the announcement listener. Non-blocking:
  // the join completes in the background and poll() does the rest.
  void begin();

  bool send(const uint8_t *frame, size_t n) override;
  void poll(QuietInstrument &qi) override;

  // For the face: one honest word about where the courier stands.
  //   "joining" · "no-time" · "listening" · "dialing" · "knocked" · "in"
  const char *state() const { return stateWord_; }
  // The courier is a road only once the door answered (law 3); before
  // that the chain skips it without a strike (QuietBearer::ready).
  bool ready() const override { return ready_; }
  int rssi() const { return WiFi.status() == WL_CONNECTED ? WiFi.RSSI() : 0; }

 private:
  const char *ssid_;
  const char *pass_;
  WiFiUDP udp_;
  WiFiClientSecure client_;

  // what we know
  bool haveSpace_ = false;
  uint8_t space_[32];
  bool haveNode_ = false;
  IPAddress nodeIP_;
  uint16_t nodePort_ = 0;

  // where we stand
  bool connected_ = false;
  bool ready_ = false;
  const char *stateWord_ = "joining";
  uint32_t nextDialMs_ = 0;
  uint32_t dialBackoffMs_ = 5000;
  uint8_t dialFails_ = 0;
  uint64_t timeFloor_ = 0;

  // inbound reassembly of ONE length-prefixed packet at a time
  uint8_t rx_[4096];
  size_t rxHave_ = 0;
  size_t rxNeed_ = 0;  // 0 = waiting for the 4-byte header
  bool rxSkipping_ = false;
  size_t rxSkip_ = 0;

  void listenAnnounce(QuietInstrument &qi);
  bool dial(QuietInstrument &qi);
  bool knock(QuietInstrument &qi);
  void readInbound(QuietInstrument &qi);
  void handleMessage(QuietInstrument &qi, const uint8_t *msg, size_t n);
  bool writePacket(const uint8_t *pkt, size_t n);
  void drop(const char *why);
  void note(const char *what);
};
#endif
