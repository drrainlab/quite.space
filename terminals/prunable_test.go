package terminals

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/objects"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// SP-1 projection classification: an object outlives MaxAge (structural —
// referenced by current state), a human observation ages like a message.
func TestSP1PrunableClassification(t *testing.T) {
	structural := []string{
		objects.SchemaCreated, objects.SchemaRevised,
		objects.SchemaArchived, objects.SchemaRestored,
		schemas.CardCreated, schemas.CardUpdated,
	}
	for _, s := range structural {
		if prunable(s) {
			t.Errorf("%s must be structural (survives MaxAge)", s)
		}
	}
	if !prunable(schemas.ObservationNoted) {
		t.Error("observation.noted.v1 must age out like a message")
	}
}
