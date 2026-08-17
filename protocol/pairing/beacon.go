// The pairing beacon (MD-1): the fallback for the address that moved.
//
// The offer carries the parent's address as the FAST PATH — but DHCP does
// not care about ceremonies, and LAN discovery cannot help a fresh device:
// the existing beacons are keyed on SHARED spaces, and a device being
// paired shares none yet. So the pairing secret itself derives a beacon
// identity, and the parent announces under it in the exact shape of the
// existing space hints. A listener holding the offer can find the parent;
// nobody else learns anything they could use — the beacon id is one more
// HKDF output of a secret with a sixty-second life.
package pairing

import (
	"crypto/hkdf"
	"crypto/sha256"
)

const beaconInfo = "qs.pair.beacon.v1"

// BeaconID derives the 32-byte identity the parent's pairing beacon
// announces under. Deterministic from the secret alone — both ends must
// derive it before any session exists, since its whole job is FINDING the
// other end.
func BeaconID(secret [32]byte) [32]byte {
	out, err := hkdf.Key(sha256.New, secret[:], nil, beaconInfo, 32)
	if err != nil {
		// Only reachable with a broken hash; a zero id would announce
		// nothing anyone derives, which is the safe direction.
		return [32]byte{}
	}
	var id [32]byte
	copy(id[:], out)
	return id
}
