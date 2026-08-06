// RR — how many spaces a person may hold before the relay stops answering.
//
// FOUND WHILE DEBUGGING SOMETHING ELSE, and it is not a rate limit. The relay
// bounds the number of capabilities in ONE Collect at CollectMaxHints (64),
// and the private pull path sends two per space — the current bucket and the
// previous one — plus a reply box per public space. So a node crosses the
// ceiling at around thirty-two spaces and its request is REFUSED, every tick,
// for as long as it holds them.
//
// The public ingress path already splits its capabilities into chunks of 64
// with a comment saying why. The private one, which is the one every ordinary
// space uses, does not. One path learned the lesson and the other did not.
//
// WHAT IT LOOKS LIKE TO A PERSON: nothing. The node keeps running, the relay
// stays reachable, sync reports failures that read as a flaky connection, and
// messages simply stop arriving. Twenty-five spaces is not an unusual number.
package node

import (
	"fmt"
	"testing"

	"github.com/drrainlab/quiet_places/transports/relay"
)

func TestManySpacesDoNotOverflowOneCollect(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := "127.0.0.1:" + itoa(port)

	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	if err := rt.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}

	// Thirty-five spaces: two capabilities each is seventy, past the relay's
	// ceiling of sixty-four. A person with thirty-five conversations is not
	// doing anything unusual.
	const spaces = 35
	for i := 0; i < spaces; i++ {
		if _, err := rt.CreateSpace(fmt.Sprintf("room %d", i)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := rt.PullFromRelay(addr); err != nil {
		t.Fatalf("pulling with %d spaces failed: %v\n\n"+
			"The relay bounds one Collect at %d capabilities and this path sends "+
			"two per space. Past the ceiling every tick is refused, for as long "+
			"as the person holds that many spaces — and from the interface it "+
			"looks like messages have simply stopped.",
			spaces, err, relay.DefaultLimits().CollectMaxHints)
	}
}
