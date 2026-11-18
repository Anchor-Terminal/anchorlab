package models

import "testing"

// Every model in models.txt has a size, and a model in several files is the sum of them.
func TestSizes(t *testing.T) {
	s := Sizes()
	if s["qwen3.8-27b"] != 28595763840 {
		t.Errorf("qwen3.8-27b: %d", s["qwen3.8-27b"])
	}
	if s["qwen3.5-122b-a10b"] != 40007829376+34200410816 {
		t.Errorf("qwen3.5-122b-a10b, two files: %d", s["qwen3.5-122b-a10b"])
	}
	if len(s) < 20 {
		t.Errorf("only %d models", len(s))
	}
	for m, n := range s {
		if n < 1<<20 {
			t.Errorf("%s: %d bytes", m, n)
		}
	}
}
