#if defined(ESP32)
#include "QuietWiFiBearer.h"
#include "QuietInstrument.h"
extern "C" {
#include "qi/cbor.h"
#include "qi/crypto.h"
}

// The LAN link's numbers, verbatim from the Go side (transports/lan,
// kernel/sync). Append-only tables; the door's own are 8/9 and 11/12.
static const char *kMulticastIP = "239.255.71.80";
static const uint16_t kMulticastPort = 47180;
static const uint8_t kAnnKeyPort = 1, kAnnKeyHints = 2;
static const uint8_t kFragStream = 1, kFragIndex = 2, kFragTotal = 3, kFragChunk = 4;
static const uint8_t kKeyType = 1, kKeyTerminal = 2, kKeyFrames = 4;
static const uint8_t kMsgFrames = 2, kMsgEpochReq = 8, kMsgEpochs = 9;
static const uint8_t kKeyEpochReq = 11, kKeyEpochs = 12;
static const uint8_t kReqSpace = 1, kReqDevice = 2, kReqSig = 3;
static const uint8_t kEpSpace = 1, kEpFrames = 2, kEpUnix = 3;
static const char *kDoorLabel = "qp-instr-door-v0:";

// Static scratch, never stack: the MCU-stack law the first live board
// taught the core (a loop task owns ~8KB). Single-threaded by contract.
static uint8_t msgBuf_[QI_MAX_FRAME + 128];
static uint8_t pktBuf_[QI_MAX_FRAME + 192];

void QuietWiFiBearer::note(const char *what) {
  Serial.print("QI NOTE wifi: ");
  Serial.println(what);
}

void QuietWiFiBearer::begin() {
  WiFi.mode(WIFI_STA);
  WiFi.setAutoReconnect(true);
  // No modem sleep: a dozing STA misses multicast the AP queues for the
  // DTIM beacon, and announcements ARE multicast — a courier that sleeps
  // through the address it waits for is a courier that never dials. The
  // mains-powered terminal pays ~40mA for it; the portable line (QI-P)
  // decides its own trade with its own numbers.
  WiFi.setSleep(false);
  WiFi.begin(ssid_, pass_);
  client_.setInsecure();  // the node's cert claims nothing; identity is in signatures
  stateWord_ = "joining";
}

// ---- outbound ------------------------------------------------------------

// One packet on the LAN link: 4-byte big-endian length, then a single-
// fragment carrier envelope {stream 0, index 0, total 1, chunk msg}.
bool QuietWiFiBearer::writePacket(const uint8_t *msg, size_t n) {
  qi_cbor_w w;
  qi_w_init(&w, pktBuf_ + 4, sizeof pktBuf_ - 4);
  qi_w_map(&w, 4);
  qi_w_uint(&w, kFragStream); qi_w_uint(&w, 0);
  qi_w_uint(&w, kFragIndex);  qi_w_uint(&w, 0);
  qi_w_uint(&w, kFragTotal);  qi_w_uint(&w, 1);
  qi_w_uint(&w, kFragChunk);  qi_w_bytes(&w, msg, n);
  size_t fn;
  if (qi_w_done(&w, &fn) != QI_OK) return false;
  pktBuf_[0] = (uint8_t)(fn >> 24); pktBuf_[1] = (uint8_t)(fn >> 16);
  pktBuf_[2] = (uint8_t)(fn >> 8);  pktBuf_[3] = (uint8_t)fn;
  size_t total = 4 + fn;
  size_t wrote = client_.write(pktBuf_, total);
  if (wrote != total) { drop("write failed"); return false; }
  return true;
}

bool QuietWiFiBearer::send(const uint8_t *frame, size_t n) {
  if (!ready_ || !connected_ || !haveSpace_) return false;  // law 3
  qi_cbor_w w;
  qi_w_init(&w, msgBuf_, sizeof msgBuf_);
  qi_w_map(&w, 3);
  qi_w_uint(&w, kKeyType);     qi_w_uint(&w, kMsgFrames);
  qi_w_uint(&w, kKeyTerminal); qi_w_bytes(&w, space_, 32);
  qi_w_uint(&w, kKeyFrames);   qi_w_array(&w, 1); qi_w_bytes(&w, frame, n);
  size_t mn;
  if (qi_w_done(&w, &mn) != QI_OK) return false;
  return writePacket(msgBuf_, mn);
}

// ---- lifecycle -----------------------------------------------------------

