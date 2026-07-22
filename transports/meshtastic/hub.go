package meshtastic

import (
	"bufio"
	"net"
	"sync"
)

// Hub is a fake mesh for development and tests: every TCP client acts as a
// Meshtastic device (config handshake, node number), and each data packet
// is rebroadcast to all other clients. LoRa-in-a-box — with none of LoRa's
// loss, delay, or airtime limits, so treat green tests as protocol proof,
// not radio proof.
type Hub struct {
	l       net.Listener
	mu      sync.Mutex
	clients map[net.Conn]uint32
	nextNum uint32
}

// StartHub listens on addr (e.g. "127.0.0.1:0").
func StartHub(addr string) (*Hub, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	h := &Hub{l: l, clients: map[net.Conn]uint32{}, nextNum: 100}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			h.mu.Lock()
			h.nextNum++
			num := h.nextNum
			h.clients[c] = num
			h.mu.Unlock()
			go h.serve(c, num)
		}
	}()
	return h, nil
}

// Addr returns the listen address.
func (h *Hub) Addr() string { return h.l.Addr().String() }

// Close stops the hub.
func (h *Hub) Close() error { return h.l.Close() }

func hubMyInfo(num uint32) []byte {
	inner := appendVarintField(nil, 1, uint64(num))
	return appendBytesField(nil, 3, inner) // FromRadio.my_info
}

func hubConfigComplete(id uint32) []byte {
	return appendVarint(appendTag(nil, 7, wireVarint), uint64(id))
}

func hubPacket(from uint32, portnum uint32, payload []byte) []byte {
	data := appendVarintField(nil, 1, uint64(portnum))
	data = appendBytesField(data, 2, payload)
	pkt := appendFixed32Field(nil, 1, from)
	pkt = appendFixed32Field(pkt, 2, Broadcast)
	pkt = appendBytesField(pkt, 4, data)
	return appendBytesField(nil, 2, pkt) // FromRadio.packet
}

func (h *Hub) serve(c net.Conn, num uint32) {
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
		c.Close()
	}()
	br := bufio.NewReader(c)
	for {
		frame, err := readFrame(br)
		if err != nil {
			return
		}
		r := &reader{b: frame}
		for !r.done() {
			tag, err := r.varint()
			if err != nil {
				return
			}
			field, wt := int(tag>>3), int(tag&7)
			switch {
			case field == 3 && wt == wireVarint: // want_config_id
				id64, err := r.varint()
				if err != nil {
					return
				}
				writeFrame(c, hubMyInfo(num))
				writeFrame(c, hubConfigComplete(uint32(id64)))
			case field == 1 && wt == wireBytes: // outgoing MeshPacket
				raw, err := r.bytes()
				if err != nil {
					return
				}
				pkt, err := decodeMeshPacket(raw)
				if err != nil || len(pkt.Payload) == 0 {
					continue
				}
				out := hubPacket(num, pkt.Portnum, pkt.Payload)
				h.mu.Lock()
				for peer, peerNum := range h.clients {
					if peerNum != num {
						writeFrame(peer, out)
					}
				}
				h.mu.Unlock()
			default:
				if err := r.skip(wt); err != nil {
					return
				}
			}
		}
	}
}
