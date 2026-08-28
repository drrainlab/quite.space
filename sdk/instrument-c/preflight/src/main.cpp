// Preflight: the four libsodium calls the instrument core depends on.
// Prints PASS/FAIL over serial; the build itself is the first answer.
#include <Arduino.h>
#include <sodium.h>

void setup() {
  Serial.begin(115200);
  delay(500);
  if (sodium_init() < 0) { Serial.println("PREFLIGHT FAIL: sodium_init"); return; }
  unsigned char seed[32]; for (int i = 0; i < 32; i++) seed[i] = 0x31;
  unsigned char pk[32], sk[64];
  crypto_sign_seed_keypair(pk, sk, seed);
  unsigned char xs[32]; for (int i = 0; i < 32; i++) xs[i] = 0x44;
  unsigned char xp[32];
  if (crypto_scalarmult_curve25519_base(xp, xs) != 0) { Serial.println("PREFLIGHT FAIL: x25519"); return; }
  unsigned char key[32]; for (int i = 0; i < 32; i++) key[i] = 0x55;
  unsigned char nonce[24]; for (int i = 0; i < 24; i++) nonce[i] = 0x66;
  const unsigned char msg[] = "quiet";
  unsigned char ct[sizeof(msg) + crypto_aead_xchacha20poly1305_ietf_ABYTES];
  unsigned long long clen = 0;
  crypto_aead_xchacha20poly1305_ietf_encrypt(ct, &clen, msg, sizeof(msg), NULL, 0, NULL, nonce, key);
  unsigned char h[32];
  crypto_hash_sha256(h, msg, sizeof(msg));
  Serial.printf("PREFLIGHT PASS: ed25519 pk[0]=%02x x25519 pk[0]=%02x aead clen=%llu sha256[0]=%02x\n",
                pk[0], xp[0], clen, h[0]);
}

void loop() { delay(1000); }