void QuietWiFiBearer::drop(const char *why) {
  if (connected_) note(why);
  client_.stop();
  connected_ = false;
  ready_ = false;
  rxHave_ = rxNeed_ = 0;
  rxSkipping_ = false;
  stateWord_ = haveNode_ ? "dialing" : "listening";
}

void QuietWiFiBearer::poll(QuietInstrument &qi) {
  static const char *lastWord = NULL;
  // THE CLOCK COMES WITH THE NETWORK. A board on a wall wart reboots and
  // has no stand to hand it the time — and without time there is no
  // hint and no dial: deaf until somebody plugs a cable in. SNTP once
  // the link is up makes the network its own clock source; the serial
  // stand stays the rescue for a LAN with no way out. What leaves the
  // board is a UDP time query and nothing else.
  static bool sntpStarted = false;
  if (!sntpStarted && WiFi.status() == WL_CONNECTED) {
    sntpStarted = true;
    configTime(0, 0, "pool.ntp.org", "time.google.com");
  }
  if (stateWord_ != lastWord) {
    lastWord = stateWord_;
    Serial.print("QI NOTE wifi: ");
    Serial.print(stateWord_);
    if (WiFi.status() == WL_CONNECTED) { Serial.print(" ip "); Serial.print(WiFi.localIP()); }
    Serial.println();
  }
  if (WiFi.status() != WL_CONNECTED) {
    if (connected_) drop("wifi lost");
    stateWord_ = "joining";
    return;
  }
  if (!haveSpace_) {
    if (!qi.space(space_)) return;  // not provisioned yet: nothing to seek
    haveSpace_ = true;
    udp_.beginMulticast(IPAddress(239, 255, 71, 80), kMulticastPort);
    stateWord_ = "listening";
#if defined(QI_NODE_HOST) && defined(QI_NODE_PORT)
    // A STATIC ADDRESS, when the network will not carry multicast (many
    // routers filter it between wireless clients). Discovery is still
    // the way back: three failed dials forget this address too and the
    // courier listens for announcements like any other.
    if (nodeIP_.fromString(QI_NODE_HOST)) {
      nodePort_ = QI_NODE_PORT;
      haveNode_ = true;
      nextDialMs_ = millis();
      stateWord_ = "dialing";
      note("node address configured statically");
    }
#endif
  }
  if (connected_) {
    if (!client_.connected()) { drop("node hung up"); return; }
    readInbound(qi);
    return;
  }
  if (!haveNode_) {
    listenAnnounce(qi);
    return;
  }
  if ((int32_t)(millis() - nextDialMs_) < 0) return;
  if (!dial(qi)) {
    if (++dialFails_ >= 3) {
      // law 5: the address is stale or the node moved — listen again
      haveNode_ = false;
      dialFails_ = 0;
      dialBackoffMs_ = 5000;
      stateWord_ = "listening";
      note("node not answering — listening for announcements again");
      return;
    }
    nextDialMs_ = millis() + dialBackoffMs_;
    if (dialBackoffMs_ < 60000) dialBackoffMs_ *= 2;
  }
}

// ---- discovery -----------------------------------------------------------

// An announcement is {1: port, 2: [hint16...], 3: nonce}. The board owns
// the space and the clock, so it computes the hints the node would carry
// — this bucket and the previous one — and matches; the source address
// of the datagram is the node's.
void QuietWiFiBearer::listenAnnounce(QuietInstrument &qi) {
  uint64_t now = qi.unixNow();
  if (now == 0) { stateWord_ = "no-time"; while (udp_.parsePacket()) udp_.flush(); return; }  // law 1
  stateWord_ = "listening";
  uint8_t want[2][16];
  uint64_t bucket = qi_lan_bucket(now);
  qi_lan_hint(space_, bucket, want[0]);
  qi_lan_hint(space_, bucket ? bucket - 1 : 0, want[1]);

  int len;
  while ((len = udp_.parsePacket()) > 0) {
    static uint8_t ann[512];
    if (len > (int)sizeof ann) { udp_.flush(); continue; }
    int got = udp_.read(ann, len);
    if (got <= 0) continue;
    qi_cbor_r r;
    qi_r_init(&r, ann, (size_t)got);
    size_t fields;
    if (qi_r_map(&r, &fields) != QI_OK) continue;
    uint64_t port = 0;
    bool match = false;
    bool bad = false;
    for (size_t i = 0; i < fields && !bad; i++) {
      uint64_t key;
      if (qi_r_uint(&r, &key) != QI_OK) { bad = true; break; }
      if (key == kAnnKeyPort) {
        if (qi_r_uint(&r, &port) != QI_OK) bad = true;
      } else if (key == kAnnKeyHints) {
        size_t nh;
        if (qi_r_array(&r, &nh) != QI_OK) { bad = true; break; }
        for (size_t h = 0; h < nh; h++) {
          const uint8_t *hb; size_t hn;
          if (qi_r_bytes(&r, &hb, &hn) != QI_OK) { bad = true; break; }
          if (hn == 16 && (!memcmp(hb, want[0], 16) || !memcmp(hb, want[1], 16))) match = true;
        }
      } else if (qi_r_skip(&r) != QI_OK) {
        bad = true;
      }
    }
    if (bad || !match || port == 0 || port > 65535) continue;
    nodeIP_ = udp_.remoteIP();
    nodePort_ = (uint16_t)port;
    haveNode_ = true;
    dialFails_ = 0;
    nextDialMs_ = millis();
    stateWord_ = "dialing";
    note("node found by hint");
    return;
  }
}

