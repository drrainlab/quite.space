package composition

import "github.com/drrainlab/quiet_places/protocol/schemas"

// Register the composition snapshot schemas with the global registry. The
// validators are structural only (the payload decodes as its declared kind);
// full contract validation (limits, allowlists, no-executable) lives in
// Validate* and runs at accept/serve time, not on opaque relay.
func init() {
	schemas.Register(SchemaAppearance, func(frame []byte) error {
		s, err := DecodeSnapshot(frame)
		if err != nil {
			return err
		}
		_, err = DecodeAppearance(s.Payload)
		return err
	})
	schemas.Register(SchemaComposition, func(frame []byte) error {
		s, err := DecodeSnapshot(frame)
		if err != nil {
			return err
		}
		_, err = DecodeComposition(s.Payload)
		return err
	})
	schemas.Register(SchemaBundleIndex, func(frame []byte) error {
		s, err := DecodeSnapshot(frame)
		if err != nil {
			return err
		}
		_, err = DecodeBundleIndex(s.Payload)
		return err
	})
}
