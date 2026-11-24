package models

import (
	"os"
	"strings"
	"testing"
)

// Every model in models.txt has a size, and a model in several files is the sum of them.
func TestSizes(t *testing.T) {
	s := Sizes()
	if s["qwen3.8-27b"] != 28595763840 {
		t.Errorf("qwen3.8-27b: %d", s["qwen3.8-27b"])
	}
	if s["qwen3.5-122b-a10b"] != 40007829376+34200410816 {
		t.Errorf("qwen3.5-122b-a10b, two files: %d", s["qwen3.5-122b-a10b"])
	}
	// a variant counts every file it loads, the one it shares with its model too
	if s["qwen3.8-27b+mtp"] != 28595763840+3164006976 || s["qwen3.8-27b+t0.6"] != s["qwen3.8-27b"] {
		t.Errorf("variants: mtp %d, t0.6 %d", s["qwen3.8-27b+mtp"], s["qwen3.8-27b+t0.6"])
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

// A preset's name is what llama-server serves the model as, so none has a colon: llama-server reads
// the part after one as a quant tag and rewrites it (qwen3.8-27b:t0.6 came out as qwen3.8-27b:6).
// Every model in models.txt has a preset of the same name.
func TestPresetNames(t *testing.T) {
	b, err := os.ReadFile("presets.ini")
	if err != nil {
		t.Fatal(err)
	}
	sections := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := line[1 : len(line)-1]
			if strings.Contains(name, ":") {
				t.Errorf("preset %q: a colon, which llama-server rewrites", name)
			}
			if sections[name] {
				t.Errorf("preset %q twice", name)
			}
			sections[name] = true
		}
	}
	for m := range Sizes() {
		if !sections[m] {
			t.Errorf("%s is in models.txt with no preset", m)
		}
	}
}

// A model's preset sampling reads as /props names it, and its file's hash is the first in the list.
func TestSamplingAndFile(t *testing.T) {
	s, ok := Sampling("muse-glimmer-30b")
	if !ok || s["temperature"] != 1.0 || s["top_p"] != 0.95 || s["top_k"] != 64 || len(s) != 3 {
		t.Errorf("muse-glimmer-30b: %v %v", s, ok)
	}
	if s, ok := Sampling("lfm3-1.7b"); !ok || s["repeat_penalty"] != 1.1 || s["temperature"] != 0.1 {
		t.Errorf("lfm3-1.7b: %v", s)
	}
	if _, ok := Sampling("no-such-model"); ok {
		t.Error("a model with no section")
	}
	if f := File("qwen3.5-122b-a10b"); f != "690a72066b02b108ac46e196f5359beb20214da31b1d647549e73163b2106658" {
		t.Errorf("file: %s", f)
	}
}
