package cmd

import "fmt"

// onceValue is a string flag that refuses a second value. The index command
// reads one source per run, and cobra's plain string flag takes the last
// repetition silently, which reads as "both sources indexed" while one was
// dropped. Failing the parse names the two values and the actual way to
// compose sources.
type onceValue struct {
	// target receives the flag's value.
	target *string
	// set reports whether a value has already arrived.
	set bool
}

// newOnceValue returns a onceValue writing into target, with a default.
func newOnceValue(target *string, def string) *onceValue {
	*target = def
	return &onceValue{target: target}
}

// String returns the current value.
func (o *onceValue) String() string { return *o.target }

// Set stores the first value and rejects any second one.
func (o *onceValue) Set(s string) error {
	if o.set {
		return fmt.Errorf("named twice (%q and %q): index one source per run, and chain runs with --merge", *o.target, s)
	}
	*o.target = s
	o.set = true
	return nil
}

// Type names the flag's value type in help output.
func (o *onceValue) Type() string { return "string" }
