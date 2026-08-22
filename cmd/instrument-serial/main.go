// instrument-serial — the DEV STAND between an ESP32 running
// QuietInstrument's SerialHexBearer and a local node opened with
// --dev-ingest (QI-M3). It is not a bearer and does not pretend to be
// one: it exists to prove that a board's bytes are the protocol's bytes
// before any real transport is chosen.
//
//	board ──USB serial── instrument-serial ──HTTP loopback── node
//
// What it does, line by line:
//
//	QI ENROLLMENT <hex>  → POST /instruments/enroll → "QI PROVISION <hex>"
//	QI FRAME <hex>       → POST /instruments/{iid}/ingest
//	(every 20 s)           GET  /instruments/epochs → "QI EPOCH <hex>" when
//	                       the current epoch changed (attach/detach/pairing)
//	on connect             "QI TIME <unix>", "QI PRINCIPAL <hex>", "QI ENROLL?"
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"go.bug.st/serial"
)

func main() {
	port := flag.String("port", "", "serial port, e.g. /dev/cu.usbserial-0001")
	baud := flag.Int("baud", 115200, "baud rate")
	api := flag.String("api", "http://127.0.0.1:7411", "node local API base URL")
	token := flag.String("token", "", "node API token (X-QP-Token)")
	space := flag.String("space", "", "space id (hex)")
	flag.Parse()
	if *port == "" || *token == "" || *space == "" {
		fmt.Fprintln(os.Stderr, "usage: instrument-serial --port DEV --api URL --token T --space HEX")
		os.Exit(2)
	}
	st := &stand{api: *api, token: *token, space: *space}
	principal, err := st.principal()
	if err != nil {
		fmt.Fprintln(os.Stderr, "node:", err)
		os.Exit(1)
	}
	s, err := serial.Open(*port, &serial.Mode{BaudRate: *baud})
	if err != nil {
		fmt.Fprintln(os.Stderr, "serial:", err)
		os.Exit(1)
	}
	defer s.Close()
	st.out = s
	fmt.Println("stand: node principal", principal[:16]+"…", "space", (*space)[:16]+"…")
	st.send("QI TIME " + fmt.Sprint(time.Now().Unix()))
	st.send("QI PRINCIPAL " + principal)
	st.send("QI ENROLL?")

	go st.epochWatch()
	sc := bufio.NewScanner(s)
	sc.Buffer(make([]byte, 1<<16), 1<<16)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "QI ENROLLMENT "):
			st.enroll(strings.TrimPrefix(line, "QI ENROLLMENT "))
		case strings.HasPrefix(line, "QI FRAME "):
			st.ingest(strings.TrimPrefix(line, "QI FRAME "))
		case strings.HasPrefix(line, "QI NOTE "):
			fmt.Println("board:", strings.TrimPrefix(line, "QI NOTE "))
		case line != "":
			fmt.Println("board?", line)
		}
	}
}

type stand struct {
	api, token, space string
	iid               string
	out               io.Writer
	lastEpoch         string
}

func (s *stand) send(line string) {
	fmt.Println("stand →", firstN(line, 80))
	fmt.Fprint(s.out, line+"\n")
}

func firstN(l string, n int) string {
	if len(l) > n {
		return l[:n] + "…"
	}
	return l
}

func (s *stand) call(method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.api+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("X-QP-Token", s.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (s *stand) principal() (string, error) {
	var st struct {
		PrincipalID string `json:"principal_id"`
	}
	if err := s.call("GET", "/api/status", nil, &st); err != nil {
		return "", err
	}
	if st.PrincipalID == "" {
		return "", fmt.Errorf("node reports no principal_id (older build?)")
	}
	return st.PrincipalID, nil
}

func (s *stand) enroll(hexEnroll string) {
	var r struct {
		InstrumentID string `json:"instrument_id"`
		ProvisionHex string `json:"provision_hex"`
	}
	if err := s.call("POST", "/api/spaces/"+s.space+"/instruments/enroll",
		map[string]string{"enrollment_hex": hexEnroll}, &r); err != nil {
		fmt.Println("enroll refused:", err)
		return
	}
	s.iid = r.InstrumentID
	fmt.Println("stand: enrolled instrument", s.iid[:16]+"…")
	s.send("QI PROVISION " + r.ProvisionHex)
}

func (s *stand) ingest(hexFrame string) {
	if s.iid == "" {
		fmt.Println("frame before enrollment — asking the board to enroll")
		s.send("QI ENROLL?")
		return
	}
	var r struct {
		Applied int `json:"applied"`
	}
	if err := s.call("POST", "/api/spaces/"+s.space+"/instruments/"+s.iid+"/ingest",
		map[string][]string{"frames_hex": {hexFrame}}, &r); err != nil {
		fmt.Println("ingest refused:", err)
		return
	}
	fmt.Printf("stand: frame applied=%d (%d bytes)\n", r.Applied, len(hexFrame)/2)
}

func (s *stand) epochWatch() {
	for {
		var r struct {
			FramesHex []string `json:"frames_hex"`
		}
		if err := s.call("GET", "/api/spaces/"+s.space+"/instruments/epochs", nil, &r); err == nil && len(r.FramesHex) > 0 {
			cur := r.FramesHex[len(r.FramesHex)-1]
			if cur != s.lastEpoch && s.iid != "" {
				s.lastEpoch = cur
				s.send("QI TIME " + fmt.Sprint(time.Now().Unix()))
				s.send("QI EPOCH " + cur)
			}
		}
		time.Sleep(20 * time.Second)
	}
}
