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

//go:embed presets.ini
var presets string

// presetKeys are presets.ini's sampling keys (llama-server's flags) and the names llama-server's
// /props gives the same settings.
var presetKeys = map[string]string{"temp": "temperature", "top-p": "top_p", "top-k": "top_k", "min-p": "min_p", "repeat-penalty": "repeat_penalty", "presence-penalty": "presence_penalty"}

// Sampling is the sampling presets.ini sets for a model, by /props' names: what the server should
// use when a request sets none. A setting the preset leaves out is the server's own default and
// isn't in the map. False when the model has no section.
func Sampling(model string) (map[string]float64, bool) {
	out, in, found := map[string]float64{}, false, false
	for _, line := range strings.Split(presets, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "["):
			in = line == "["+model+"]"
			found = found || in
		case in && line != "" && !strings.HasPrefix(line, ";"):
			k, v, ok := strings.Cut(line, "=")
			if name := presetKeys[strings.TrimSpace(k)]; ok && name != "" {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					out[name] = f
				}
			}
		}
	}
	return out, found
}

// File is the SHA-256 of a model's first file in models.txt, what pins the build that ran: "" for
// a model that isn't in the list.
func File(model string) string {
	for _, line := range strings.Split(list, "\n") {
		if f := strings.Fields(line); len(f) >= 6 && f[0] == model {
			return f[5]
		}
	}
	return ""
}
