package relay

import (
	"testing"
	"time"
)

func TestAWriteWaitsForItsBodyNotForADeadSocket(t *testing.T) {
	if got := putTimeout(0); got != 4*time.Second {
		t.Fatalf("an empty write waits %s, want 4s", got)
	}
	if got := putTimeout(200); got < 4*time.Second || got > 4*time.Second+10*time.Millisecond {
		t.Fatalf("a one-line message waits %s, want about 4s", got)
	}
	if got := putTimeout(768 << 10); got != 10*time.Second {
		t.Fatalf("a full item waits %s, want the 10s it always had", got)
	}
}
