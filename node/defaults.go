// What a build ARRIVES KNOWING, and the line it will not cross.
//
// A fresh node knows nobody and nothing. That is correct — it is the whole
// premise — and it is also an empty room, which is a poor first minute for
// somebody who was handed an apk by a friend. A build may therefore ship one
// suggestion: a directory it knows the address of.
//
// A SUGGESTION IS NOT A SUBSCRIPTION, and this file exists to keep those
// apart. Nothing here is opened, fetched, dialled or joined. The node holds
// a string and offers it; a person presses, or does not, and until they do
// the relay never hears from this device on account of it. An app that
// quietly opened a space nobody asked for would be telling on its owner the
// first time it ran — to the relay, which learns the device is alive, and to
// the space, which learns somebody arrived.
//
// The values are VARIABLES rather than constants so a build can set them
// with -ldflags without a source change:
//
//	go build -ldflags "-X github.com/drrainlab/quiet_places/node.DefaultDirectoryLink=<link>"
//
// Empty is still meaningful and still supported: a build with no link has no
// official home, and Discover shows only what its owner added.
package node

import "net/http"

var (
	// DefaultDirectoryLink is a share link to the official directory: the one
	// Discover opens on, before anybody has added anything of their own.
	//
	// A VALUE HERE IS A PRODUCT DECISION, not a convenience. This address is
	// what a person's first press of Discover reaches, so it belongs in the
	// source where it can be read and reviewed, and it is overridable with
	// -ldflags for a build that should point somewhere else — or nowhere.
	//
	// The official quite.space directory, on the catalog-1 relay and kept
	// reachable by the mirror running beside it — so a first press of
	// Discover works even while the owner's laptop is shut.
	DefaultDirectoryLink = "MTk1LjYzLjE2MC4yMzc6NzQxMQpzcGFjZTpmZTA0NjNkYzE4MjVkNDNlODM0MTdiNDRiMmQzOWVjNWQ1ZjBjM2EzNzQyYjdmYTM4NGMwODE0ODcxMTEzMGVj"
	// DefaultDirectoryTitle names it for a person, before they open it.
	DefaultDirectoryTitle = "quite.space"
	// DefaultDirectoryNote says in one sentence what is behind the link, so
	// the choice to press is made with something in hand.
	DefaultDirectoryNote = "the places this build knows about"
)

// SuggestedDirectory is what this build would like to offer, and whether it
// has anything to offer at all.
type SuggestedDirectory struct {
	Link  string `json:"link"`
	Title string `json:"title,omitempty"`
	Note  string `json:"note,omitempty"`
	// Held says this node already has it, in which case there is nothing to
	// suggest — the answer is computed here rather than in the interface so
	// a CLI and a phone agree about it.
	Held bool `json:"held"`
}

// SuggestedDirectoryFor reports the build's suggestion, already checked
// against what this node holds.
func (r *Runtime) SuggestedDirectoryFor() SuggestedDirectory {
	s := SuggestedDirectory{
		Link:  DefaultDirectoryLink,
		Title: DefaultDirectoryTitle,
		Note:  DefaultDirectoryNote,
	}
	if s.Link == "" {
		return s
	}
	_, target, _, err := ParsePublicLink(s.Link)
	if err != nil {
		// A build shipped with a link nobody can parse suggests nothing. It
		// is a packaging mistake, and the person holding the phone is not
		// the one who can fix it.
		return SuggestedDirectory{}
	}
	r.mu.Lock()
	_, s.Held = r.spaces[target]
	r.mu.Unlock()
	return s
}

// handleSuggestedDirectory answers what this build would offer. It performs
// no network call — the point of the whole file is that nothing happens
// until somebody presses.
func (a *APIServer) handleSuggestedDirectory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.rt.SuggestedDirectoryFor())
}
