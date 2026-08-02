// Minimal protobuf subset for the Meshtastic Client API (plan §19 T6).
// Field numbers are taken from the official meshtastic/protobufs
// (mesh.proto, portnums.proto) and are wire-stable:
//
//	ToRadio:    packet=1, want_config_id=3, heartbeat=7
//	FromRadio:  packet=2, my_info=3, config_complete_id=7
//	MeshPacket: from=1(f32), to=2(f32), channel=3, decoded=4, encrypted=5,
//	            id=6(f32), hop_limit=9, want_ack=10
//	Data:       portnum=1, payload=2
//	MyNodeInfo: my_node_num=1
//
// Hand-rolling this tiny subset keeps the client CGO-free and
// dependency-light (ADR-011); unknown fields are skipped, never errors
// (the firmware evolves faster than we do).
package meshtastic

import (
	"errors"
	"fmt"
)

// PortPrivateApp is the Meshtastic portnum for third-party apps.
const PortPrivateApp = 256

// PortRoutingApp carries the mesh's own control messages. It is how the
// firmware reports the FATE of a packet we asked it to deliver reliably:
// a Routing message whose error_reason is NONE for an acknowledgement, or
// MAX_RETRANSMIT when it gave up after its retries. Without reading it, a
// client that sets want_ack cannot tell a retry that worked from one that
// never happened.
const PortRoutingApp = 5

// Routing.Error values we act on (mesh.proto). Others are recorded by
// number rather than mapped onto a name we do not have.
const (
	RoutingNone          = 0
	RoutingMaxRetransmit = 5
)

// DecodeRoutingError reads a Routing payload's error_reason (field 3).
// Reports false when the payload carries a route request or reply instead,
// which is a different message and not an outcome for anything we sent.
func DecodeRoutingError(b []byte) (int32, bool) {
	r := &reader{b: b}
	for !r.done() {
		tag, err := r.varint()
		if err != nil {
			return 0, false
		}
		field, wt := int(tag>>3), int(tag&7)
		if field == 3 && wt == wireVarint {
			v, err := r.varint()
			if err != nil {
				return 0, false
			}
			return int32(v), true
		}
		if err := r.skip(wt); err != nil {
			return 0, false
		}
	}
	return 0, false
}

// Broadcast is MeshPacket.to for channel-wide delivery.
const Broadcast = 0xFFFFFFFF

// DataPayloadMax is DATA_PAYLOAD_LEN from the official protobufs.
const DataPayloadMax = 233

const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func appendTag(b []byte, field, wt int) []byte {
	return appendVarint(b, uint64(field)<<3|uint64(wt))
}

func appendVarintField(b []byte, field int, v uint64) []byte {
	if v == 0 {
		return b
	}
	return appendVarint(appendTag(b, field, wireVarint), v)
}

func appendBoolField(b []byte, field int, v bool) []byte {
	if !v {
		return b
	}
	return appendVarint(appendTag(b, field, wireVarint), 1)
}

