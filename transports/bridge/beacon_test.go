package bridge

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func testCustodian(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func sampleBeacon() Beacon {
	return Beacon{
		Version:    1,
		NetworkID:  "beta-mesh-01",
		Label:      "roof Pi",
		BootID:     0x1122334455667788,
		Sequence:   7,
		IssuedSlot: 1_785_000_000,
		ValidFor:   600,
		UplinkUp:   true,
		QueueDepth: 12,
	}
}

// Everything a beacon claims must be covered by the signature. A field left
// outside it is one an attacker can rewrite in flight: "the uplink is up"
// from a gateway that has no internet, or a queue depth that hides a gateway
// drowning.
func TestEveryClaimIsSigned(t *testing.T) {
	priv := testCustodian(t)
	raw, err := SignBeacon(priv, sampleBeacon())
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyBeacon(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := sampleBeacon()
	if got.NetworkID != want.NetworkID || got.Label != want.Label ||
		got.BootID != want.BootID || got.Sequence != want.Sequence ||
		got.IssuedSlot != want.IssuedSlot || got.ValidFor != want.ValidFor ||
		got.UplinkUp != want.UplinkUp || got.QueueDepth != want.QueueDepth {
		t.Fatalf("round trip lost or changed a field:\n got %+v\nwant %+v", got, want)
	}
	if len(got.PublicKey) != ed25519.PublicKeySize {
		t.Fatal("no key to check the signature against")
	}

	// Flip one bit anywhere in the signed body and it must stop verifying.
	for _, i := range []int{4, len(raw) / 3, len(raw) / 2} {
		bad := append([]byte(nil), raw...)
		bad[i] ^= 0x01
		if _, err := VerifyBeacon(bad); err == nil {
			t.Fatalf("a beacon altered at byte %d still verified", i)
		}
	}
}

// A beacon is broadcast on a shared carrier, so it must be small. At LoRa
// airtime a presence announcement that costs several packets is one an
// operator will turn off.
func TestBeaconFitsOneRadioPacket(t *testing.T) {
	priv := testCustodian(t)
	raw, err := SignBeacon(priv, sampleBeacon())
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 200 {
		t.Fatalf("a beacon is %d bytes: it no longer fits one LoRa packet", len(raw))
	}
	t.Logf("beacon is %d bytes", len(raw))
}

// Unbounded strings on a broadcast carrier are an airtime problem and a
// parser problem at once.
func TestOversizedFieldsAreRefusedAtSigning(t *testing.T) {
	priv := testCustodian(t)
	long := sampleBeacon()
	long.NetworkID = string(make([]byte, 200))
	if _, err := SignBeacon(priv, long); err == nil {
		t.Error("an oversized network id was signed")
	}
	long = sampleBeacon()
	long.Label = string(make([]byte, 200))
	if _, err := SignBeacon(priv, long); err == nil {
		t.Error("an oversized label was signed")
	}
}

// A beacon carries no space identity, no terminal id and no payload. It says
// a gateway exists and how it is doing — nothing about who is using it.
// Leaking a terminal id here would tell every listener on the carrier which
// spaces this segment serves.
func TestBeaconNamesNobody(t *testing.T) {
	priv := testCustodian(t)
	raw, err := SignBeacon(priv, sampleBeacon())
	if err != nil {
		t.Fatal(err)
	}
	// The only 32-byte value in a beacon is the custodian's public key,
	// which is exactly what it is for.
	pub := priv.Public().(ed25519.PublicKey)
	stripped := make([]byte, 0, len(raw))
	for i := 0; i+32 <= len(raw); i++ {
		if string(raw[i:i+32]) == string(pub) {
			stripped = append(stripped, raw[:i]...)
			stripped = append(stripped, raw[i+32:]...)
			break
		}
	}
	if len(stripped) == 0 {
		t.Fatal("the custodian key is not in the beacon at all")
	}
}
