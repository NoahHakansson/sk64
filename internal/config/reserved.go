package config

// ReservedKey is a key the config layer must never rebind. The typed YES word
// and the exact-name delete input are reserved by construction, not by key:
// armed gates capture input, so rebinds never apply inside them.
type ReservedKey struct {
	Key     string // Bubble Tea key name
	Meaning string // cited verbatim by config validation errors
}

// ReservedKeys returns keys whose safety semantics cannot be rebound.
func ReservedKeys() []ReservedKey {
	return []ReservedKey{
		{Key: "N", Meaning: "starts create flows (uppercase mutation initiator)"},
		{Key: "D", Meaning: "starts delete flows (uppercase mutation initiator)"},
		{Key: "R", Meaning: "starts restart flows (uppercase mutation initiator)"},
		{Key: "Y", Meaning: "advances mutating confirms (uppercase mutation initiator)"},
		{Key: "esc", Meaning: "always cancels; closes a gate with nothing dispatched"},
		{Key: "enter", Meaning: "accepts typed or selected input only; never an alias for Y"},
	}
}
