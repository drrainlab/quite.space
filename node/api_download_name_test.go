package node

import "testing"

// A saved asset keeps the name the block gave it, and always ends in an
// extension the media type vouches for: a phone opens files by extension,
// and "asset-<hash>.bin" opened nothing.
func TestASavedAssetKeepsItsNameAndGetsAnExtension(t *testing.T) {
	aid := "0123456789abcdef0123456789abcdef"
	cases := []struct{ name, ct, want string }{
		{"отчёт.pdf", "application/pdf", "отчёт.pdf"},
		{"отчёт", "application/pdf", "отчёт.pdf"},
		{"", "image/jpeg", "asset-0123456789ab.jpg"},
		{"a very wide test image", "image/png", "a very wide test image.png"},
		{"../../etc/passwd", "text/plain", ".._.._etc_passwd"},
		{"notes", "application/octet-stream", "notes.bin"},
		{"clip", "video/quicktime", "clip.mov"},
	}
	for _, c := range cases {
		if got := downloadFilename(c.name, aid, c.ct); got != c.want {
			t.Errorf("downloadFilename(%q, %q) = %q, want %q", c.name, c.ct, got, c.want)
		}
	}
}