// ---- the door ------------------------------------------------------------

bool QuietWiFiBearer::dial(QuietInstrument &qi) {
  stateWord_ = "dialing";
  client_.setInsecure();
  if (!client_.connect(nodeIP_, nodePort_)) return false;
  connected_ = true;
  rxHave_ = rxNeed_ = 0;
  if (!knock(qi)) { drop("knock failed"); return false; }
  dialBackoffMs_ = 5000;
  stateWord_ = "knocked";
  return true;
}

// law 2: sign the node's certificate fingerprint and the space, send
// {type 8, 11: {space, device, sig}} as the FIRST message on the wire.
bool QuietWiFiBearer::knock(QuietInstrument &qi) {
  uint8_t fp[32];
  if (!client_.getFingerprintSHA256(fp)) return false;
  static uint8_t toSign[17 + 32 + 32];
  memcpy(toSign, kDoorLabel, 17);
  memcpy(toSign + 17, fp, 32);
  memcpy(toSign + 49, space_, 32);
  uint8_t sig[64];
  qi_ed25519_sign(qi.core().device_sk, toSign, sizeof toSign, sig);
  { char who[48]; snprintf(who, sizeof who, "knock dev=%02x%02x%02x%02x space=%02x%02x%02x%02x",
      qi.core().device_pub[0], qi.core().device_pub[1], qi.core().device_pub[2], qi.core().device_pub[3],
      space_[0], space_[1], space_[2], space_[3]);
    note(who); }

  static uint8_t payload[160];
  qi_cbor_w pw;
  qi_w_init(&pw, payload, sizeof payload);
  qi_w_map(&pw, 3);
  qi_w_uint(&pw, kReqSpace);  qi_w_bytes(&pw, space_, 32);
  qi_w_uint(&pw, kReqDevice); qi_w_bytes(&pw, qi.core().device_pub, 32);
  qi_w_uint(&pw, kReqSig);    qi_w_bytes(&pw, sig, 64);
  size_t pn;
  if (qi_w_done(&pw, &pn) != QI_OK) return false;

  qi_cbor_w w;
  qi_w_init(&w, msgBuf_, sizeof msgBuf_);
  qi_w_map(&w, 2);
  qi_w_uint(&w, kKeyType);     qi_w_uint(&w, kMsgEpochReq);
  qi_w_uint(&w, kKeyEpochReq); qi_w_bytes(&w, payload, pn);
  size_t mn;
  if (qi_w_done(&w, &mn) != QI_OK) return false;
  return writePacket(msgBuf_, mn);
}

// ---- inbound -------------------------------------------------------------

