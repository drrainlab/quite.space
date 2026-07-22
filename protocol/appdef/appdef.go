// Package appdef defines the Application half of ADR-014: declarative app
// DEFINITIONS (logic + requested capabilities, immutable per revision) and
// app INSTANCES (a pinned definition revision + props + GRANTED capabilities
// + an isolated state partition). No code travels: inputs are schema-scoped
// event queries with an instance-bound equality filter, state is folded by a
// fixed reducer vocabulary, actions emit registered schemas through the node.
//
// The authoring form is strict JSON (the composer/AI surface); on the wire a
// definition/instance rides in a canonical-CBOR payload {1: fallback text,
// 2: json bytes} so relays stay schema-opaque and the fallback convention
// holds. App STATE events are canonical CBOR partitioned by instance_id.
package appdef

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// Schema ids.
const (
	SchemaDefinition = "app.definition.v1"
	SchemaInstance   = "app.instance.v1"
	// Domain state schemas (APP-1 blocks emit THESE, not a generic app.state):
	SchemaPollVote     = "poll.vote.v1"
	SchemaFormResponse = "form.response.v1"
)

// Bounds.
const (
	MaxName       = 32
	MaxCollection = 8
	MaxViewBlocks = 64
	MaxViewDepth  = 4
	MaxProps      = 16
	MaxPropValue  = 200
	MaxDataBytes  = 4096
	MaxJSONBytes  = 32 * 1024
)

var allowedReducers = map[string]bool{"latest": true, "ring": true, "count": true, "list": true, "sum": true}
var allowedViewTypes = map[string]bool{
	"heading": true, "text": true, "metric-row": true, "event-list": true,
	"action-button": true, "chart": true, "stack": true,
}
var allowedCapKinds = map[string]bool{"read": true, "append": true}

// ---- JSON models (the authoring form) ----

type Where struct {
	Field string `json:"field"`
	Value string `json:"value"` // $instance.id | $instance.props.<name>
}

