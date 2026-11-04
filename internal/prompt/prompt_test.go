package prompt

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestLine verifies a line read returns the trimmed value and echoes the label
// without any color when the output is not a terminal.
func TestLine(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	p := New(strings.NewReader("  hello world  \n"), &out, &out)
	got, err := p.Line("Name")
	if err != nil {
		t.Fatalf("Line: %v", err)
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
	if !strings.Contains(out.String(), "Name") {
		t.Errorf("prompt label missing from %q", out.String())
	}
	if strings.Contains(out.String(), "\033[") {
		t.Errorf("color leaked into non-terminal output: %q", out.String())
	}
	if p.Interactive() {
		t.Error("Interactive() = true for a non-terminal input")
	}
}

// TestConfirm verifies yes/no parsing and the default on an empty answer.
func TestConfirm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Def  bool
		Want bool
	}{ // Test 0-5: explicit yes/no, defaults, and an unrecognized answer.
		{In: "y\n", Def: false, Want: true},
		{In: "yes\n", Def: false, Want: true},
		{In: "n\n", Def: true, Want: false},
		{In: "\n", Def: true, Want: true},
		{In: "\n", Def: false, Want: false},
		{In: "maybe\n", Def: true, Want: true},
	}
	for testNum, test := range tests {
		var out bytes.Buffer
		p := New(strings.NewReader(test.In), &out, &out)
		got, err := p.Confirm("Proceed?", test.Def)
		if err != nil {
			t.Fatalf("test %d: Confirm: %v", testNum, err)
		}
		if got != test.Want {
			t.Errorf("test %d: got %v, want %v", testNum, got, test.Want)
		}
	}
}

// TestChoose verifies the menu re-asks on an out-of-range answer, returns the
// zero-based index on a valid one, and aborts on "q".
func TestChoose(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	p := New(strings.NewReader("9\n2\n"), &out, &out)
	idx, err := p.Choose("Source", []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if idx != 1 {
		t.Errorf("idx = %d, want 1", idx)
	}

	var out2 bytes.Buffer
	q := New(strings.NewReader("q\n"), &out2, &out2)
	if _, err := q.Choose("Source", []string{"a", "b"}); !errors.Is(err, ErrAborted) {
		t.Errorf("quit: err = %v, want ErrAborted", err)
	}
}

// TestSecretFallback verifies that without a terminal the secret read falls back
// to a line read and never echoes the value.
func TestSecretFallback(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	p := New(strings.NewReader("hunter2\n"), &out, &out)
	got, err := p.Secret("Token")
	if err != nil {
		t.Fatalf("Secret: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("got %q, want %q", got, "hunter2")
	}
	if strings.Contains(out.String(), "hunter2") {
		t.Errorf("secret echoed into output: %q", out.String())
	}
}

// TestReadEOF verifies a closed input with no data reads as io.EOF, so the
// caller can treat it as an abort.
func TestReadEOF(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	p := New(strings.NewReader(""), &out, &out)
	if _, err := p.Line("Name"); !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want io.EOF", err)
	}
}
