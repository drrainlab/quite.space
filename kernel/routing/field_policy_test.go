package routing

import "testing"

// SP-3 admits the field families — and closes a latent SP-1 gap: the
// object. family shipped without radio admission, so an object revision
// could never cross a bridge (custody was silently released).
func TestFieldFamiliesRadioAdmitted(t *testing.T) {
	admitted := []string{
		"object.created.v1", // the SP-1 gap, now closed
		"object.revised.v1",
		"object.attached.v1",
		"marker.placed.v1",
		"checkin.sent.v1",
		"observation.position.v1", // rode the observation. family all along
	}
	for _, s := range admitted {
		if !RadioAdmits(s, 200) {
			t.Errorf("%s refused radio admission", s)
		}
	}
	// Unknown families stay refused; size still gates everything.
	if RadioAdmits("mystery.event.v1", 200) {
		t.Error("unknown family admitted")
	}
	if RadioAdmits("checkin.sent.v1", RadioDecodeCap+1) {
		t.Error("oversize admitted")
	}
}