type Input struct {
	Schema string `json:"schema"`
	Where  *Where `json:"where,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type Reducer struct {
	Kind  string `json:"reducer"` // latest | ring | count | list | sum
	Input string `json:"input"`
	Size  int    `json:"size,omitempty"`
	Field string `json:"field,omitempty"`
}

type ViewBlock struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Props    map[string]string `json:"props,omitempty"` // values may be $state refs
	Children []ViewBlock       `json:"children,omitempty"`
}

type Action struct {
	Emit   string   `json:"emit"`
	Fields []string `json:"fields,omitempty"` // request fields copied into data
}

type Capability struct {
	Kind    string   `json:"kind"` // read | append
	Schemas []string `json:"schemas"`
}

// Definition is the declarative program (immutable per revision event).
type Definition struct {
	AppID     string             `json:"app_id"`
	Name      string             `json:"name"`
	Inputs    map[string]Input   `json:"inputs,omitempty"`
	State     map[string]Reducer `json:"state,omitempty"`
	View      []ViewBlock        `json:"view,omitempty"`
	Actions   map[string]Action  `json:"actions,omitempty"`
	Requested []Capability       `json:"requested_capabilities,omitempty"`
	Fallback  string             `json:"fallback,omitempty"`
}

// Instance places one definition revision with its own state partition.
type Instance struct {
	InstanceID string `json:"instance_id"` // hex 16B
	AppID      string `json:"app_id"`
	// RevisionEventID pins the exact definition revision ("" = built-in
	// template pinned by the binary; correction 2 — only pinned exists).
	RevisionEventID string            `json:"revision_event_id,omitempty"`
	DocumentID      string            `json:"document_id,omitempty"`
	BlockID         string            `json:"block_id,omitempty"`
	Props           map[string]string `json:"props,omitempty"`
	Granted         []Capability      `json:"granted_capabilities,omitempty"`
}

// ---- validation ----

func checkName(s, what string) error {
	if s == "" || len(s) > MaxName {
		return fmt.Errorf("appdef: %s missing or too long", what)
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return fmt.Errorf("appdef: %s has illegal characters", what)
		}
	}
	return nil
}

// CapSet is a capability lookup: kind → schema set.
type CapSet map[string]map[string]bool

func NewCapSet(caps []Capability) CapSet {
	out := CapSet{}
	for _, c := range caps {
		m, ok := out[c.Kind]
		if !ok {
			m = map[string]bool{}
			out[c.Kind] = m
		}
		for _, s := range c.Schemas {
			m[s] = true
		}
	}
	return out
}

func (cs CapSet) Has(kind, schema string) bool { return cs[kind][schema] }

// Intersect returns requested ∩ granted ∩ policy for one kind+schema.
func Allowed(kind, schema string, requested, granted, policy CapSet) bool {
	return requested.Has(kind, schema) && granted.Has(kind, schema) && policy.Has(kind, schema)
}

// ValidateDefinition is the authoring gate for a definition.
func ValidateDefinition(d *Definition) error {
	if err := checkName(d.AppID, "app id"); err != nil {
		return err
	}
	if d.Name == "" || len(d.Name) > 80 {
		return errors.New("appdef: name required and bounded")
	}
	if len(d.Inputs) > MaxCollection || len(d.State) > MaxCollection || len(d.Actions) > MaxCollection {
		return errors.New("appdef: too many inputs/state/actions")
	}
	req := NewCapSet(d.Requested)
	for _, c := range d.Requested {
		if !allowedCapKinds[c.Kind] {
			return fmt.Errorf("appdef: capability kind %q unknown", c.Kind)
		}
		for _, s := range c.Schemas {
			if !schemas.ValidID(s) {
				return fmt.Errorf("appdef: capability schema %q malformed", s)
			}
		}
	}
	for name, in := range d.Inputs {
		if err := checkName(name, "input name"); err != nil {
			return err
		}
		if !schemas.ValidID(in.Schema) {
			return fmt.Errorf("appdef: input %s schema malformed", name)
		}
		// Reading a schema must be REQUESTED (grants intersect later).
		if !req.Has("read", in.Schema) {
			return fmt.Errorf("appdef: input %s reads %s without requesting it", name, in.Schema)
		}
		if in.Where != nil {
			if in.Where.Field != "instance_id" && in.Where.Field != "sensor_id" {
				return fmt.Errorf("appdef: where field %q is not indexable", in.Where.Field)
			}
			if in.Where.Value != "$instance.id" && !isPropRef(in.Where.Value) {
				return fmt.Errorf("appdef: where value must be $instance.id or $instance.props.<name>")
			}
		}
		if in.Limit < 0 || in.Limit > 1000 {
			return errors.New("appdef: input limit out of range")
		}
	}
	for name, r := range d.State {
		if err := checkName(name, "state name"); err != nil {
			return err
		}
		if !allowedReducers[r.Kind] {
			return fmt.Errorf("appdef: reducer %q unknown", r.Kind)
		}
		if _, ok := d.Inputs[r.Input]; !ok {
			return fmt.Errorf("appdef: state %s folds unknown input %q", name, r.Input)
		}
		if r.Kind == "ring" && (r.Size < 1 || r.Size > 1000) {
			return errors.New("appdef: ring size out of range")
		}
	}
	for name, a := range d.Actions {
		if err := checkName(name, "action name"); err != nil {
			return err
		}
		if !schemas.ValidID(a.Emit) {
			return fmt.Errorf("appdef: action %s emits malformed schema", name)
		}
		// Emitting a schema must be REQUESTED.
		if !req.Has("append", a.Emit) {
			return fmt.Errorf("appdef: action %s emits %s without requesting it", name, a.Emit)
		}
		if len(a.Fields) > MaxProps {
			return errors.New("appdef: too many action fields")
		}
	}
	total := 0
	for i := range d.View {
		if err := walkView(&d.View[i], 1, &total); err != nil {
			return err
		}
	}
	if total > MaxViewBlocks {
		return errors.New("appdef: view too large")
	}
	return nil
}

func isPropRef(v string) bool {
	const p = "$instance.props."
	return len(v) > len(p) && v[:len(p)] == p
}

func walkView(b *ViewBlock, depth int, total *int) error {
	if depth > MaxViewDepth {
		return errors.New("appdef: view too deep")
	}
	*total++
	if !allowedViewTypes[b.Type] {
		return fmt.Errorf("appdef: view type %q not allowed", b.Type)
	}
	if len(b.Props) > MaxProps {
		return errors.New("appdef: too many view props")
	}
	for k, v := range b.Props {
		if len(k) > MaxName || len(v) > MaxPropValue {
			return errors.New("appdef: view prop too long")
		}
	}
	for i := range b.Children {
		if err := walkView(&b.Children[i], depth+1, total); err != nil {
			return err
		}
	}
	return nil
}

// ValidateInstance checks an instance against its definition and a node
// policy: granted must be well-formed; effective use is checked at runtime.
func ValidateInstance(inst *Instance, def *Definition) error {
	b, err := hex.DecodeString(inst.InstanceID)
	if err != nil || len(b) != 16 {
		return errors.New("appdef: instance id must be 16 bytes hex")
	}
	if inst.AppID != def.AppID {
		return errors.New("appdef: instance/definition app id mismatch")
	}
	if inst.RevisionEventID != "" {
		rb, err := hex.DecodeString(inst.RevisionEventID)
		if err != nil || len(rb) != 32 {
			return errors.New("appdef: pinned revision must be 32 bytes hex")
		}
	}
	if len(inst.Props) > MaxProps {
		return errors.New("appdef: too many instance props")
	}
	for k, v := range inst.Props {
		if len(k) > MaxName || len(v) > MaxPropValue {
			return errors.New("appdef: instance prop too long")
		}
	}
	for _, c := range inst.Granted {
		if !allowedCapKinds[c.Kind] {
			return fmt.Errorf("appdef: granted kind %q unknown", c.Kind)
		}
		for _, s := range c.Schemas {
			if !schemas.ValidID(s) {
				return fmt.Errorf("appdef: granted schema %q malformed", s)
			}
		}
	}
	return nil
}

// ---- wire payloads ----

// docPayload wraps strict JSON in the fallback-first CBOR convention.
func docPayload(fallback string, jsonBytes []byte) []byte {
	buf := codec.AppendMap(nil, 2)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, fallback)
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendBytes(buf, jsonBytes)
	return buf
}

func decodeDocPayload(payload []byte) (fallback string, jsonBytes []byte, err error) {
	d := codec.NewDecoder(payload)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return "", nil, err
	}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return "", nil, er
		}
		if !ok {
			break
		}
		switch k {
		case 1:
			fallback, er = d.ReadText()
		case 2:
			jsonBytes, er = d.ReadBytes()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return "", nil, er
		}
	}
	if err := d.Done(); err != nil {
		return "", nil, err
	}
	if len(jsonBytes) == 0 || len(jsonBytes) > MaxJSONBytes {
		return "", nil, errors.New("appdef: payload body missing or too large")
	}
	return fallback, jsonBytes, nil
}

func strictUnmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

// EncodeDefinition / DecodeDefinition.
func EncodeDefinition(d *Definition) ([]byte, error) {
	if err := ValidateDefinition(d); err != nil {
		return nil, err
	}
	j, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return docPayload(d.Name, j), nil
}

func DecodeDefinition(payload []byte) (*Definition, error) {
	_, j, err := decodeDocPayload(payload)
	if err != nil {
		return nil, err
	}
	var d Definition
	if err := strictUnmarshal(j, &d); err != nil {
		return nil, errors.New("appdef: definition JSON malformed")
	}
	if err := ValidateDefinition(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

// EncodeInstance / DecodeInstance. The instance validates against its
// definition at CREATE time (node-side); wire decode is structural.
func EncodeInstance(inst *Instance) ([]byte, error) {
	j, err := json.Marshal(inst)
	if err != nil {
		return nil, err
	}
	return docPayload(inst.AppID, j), nil
}

func DecodeInstance(payload []byte) (*Instance, error) {
	_, j, err := decodeDocPayload(payload)
	if err != nil {
		return nil, err
	}
	var inst Instance
	if err := strictUnmarshal(j, &inst); err != nil {
		return nil, errors.New("appdef: instance JSON malformed")
	}
	if b, err := hex.DecodeString(inst.InstanceID); err != nil || len(b) != 16 {
		return nil, errors.New("appdef: instance id must be 16 bytes hex")
	}
	return &inst, nil
}

// ---- app STATE events (partitioned by instance_id; canonical CBOR) ----

// StateEvent is the shared shape of domain state schemas (poll votes, form
// responses): key 1 fallback, key 2 instance_id, key 3 bounded JSON data.
type StateEvent struct {
	Fallback   string
	InstanceID [16]byte
	Data       []byte // strict JSON, bounded
}

func (s *StateEvent) Encode() []byte {
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, s.Fallback)
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendBytes(buf, s.InstanceID[:])
	buf = codec.AppendUint(buf, 3)
	buf = codec.AppendBytes(buf, s.Data)
	return buf
}

func DecodeStateEvent(payload []byte) (*StateEvent, error) {
	d := codec.NewDecoder(payload)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	s := &StateEvent{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case 1:
			s.Fallback, er = d.ReadText()
		case 2:
			var b []byte
			b, er = d.ReadBytes()
			if er == nil {
				if len(b) != 16 {
					return nil, errors.New("appdef: state event instance id must be 16 bytes")
				}
				copy(s.InstanceID[:], b)
			}
		case 3:
			s.Data, er = d.ReadBytes()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if s.InstanceID == ([16]byte{}) {
		return nil, errors.New("appdef: state event needs an instance id")
	}
	if len(s.Data) > MaxDataBytes {
		return nil, errors.New("appdef: state event data too large")
	}
	if len(s.Data) > 0 && !json.Valid(s.Data) {
		return nil, errors.New("appdef: state event data is not JSON")
	}
	return s, nil
}

func init() {
	schemas.Register(SchemaDefinition, func(p []byte) error { _, err := DecodeDefinition(p); return err })
	schemas.Register(SchemaInstance, func(p []byte) error { _, err := DecodeInstance(p); return err })
	state := func(p []byte) error { _, err := DecodeStateEvent(p); return err }
	schemas.Register(SchemaPollVote, state)
	schemas.Register(SchemaFormResponse, state)
}
