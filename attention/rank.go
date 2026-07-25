package attention

import (
	"math"
	"sort"
)

// The personal ranking head: online logistic regression over the sparse
// features. It updates on every piece of feedback, so "not mine" changes the
// next ranking immediately rather than after some retraining cycle. The
// whole model is a few thousand float32s — kilobytes, private, deletable.

const (
	learningRate = 0.15
	// l2Decay pulls unused weights back toward zero so a burst of feedback
	// on one topic does not permanently distort everything else.
	l2Decay = 1e-5
	// weightClamp keeps a single runaway feature from dominating the score.
	weightClamp = 6.0
)

// Model is the lexical ranking head. It is deliberately separate from any
// semantic calibration: replacing the encoder must not erase what the
// lexical layer learned about this person.
type Model struct {
	W        map[int]float32 `json:"w"`
	Bias     float32         `json:"bias"`
	Positive int             `json:"positive"`
	Negative int             `json:"negative"`
}

func NewModel() *Model {
	return &Model{W: map[int]float32{}, Bias: -0.5}
}

// Score returns the model's probability that this candidate deserves the
// person's attention.
func (m *Model) Score(f Features) float64 {
	if m == nil {
		return 0
	}
	z := float64(m.Bias)
	for i, v := range f {
		z += float64(m.W[i]) * v
	}
	return 1 / (1 + math.Exp(-z))
}

// Learn applies one feedback example. label is 1 for "useful" (including
// "you should have caught this") and 0 for "not mine".
//
// Policy commands — mute this space, digest only, never this topic — must
// NOT come through here: silencing a place is not a statement that its
// content is worthless, and treating it as one would poison the model.
func (m *Model) Learn(f Features, label float64) {
	if m == nil {
		return
	}
	if m.W == nil {
		m.W = map[int]float32{}
	}
	p := m.Score(f)
	err := label - p
	for i, v := range f {
		w := float64(m.W[i])
		w += learningRate * err * v
		w -= l2Decay * w
		m.W[i] = float32(clamp(w, -weightClamp, weightClamp))
	}
	m.Bias = float32(clamp(float64(m.Bias)+learningRate*err*0.1, -weightClamp, weightClamp))
	if label >= 0.5 {
		m.Positive++
	} else {
		m.Negative++
	}
}

// Trained reports whether the model has seen enough feedback to be worth
// listening to. Before that QuietRank stays on rules alone rather than
// pretending a cold model knows the person.
func (m *Model) Trained() bool {
	return m != nil && m.Positive+m.Negative >= 3
}

// Explain returns an HONEST reason for the model's contribution. Features
// are hashed into a fixed space, so collisions make "term X did it"
// unprovable — we report a learned pattern and mark it approximate rather
// than inventing a term. Exact reasons only ever come from the rule layer.
func (m *Model) Explain(f Features, score float64) []Reason {
	if !m.Trained() || score < 0.5 {
		return nil
	}
	// Report the structural contributors, which are NOT hashed and therefore
	// can be named truthfully.
	var named []string
	for idx, label := range map[int]string{
		fHasQuestion:     "question mark",
		fIsReply:         "a reply",
		fMixedScript:     "mixed languages",
		fFromKnownAuthor: "someone you know",
	} {
		if f[idx] != 0 && m.W[idx] > 0.05 {
			named = append(named, label)
		}
	}
	sort.Strings(named)
	r := Reason{Code: ReasonLearned, Exact: false}
	if len(named) > 0 {
		r.Detail = named[0]
	}
	return []Reason{r}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
