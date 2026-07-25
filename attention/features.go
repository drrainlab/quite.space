package attention

import (
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// Feature extraction for the lexical layer.
//
// Character n-grams are the multilingual trick here: they do not know where
// Russian stops and English starts, they survive typos and transliteration,
// and a message that mixes both ("Реле готово, but we still need someone to
// test the mesh fallback") produces useful features either way. No
// dictionary, no tokenizer per language, no model.

const (
	// featureBuckets is the hashed feature space. Collisions are real and
	// deliberate — they are the price of a fixed-size, kilobyte profile, and
	// they are exactly why explanations from this layer are labelled
	// approximate (D4).
	featureBuckets = 1 << 14

	ngramMin = 3
	ngramMax = 5

	// maxFeatureText bounds work per candidate; long posts do not get to
	// spend unbounded CPU on the local device.
	maxFeatureText = 2000
)

// Structural feature slots live at the TOP of the space, above the hashed
// n-grams, so they can never be collided into by text.
const (
	fStructBase  = featureBuckets
	fHasQuestion = fStructBase + iota
	fHasExclaim
	fIsReply
	fShort
	fLong
	fCyrillic
	fLatin
	fMixedScript
	fFromKnownAuthor
	fSpacePriority
	featureSpace // total width
)

// Features is a sparse feature vector: index → value.
type Features map[int]float64

// Extract turns a candidate into sparse features. authorKnown and
// spacePriority are caller-supplied context (a known author and a space you
// care about legitimately shift attention).
func Extract(c Candidate, authorKnown bool, spacePriority float64) Features {
	f := Features{}
	text := c.Text
	if len(text) > maxFeatureText {
		text = text[:maxFeatureText]
	}
	lower := strings.ToLower(text)

	// Hashed character n-grams over a normalized string. Word boundaries are
	// marked with spaces so "mesh " and " mesh" differ from "meshed".
	norm := normalizeForNgrams(lower)
	runes := []rune(norm)
	for n := ngramMin; n <= ngramMax; n++ {
		for i := 0; i+n <= len(runes); i++ {
			h := hashBucket(string(runes[i : i+n]))
			f[h] += 1
		}
	}
	// L2-normalize the text part so long messages do not simply outweigh
	// short ones — a one-line question is often the important thing.
	var sum float64
	for _, v := range f {
		sum += v * v
	}
	if sum > 0 {
		inv := 1 / math.Sqrt(sum)
		for k := range f {
			f[k] *= inv
		}
	}

	if strings.Contains(text, "?") {
		f[fHasQuestion] = 1
	}
	if strings.Contains(text, "!") {
		f[fHasExclaim] = 1
	}
	if c.ReplyTo != nil {
		f[fIsReply] = 1
	}
	switch n := len([]rune(text)); {
	case n <= 40:
		f[fShort] = 1
	case n >= 400:
		f[fLong] = 1
	}
	cyr, lat := scriptMix(text)
	if cyr > 0 {
		f[fCyrillic] = 1
	}
	if lat > 0 {
		f[fLatin] = 1
	}
	if cyr > 0 && lat > 0 {
		f[fMixedScript] = 1 // code-switching is a signal in its own right
	}
	if authorKnown {
		f[fFromKnownAuthor] = 1
	}
	if spacePriority != 0 {
		f[fSpacePriority] = spacePriority
	}
	return f
}

// normalizeForNgrams collapses punctuation and whitespace so n-grams line up
// across formatting differences, keeping single spaces as word boundaries.
func normalizeForNgrams(lower string) string {
	var b strings.Builder
	b.Grow(len(lower) + 2)
	b.WriteByte(' ')
	prevSpace := true
	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevSpace = false
			continue
		}
		if !prevSpace {
			b.WriteByte(' ')
			prevSpace = true
		}
	}
	if !prevSpace {
		b.WriteByte(' ')
	}
	return b.String()
}

func hashBucket(s string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return int(h.Sum32() % featureBuckets)
}

func scriptMix(s string) (cyrillic, latin int) {
	for _, r := range s {
		switch {
		case r >= 'а' && r <= 'я', r >= 'А' && r <= 'Я', r == 'ё', r == 'Ё':
			cyrillic++
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			latin++
		}
	}
	return
}
