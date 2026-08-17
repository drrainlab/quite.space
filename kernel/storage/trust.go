// The device trust store (MD-0, ADR-002): who this node believes is a device
// of whom, and who has been revoked.
//
// Two keys, because the two things have different lifetimes and mixing them
// would blur that:
//
//	ksKeyCerts   certificates AND revocations — one trust store, and they
//	             must land in ONE atomic write: a revocation persisted
//	             without the certificate it answers, or the reverse, is a
//	             window in which a revoked device is admitted again.
//	ksKeyLegacy  the frozen migration allowlist — written exactly once, when
//	             this build first opens a keystore that predates it, and
//	             never appended to afterwards. An allowlist that keeps
//	             growing is the open door it was written to close, wearing
//	             a longer name.
//
// Frames are stored ENCODED and opaque, the way PassRecord keeps its signed
// pass and SpaceMeta keeps its manifest. A frame that came back from disk is
// verified exactly like one that came off the wire (kernel/identity), because
// a plain file is not a reason to trust bytes.
package storage

import (
	"errors"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

var errMalformedTrustEntry = errors.New("storage: malformed trust entry")

// CertRecord is one device certificate as it was received. Device is carried
// beside the frame so a lookup costs no decode; the frame remains the truth.
type CertRecord struct {
	Device id.DeviceID
	Frame  []byte // identity.Certificate.Encode()
}

// RevRecord is one revocation, same shape and for the same reason.
type RevRecord struct {
	Device id.DeviceID
	Frame  []byte // identity.Revocation.Encode()
}

// LegacyBinding is a (principal, device) pair that already had history here
// when this build first ran. Those devices predate certification and are
// admitted without one — forever, and only them.
type LegacyBinding struct {
	Principal id.PrincipalID
	Device    id.DeviceID
}

// Record arities, NAMED — see SpaceMeta's unnamed literals for what a bare
// number costs here. Append only, and bump when you do.
const (
	certFields   = 2
	revFields    = 2
	legacyFields = 2
)

func appendCertRecord(buf []byte, c CertRecord) []byte {
	buf = codec.AppendArray(buf, certFields)
	buf = codec.AppendBytes(buf, c.Device[:])
	buf = codec.AppendBytes(buf, c.Frame)
	return buf
}

func readCertRecord(d *codec.Decoder) (CertRecord, error) {
	var c CertRecord
	acount, err := d.ReadArray()
	if err != nil {
		return c, err
	}
	if acount >= 1 {
		raw, er := d.ReadBytes()
		if er != nil {
			return c, er
		}
		if len(raw) != len(id.DeviceID{}) {
			return c, errMalformedTrustEntry
		}
		copy(c.Device[:], raw)
	}
	if acount >= 2 {
		if c.Frame, err = d.ReadBytes(); err != nil {
			return c, err
		}
	}
	// A newer build appended something: skip it rather than dying mid-record.
	for i := certFields; i < acount; i++ {
		if err := d.SkipItem(); err != nil {
			return c, err
		}
	}
	return c, nil
}

func appendRevRecord(buf []byte, r RevRecord) []byte {
	buf = codec.AppendArray(buf, revFields)
	buf = codec.AppendBytes(buf, r.Device[:])
	buf = codec.AppendBytes(buf, r.Frame)
	return buf
}

func readRevRecord(d *codec.Decoder) (RevRecord, error) {
	var r RevRecord
	acount, err := d.ReadArray()
	if err != nil {
		return r, err
	}
	if acount >= 1 {
		raw, er := d.ReadBytes()
		if er != nil {
			return r, er
		}
		if len(raw) != len(id.DeviceID{}) {
			return r, errMalformedTrustEntry
		}
		copy(r.Device[:], raw)
	}
	if acount >= 2 {
		if r.Frame, err = d.ReadBytes(); err != nil {
			return r, err
		}
	}
	for i := revFields; i < acount; i++ {
		if err := d.SkipItem(); err != nil {
			return r, err
		}
	}
	return r, nil
}

// appendTrustStore writes certificates and revocations as one top-level
// value: a 2-array of [certs, revocations]. One value, one atomic write.
func appendTrustStore(buf []byte, certs []CertRecord, revs []RevRecord) []byte {
	buf = codec.AppendArray(buf, 2)
	buf = codec.AppendArray(buf, len(certs))
	for _, c := range certs {
		buf = appendCertRecord(buf, c)
	}
	buf = codec.AppendArray(buf, len(revs))
	for _, r := range revs {
		buf = appendRevRecord(buf, r)
	}
	return buf
}

func readTrustStore(d *codec.Decoder) (certs []CertRecord, revs []RevRecord, err error) {
	outer, err := d.ReadArray()
	if err != nil {
		return nil, nil, err
	}
	if outer >= 1 {
		n, er := d.ReadArray()
		if er != nil {
			return nil, nil, er
		}
		for i := 0; i < n; i++ {
			c, er := readCertRecord(d)
			if er != nil {
				return nil, nil, er
			}
			certs = append(certs, c)
		}
	}
	if outer >= 2 {
		n, er := d.ReadArray()
		if er != nil {
			return nil, nil, er
		}
		for i := 0; i < n; i++ {
			r, er := readRevRecord(d)
			if er != nil {
				return nil, nil, er
			}
			revs = append(revs, r)
		}
	}
	for i := 2; i < outer; i++ {
		if er := d.SkipItem(); er != nil {
			return nil, nil, er
		}
	}
	return certs, revs, nil
}

func appendLegacyBindings(buf []byte, bs []LegacyBinding) []byte {
	buf = codec.AppendArray(buf, len(bs))
	for _, b := range bs {
		buf = codec.AppendArray(buf, legacyFields)
		buf = codec.AppendBytes(buf, b.Principal[:])
		buf = codec.AppendBytes(buf, b.Device[:])
	}
	return buf
}

func readLegacyBindings(d *codec.Decoder) ([]LegacyBinding, error) {
	n, err := d.ReadArray()
	if err != nil {
		return nil, err
	}
	out := make([]LegacyBinding, 0, n)
	for i := 0; i < n; i++ {
		acount, er := d.ReadArray()
		if er != nil {
			return nil, er
		}
		var b LegacyBinding
		if acount >= 1 {
			raw, er := d.ReadBytes()
			if er != nil {
				return nil, er
			}
			if len(raw) != len(id.PrincipalID{}) {
				return nil, errMalformedTrustEntry
			}
			copy(b.Principal[:], raw)
		}
		if acount >= 2 {
			raw, er := d.ReadBytes()
			if er != nil {
				return nil, er
			}
			if len(raw) != len(id.DeviceID{}) {
				return nil, errMalformedTrustEntry
			}
			copy(b.Device[:], raw)
		}
		for k := legacyFields; k < acount; k++ {
			if er := d.SkipItem(); er != nil {
				return nil, er
			}
		}
		out = append(out, b)
	}
	return out, nil
}