func appendFixed32Field(b []byte, field int, v uint32) []byte {
	if v == 0 {
		return b
	}
	b = appendTag(b, field, wireFixed32)
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

func appendBytesField(b []byte, field int, payload []byte) []byte {
	b = appendTag(b, field, wireBytes)
	b = appendVarint(b, uint64(len(payload)))
	return append(b, payload...)
}

// EncodeDataPacket builds ToRadio{packet: MeshPacket{to, channel,
// decoded: Data{portnum, payload}, id, hop_limit, want_ack}}.
// from is omitted: the radio fills in its own node number.
func EncodeDataPacket(to, channel, portnum uint32, payload []byte, packetID uint32, hopLimit uint32, wantAck bool) []byte {
	data := appendVarintField(nil, 1, uint64(portnum))
	data = appendBytesField(data, 2, payload)

	pkt := appendFixed32Field(nil, 2, to)
	pkt = appendVarintField(pkt, 3, uint64(channel))
	pkt = appendBytesField(pkt, 4, data)
	pkt = appendFixed32Field(pkt, 6, packetID)
	pkt = appendVarintField(pkt, 9, uint64(hopLimit))
	pkt = appendBoolField(pkt, 10, wantAck)

	return appendBytesField(nil, 1, pkt)
}

// EncodeWantConfig builds ToRadio{want_config_id}.
func EncodeWantConfig(id uint32) []byte {
	return appendVarint(appendTag(nil, 3, wireVarint), uint64(id))
}

// EncodeHeartbeat builds ToRadio{heartbeat: Heartbeat{}} — keeps TCP links
// alive (the firmware drops idle clients).
func EncodeHeartbeat() []byte {
	return appendBytesField(nil, 7, nil)
}

// RxPacket is a received mesh packet we could decode.
type RxPacket struct {
	From    uint32
	To      uint32
	Channel uint32
	Portnum uint32
	Payload []byte
	ID      uint32
	// RequestID names the packet this one answers for (Data field 6). On a
	// ROUTING_APP packet it is the id of OUR packet whose fate is being
	// reported.
	RequestID uint32
	Encrypted bool // decoded absent: the radio gave us ciphertext only
}

// ConfigField is one configuration-bearing FromRadio member, kept raw so
// config.go can decode it without this file growing a second schema.
type ConfigField struct {
	Field int
	Raw   []byte
}

// FromRadioMsg is the subset of FromRadio we care about.
type FromRadioMsg struct {
	Packet           *RxPacket
	MyNodeNum        *uint32
	ConfigCompleteID *uint32
	// Config carries config=5, channel=10 and metadata=13 (RB-2).
	Config []ConfigField
	// Queue is the radio's report on its own outgoing queue (field 11).
	// It is the only place the firmware says whether it actually took the
	// last packet, and how much room is left — the difference between a
	// client that paces itself and one that pours fragments into a queue
	// that quietly overflows.
	Queue *QueueStatus
	// Skipped lists the top-level field numbers this build did not read.
	// Must-ignore is the rule, but silently ignoring is how a transcription
	// error in a hand-written protobuf subset stays invisible: `--raw`
	// prints these so real hardware can correct us.
	Skipped []int
}

// QueueStatus mirrors meshtastic.QueueStatus (mesh.proto). Field numbers
// transcribed from the reference protobufs:
//
//	res=1, free=2, maxlen=3, mesh_packet_id=4
type QueueStatus struct {
	// Res is the firmware's error code for the last queue attempt. Zero is
	// success; anything else means that packet was NOT queued.
	Res int32
	// Free is how many entries remain in the outgoing queue, and Maxlen how
	// many there are in total.
	Free   int32
	Maxlen int32
	// PacketID names the packet this report answers for, so a caller can
	// tell a refusal of ITS packet from a status that merely arrived.
	PacketID uint32
}

// Accepted reports whether the packet this status answers for was queued.
func (q QueueStatus) Accepted() bool { return q.Res == 0 }

func decodeQueueStatus(b []byte) (*QueueStatus, error) {
	q := &QueueStatus{}
	r := &reader{b: b}
	for !r.done() {
		tag, err := r.varint()
		if err != nil {
			return nil, err
		}
		field, wt := int(tag>>3), int(tag&7)
		if wt != wireVarint {
			if err := r.skip(wt); err != nil {
				return nil, err
			}
			continue
		}
		v, err := r.varint()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			q.Res = int32(v)
		case 2:
			q.Free = int32(v)
		case 3:
			q.Maxlen = int32(v)
		case 4:
			q.PacketID = uint32(v)
		}
	}
	return q, nil
}

type reader struct {
	b   []byte
	pos int
}

func (r *reader) done() bool { return r.pos >= len(r.b) }

func (r *reader) varint() (uint64, error) {
	var v uint64
	var shift uint
	for {
		if r.pos >= len(r.b) {
			return 0, errors.New("meshtastic: truncated varint")
		}
		c := r.b[r.pos]
		r.pos++
		v |= uint64(c&0x7f) << shift
		if c < 0x80 {
			return v, nil
		}
		shift += 7
		if shift > 63 {
			return 0, errors.New("meshtastic: varint overflow")
		}
	}
}

func (r *reader) fixed32() (uint32, error) {
	if r.pos+4 > len(r.b) {
		return 0, errors.New("meshtastic: truncated fixed32")
	}
	v := uint32(r.b[r.pos]) | uint32(r.b[r.pos+1])<<8 |
		uint32(r.b[r.pos+2])<<16 | uint32(r.b[r.pos+3])<<24
	r.pos += 4
	return v, nil
}

