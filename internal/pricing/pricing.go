// Package pricing turns token counts into dollars.
//
// Prices are USD per million tokens and are meant to be edited: providers
// change them often, and many companies have negotiated rates. Models are
// matched by longest prefix, so "claude-sonnet-4-5-20250929" uses the
// "claude-sonnet-4" entry.
package pricing

import (
	"encoding/json"
	"os"
	"strings"
)

// Price is the cost of one million tokens.
type Price struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

// Table maps a model-name prefix to its price.
type Table map[string]Price

// Default is a starting point. Override it with a JSON file (-prices flag).
var Default = Table{
	"claude-opus-4":    {Input: 15, Output: 75},
	"claude-sonnet-4":  {Input: 3, Output: 15},
	"claude-haiku-4":   {Input: 1, Output: 5},
	"claude-3-5-haiku": {Input: 0.8, Output: 4},
	"gpt-5":            {Input: 1.25, Output: 10},
	"gpt-5-mini":       {Input: 0.25, Output: 2},
	"gpt-4.1":          {Input: 2, Output: 8},
	"gpt-4.1-mini":     {Input: 0.4, Output: 1.6},
	"gpt-4o":           {Input: 2.5, Output: 10},
	"gpt-4o-mini":      {Input: 0.15, Output: 0.6},
	"o3":               {Input: 2, Output: 8},
	"o4-mini":          {Input: 1.1, Output: 4.4},
}

// Load reads a JSON price table from path and merges it over Default.
func Load(path string) (Table, error) {
	t := Table{}
	for k, v := range Default {
		t[k] = v
	}
	if path == "" {
		return t, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var override Table
	if err := json.Unmarshal(b, &override); err != nil {
		return nil, err
	}
	for k, v := range override {
		t[k] = v
	}
	return t, nil
}

// Lookup finds the price for model by longest matching prefix.
func (t Table) Lookup(model string) (Price, bool) {
	best, bestLen := Price{}, -1
	for prefix, p := range t {
		if strings.HasPrefix(model, prefix) && len(prefix) > bestLen {
			best, bestLen = p, len(prefix)
		}
	}
	return best, bestLen >= 0
}

// Cost returns the dollar cost of a request. Unknown models cost 0 and
// report ok=false so the dashboard can flag them instead of guessing.
func (t Table) Cost(model string, inTok, outTok int64) (usd float64, ok bool) {
	p, ok := t.Lookup(model)
	if !ok {
		return 0, false
	}
	return (float64(inTok)*p.Input + float64(outTok)*p.Output) / 1e6, true
}
