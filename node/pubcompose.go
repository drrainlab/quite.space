// AI post composition (ADR-014 invariant 13): a prompt becomes a *validated
// draft document*, never a publication. The system prompt enumerates the
// bounded block grammar; the model returns JSON only; the result passes the
// same authoring validator as a hand-written post and lands in the composer
// for the user to edit and publish.
package node

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/publication"
)

// documentSystemPrompt pins the composable grammar. Asset-bearing blocks are
// excluded — a model cannot know this space's asset ids; the author attaches
// media by hand in the composer.
func documentSystemPrompt() string {
	return strings.Join([]string{
		"You draft a post for a quiet, personal publication space.",
		"Reply with a SINGLE JSON object and nothing else — no prose, no code fences.",
		"Shape:",
		`{"kind":"article|note|release|log","title":"…","summary":"…","tags":["…"],`,
		` "blocks":[{"id":"b1","type":"…","props":{…}}]}`,
		"Allowed block types and their props:",
		`  heading   {"text":"…"}`,
		`  text      {"text":"…"}            — a paragraph`,
		`  quote     {"text":"…","extra":"attribution"}`,
		`  callout   {"text":"…","extra":"info|warning"}`,
		`  code      {"text":"…","extra":"language"}`,
		`  link      {"text":"https://…","more":"link title"}`,
		`  separator {}`,
		`  section   {"text":"section title"} with "children":[…blocks…]`,
		`  credits   {"items":["role","name","role","name"]}`,
		"Rules: unique block ids; at most 40 blocks; do NOT use image, gallery,",
		"audio, video-link, file or app blocks (the author attaches media by",
		"hand); tags are short lowercase words; no other keys.",
	}, "\n")
}

// ProposeDocument generates a validated draft for a prompt. Never published.
func (r *Runtime) ProposeDocument(ctx context.Context, tid id.TerminalID, prompt string) (*publication.Document, error) {
	cfg := r.LLMConfig()
	if cfg.Provider == "" || cfg.Model == "" {
		return nil, errors.New("node: configure an AI provider in Settings first")
	}
	raw, err := r.llm().Generate(ctx, cfg, documentSystemPrompt(), prompt)
	if err != nil {
		return nil, err
	}
	i := strings.IndexByte(raw, '{')
	j := strings.LastIndexByte(raw, '}')
	if i < 0 || j < i {
		return nil, errors.New("node: the model did not return a JSON object")
	}
	var dj documentJSON
	if err := json.Unmarshal([]byte(raw[i:j+1]), &dj); err != nil {
		return nil, errors.New("node: could not parse the model's JSON")
	}
	dj.DocumentID = "" // a proposal always drafts a NEW document
	doc, err := documentFromJSON(dj)
	if err != nil {
		return nil, err
	}
	// The proposal must pass the SAME authoring gate as a manual post.
	if err := publication.Validate(doc, r.spaceAssetOK(tid)); err != nil {
		return nil, err
	}
	return doc, nil
}

func (a *APIServer) handleProposeDocument(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Prompt string `json:"prompt"`
	}](r)
	if err != nil || strings.TrimSpace(body.Prompt) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("prompt required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	doc, err := a.rt.ProposeDocument(ctx, tid, body.Prompt)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "document": documentToJSON(doc)})
}
