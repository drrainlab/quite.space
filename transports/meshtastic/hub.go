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
	cfg     *HubConfig
}

// HubConfig is the configuration simulated devices report during the
// handshake. A fake node that cannot be misconfigured is no use for testing
// the diagnostic that exists to catch misconfiguration, so the Hub can be
// told to describe itself as any radio — including a wrong one.
//
// Nil (the default) means the device reports no configuration at all, which
// is what older firmware does and what every test written before RB-2 saw.
type HubConfig struct {
	Region      uint32
	ModemPreset uint32
	HopLimit    uint32
	UsePreset   bool
	TxEnabled   bool
	ChannelName string
	// PSK is the raw channel key the simulated device reports. Only ever a
	// test value: it exists so the diagnostic can be shown to hash it and
	// never hand it back.
	PSK      []byte
	Firmware string
}

// SetConfig makes devices accepted from now on report cfg.
func (h *Hub) SetConfig(cfg *HubConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg = cfg
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
func (h *Hub) Close() error {
	h.DropAll()
	return h.l.Close()
}

// DropAll disconnects every client but keeps listening — a radio being
// unplugged and plugged back in, or rebooting after a config change. The
// address stays the same, which is what makes reconnection testable.
func (h *Hub) DropAll() {
	h.mu.Lock()
	conns := make([]net.Conn, 0, len(h.clients))
	for c := range h.clients {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

func hubMyInfo(num uint32) []byte {
	inner := appendVarintField(nil, 1, uint64(num))
	return appendBytesField(nil, 3, inner) // FromRadio.my_info
}

func hubConfigComplete(id uint32) []byte {
	return appendVarint(appendTag(nil, 7, wireVarint), uint64(id))
}

// hubConfigFrames renders a HubConfig as the frames real firmware emits.
//
// The field numbers are written out again here rather than shared with the
// decoder in config.go: two independent transcriptions of the same schema
// catch a typo in either. They cannot catch a systematic misreading of the
// upstream .proto — only real hardware can, which is what `quiet-radio
// --raw` is for.
func hubConfigFrames(c *HubConfig) [][]byte {
	if c == nil {
		return nil
	}
	var out [][]byte
	if c.Firmware != "" {
		md := appendBytesField(nil, 1, []byte(c.Firmware)) // firmware_version
		out = append(out, appendBytesField(nil, 13, md))   // FromRadio.metadata
	}
	lora := appendBoolField(nil, 1, c.UsePreset)             // use_preset
	lora = appendVarintField(lora, 2, uint64(c.ModemPreset)) // modem_preset
	lora = appendVarintField(lora, 7, uint64(c.Region))      // region
	lora = appendVarintField(lora, 8, uint64(c.HopLimit))    // hop_limit
	lora = appendBoolField(lora, 9, c.TxEnabled)             // tx_enabled
	cfg := appendBytesField(nil, 6, lora)                    // Config.lora
	out = append(out, appendBytesField(nil, 5, cfg))         // FromRadio.config

	set := appendBytesField(nil, 2, c.PSK)                // psk
	set = appendBytesField(set, 3, []byte(c.ChannelName)) // name
	ch := appendBytesField(nil, 2, set)                   // Channel.settings
	ch = appendVarintField(ch, 3, 1)                      // role = PRIMARY
	out = append(out, appendBytesField(nil, 10, ch))      // FromRadio.channel
	return out
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
				h.mu.Lock()
				frames := hubConfigFrames(h.cfg)
				h.mu.Unlock()
				for _, f := range frames {
					writeFrame(c, f)
				}
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