void QuietWiFiBearer::readInbound(QuietInstrument &qi) {
  // Bounded per poll so a chatty node cannot starve the sensors.
  for (int rounds = 0; rounds < 8; rounds++) {
    int avail = client_.available();
    if (avail <= 0) return;
    if (rxSkipping_) {
      // an oversized packet: drain it and resync on the next header
      size_t take = (size_t)avail < rxSkip_ ? (size_t)avail : rxSkip_;
      if (take > sizeof rx_) take = sizeof rx_;
      int got = client_.read(rx_, take);
      if (got <= 0) return;
      rxSkip_ -= (size_t)got;
      if (rxSkip_ == 0) rxSkipping_ = false;
      continue;
    }
    if (rxNeed_ == 0) {
      if (avail < 4) return;
      uint8_t h[4];
      if (client_.read(h, 4) != 4) { drop("header read failed"); return; }
      size_t n = ((size_t)h[0] << 24) | ((size_t)h[1] << 16) | ((size_t)h[2] << 8) | h[3];
      if (n == 0 || n > (1u << 20)) { drop("bad packet length"); return; }
      if (n > sizeof rx_) { rxSkipping_ = true; rxSkip_ = n; continue; }
      rxNeed_ = n;
      rxHave_ = 0;
      continue;
    }
    size_t want = rxNeed_ - rxHave_;
    if ((size_t)avail < want) want = (size_t)avail;
    int got = client_.read(rx_ + rxHave_, want);
    if (got <= 0) return;
    rxHave_ += (size_t)got;
    if (rxHave_ < rxNeed_) continue;
    // one whole fragment packet: unwrap (total==1 only — everything the
    // door sends is single-fragment by construction), then the message
    qi_cbor_r r;
    qi_r_init(&r, rx_, rxNeed_);
    rxNeed_ = rxHave_ = 0;
    size_t fields;
    if (qi_r_map(&r, &fields) != QI_OK) continue;
    uint64_t total = 0;
    const uint8_t *chunk = NULL; size_t chunkN = 0;
    bool bad = false;
    for (size_t i = 0; i < fields && !bad; i++) {
      uint64_t key, v;
      if (qi_r_uint(&r, &key) != QI_OK) { bad = true; break; }
      if (key == kFragTotal) { if (qi_r_uint(&r, &total) != QI_OK) bad = true; }
      else if (key == kFragChunk) { if (qi_r_bytes(&r, &chunk, &chunkN) != QI_OK) bad = true; }
      else if (key == kFragStream || key == kFragIndex) { if (qi_r_uint(&r, &v) != QI_OK) bad = true; }
      else if (qi_r_skip(&r) != QI_OK) bad = true;
    }
    if (bad || total != 1 || !chunk) continue;
    handleMessage(qi, chunk, chunkN);
  }
}

// Only one message matters to a courier: the epochs freight. Hellos and
// summaries from the node are a peer's vocabulary and are skipped whole.
void QuietWiFiBearer::handleMessage(QuietInstrument &qi, const uint8_t *msg, size_t n) {
  qi_cbor_r r;
  qi_r_init(&r, msg, n);
  size_t fields;
  if (qi_r_map(&r, &fields) != QI_OK) return;
  uint64_t type = 0;
  const uint8_t *payload = NULL; size_t payloadN = 0;
  for (size_t i = 0; i < fields; i++) {
    uint64_t key;
    if (qi_r_uint(&r, &key) != QI_OK) return;
    if (key == kKeyType) { if (qi_r_uint(&r, &type) != QI_OK) return; }
    else if (key == kKeyEpochs) { if (qi_r_bytes(&r, &payload, &payloadN) != QI_OK) return; }
    else if (qi_r_skip(&r) != QI_OK) return;
  }
  if (type != kMsgEpochs || !payload) return;

  qi_cbor_r p;
  qi_r_init(&p, payload, payloadN);
  if (qi_r_map(&p, &fields) != QI_OK) return;
  uint64_t unix = 0;
  bool spaceOK = false;
  int learned = 0;
  for (size_t i = 0; i < fields; i++) {
    uint64_t key;
    if (qi_r_uint(&p, &key) != QI_OK) return;
    if (key == kEpSpace) {
      const uint8_t *s; size_t sn;
      if (qi_r_bytes(&p, &s, &sn) != QI_OK) return;
      spaceOK = sn == 32 && !memcmp(s, space_, 32);
    } else if (key == kEpFrames) {
      size_t nf;
      if (qi_r_array(&p, &nf) != QI_OK) return;
      for (size_t f = 0; f < nf; f++) {
        const uint8_t *fb; size_t fn;
        if (qi_r_bytes(&p, &fb, &fn) != QI_OK) return;
        if (qi.ingestEpochFrame(fb, fn)) learned++;
      }
    } else if (key == kEpUnix) {
      if (qi_r_uint(&p, &unix) != QI_OK) return;
    } else if (qi_r_skip(&p) != QI_OK) {
      return;
    }
  }
  if (!spaceOK) return;
  // law 4: the clock is a floor
  if (unix > timeFloor_ && unix > qi.unixNow()) { timeFloor_ = unix; qi.setUnixTime(unix); }
  if (!ready_) {
    ready_ = true;
    stateWord_ = "in";
    note("door answered — in the space");
  }
  (void)learned;
}
#endif
