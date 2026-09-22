package relay

import "testing"

func TestPushHintsRideTheParkAndAbsentMeansAll(t *testing.T) {
	m := &Msg{Type: MsgListen, Hints: [][]byte{[]byte("aaaaaaaaaaaaaaaa"), []byte("bbbbbbbbbbbbbbbb")},
		PushHints: [][]byte{[]byte("aaaaaaaaaaaaaaaa")}, Push: "https://push.example/x", PushSet: true}
	back, err := DecodeMsg(m.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if len(back.PushHints) != 1 || string(back.PushHints[0]) != "aaaaaaaaaaaaaaaa" {
		t.Fatalf("push hints = %q", back.PushHints)
	}
	plain, err := DecodeMsg((&Msg{Type: MsgListen, Hints: m.Hints, Push: m.Push, PushSet: true}).Encode())
	if err != nil {
		t.Fatal(err)
	}
	if plain.PushHints != nil {
		t.Fatalf("an absent key decoded as %q", plain.PushHints)
	}
}