func (r *reader) bytes() ([]byte, error) {
	n, err := r.varint()
	if err != nil {
		return nil, err
	}
	if uint64(r.pos)+n > uint64(len(r.b)) {
		return nil, errors.New("meshtastic: truncated bytes")
	}
	out := r.b[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return out, nil
}

func (r *reader) skip(wt int) error {
	switch wt {
	case wireVarint:
		_, err := r.varint()
		return err
	case wireFixed64:
		if r.pos+8 > len(r.b) {
			return errors.New("meshtastic: truncated fixed64")
		}
		r.pos += 8
		return nil
	case wireBytes:
		_, err := r.bytes()
		return err
	case wireFixed32:
		_, err := r.fixed32()
		return err
	default:
		return fmt.Errorf("meshtastic: unsupported wire type %d", wt)
	}
}

// DecodeFromRadio parses one FromRadio frame, skipping unknown fields.
func DecodeFromRadio(b []byte) (*FromRadioMsg, error) {
	msg := &FromRadioMsg{}
	r := &reader{b: b}
	for !r.done() {
		tag, err := r.varint()
		if err != nil {
			return nil, err
		}
		field, wt := int(tag>>3), int(tag&7)
		switch {
		case field == 2 && wt == wireBytes: // packet
			raw, err := r.bytes()
			if err != nil {
				return nil, err
			}
			pkt, err := decodeMeshPacket(raw)
			if err != nil {
				return nil, err
			}
			msg.Packet = pkt
		case field == 3 && wt == wireBytes: // my_info
			raw, err := r.bytes()
			if err != nil {
				return nil, err
			}
			num, err := decodeMyNodeNum(raw)
			if err != nil {
				return nil, err
			}
			msg.MyNodeNum = &num
		case field == 7 && wt == wireVarint: // config_complete_id
			v, err := r.varint()
			if err != nil {
				return nil, err
			}
			u := uint32(v)
			msg.ConfigCompleteID = &u
		case field == 11 && wt == wireBytes: // queue_status
			raw, err := r.bytes()
			if err != nil {
				return nil, err
			}
			q, err := decodeQueueStatus(raw)
			if err != nil {
				return nil, err
			}
			msg.Queue = q
		case (field == 5 || field == 10 || field == 13) && wt == wireBytes:
			// config / channel / metadata — decoded in config.go.
			raw, err := r.bytes()
			if err != nil {
				return nil, err
			}
			msg.Config = append(msg.Config, ConfigField{
				Field: field, Raw: append([]byte(nil), raw...)})
		default:
			msg.Skipped = append(msg.Skipped, field)
			if err := r.skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return msg, nil
}

func decodeMeshPacket(b []byte) (*RxPacket, error) {
	p := &RxPacket{}
	r := &reader{b: b}
	for !r.done() {
		tag, err := r.varint()
		if err != nil {
			return nil, err
		}
		field, wt := int(tag>>3), int(tag&7)
		switch {
		case field == 1 && wt == wireFixed32:
			p.From, err = r.fixed32()
		case field == 2 && wt == wireFixed32:
			p.To, err = r.fixed32()
		case field == 3 && wt == wireVarint:
			var v uint64
			v, err = r.varint()
			p.Channel = uint32(v)
		case field == 4 && wt == wireBytes: // decoded Data
			var raw []byte
			raw, err = r.bytes()
			if err == nil {
				err = decodeData(raw, p)
			}
		case field == 5 && wt == wireBytes: // encrypted
			_, err = r.bytes()
			p.Encrypted = true
		case field == 6 && wt == wireFixed32:
			p.ID, err = r.fixed32()
		default:
			err = r.skip(wt)
		}
		if err != nil {
			return nil, err
		}
	}
	return p, nil
}

func decodeData(b []byte, p *RxPacket) error {
	r := &reader{b: b}
	for !r.done() {
		tag, err := r.varint()
		if err != nil {
			return err
		}
		field, wt := int(tag>>3), int(tag&7)
		switch {
		case field == 1 && wt == wireVarint:
			v, err := r.varint()
			if err != nil {
				return err
			}
			p.Portnum = uint32(v)
		case field == 2 && wt == wireBytes:
			raw, err := r.bytes()
			if err != nil {
				return err
			}
			p.Payload = append([]byte(nil), raw...)
		case field == 6 && wt == wireVarint: // request_id
			v, err := r.varint()
			if err != nil {
				return err
			}
			p.RequestID = uint32(v)
		case field == 6 && wt == wireFixed32: // request_id, some builds
			v, err := r.fixed32()
			if err != nil {
				return err
			}
			p.RequestID = v
		default:
			if err := r.skip(wt); err != nil {
				return err
			}
		}
	}
	return nil
}

func decodeMyNodeNum(b []byte) (uint32, error) {
	r := &reader{b: b}
	for !r.done() {
		tag, err := r.varint()
		if err != nil {
			return 0, err
		}
		field, wt := int(tag>>3), int(tag&7)
		if field == 1 && wt == wireVarint {
			v, err := r.varint()
			return uint32(v), err
		}
		if err := r.skip(wt); err != nil {
			return 0, err
		}
	}
	return 0, errors.New("meshtastic: my_info without my_node_num")
}
