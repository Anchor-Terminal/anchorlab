// Package models reads models.txt, the files MODELS.md runs, as built into the harness.
package models

import (
	_ "embed"
	"strconv"
	"strings"
)

//go:embed models.txt
var list string

// Sizes is each model's size in bytes, the sum of its files as the Hub gave them (models.txt).
// A model that isn't in the list, such as one from a preset of our own, has no size here.
func Sizes() map[string]int64 {
	out := map[string]int64{}
	for _, line := range strings.Split(list, "\n") {
		f := strings.Fields(line)
		if len(f) < 6 || strings.HasPrefix(f[0], "#") {
			continue
		}
		n, err := strconv.ParseInt(f[4], 10, 64)
		if err != nil || n <= 0 {
			continue
		}
		out[f[0]] += n
	}
	return out
}
