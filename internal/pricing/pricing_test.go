package pricing

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestLongestPrefixWins(t *testing.T) {
	// "gpt-4o-mini-2024-07-18" must use gpt-4o-mini, not gpt-4o.
	p, ok := Default.Lookup("gpt-4o-mini-2024-07-18")
	if !ok || p != Default["gpt-4o-mini"] {
		t.Fatalf("got %+v %v", p, ok)
	}
}

func TestCost(t *testing.T) {
	c, ok := Default.Cost("claude-sonnet-4-5-20250929", 2_000_000, 500_000)
	if !ok || math.Abs(c-(6+7.5)) > 1e-9 {
		t.Fatalf("got %v %v", c, ok)
	}
	if _, ok := Default.Cost("mystery-model", 10, 10); ok {
		t.Fatal("unknown model should not be priced")
	}
}

func TestLoadOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.json")
	os.WriteFile(path, []byte(`{"claude-sonnet-4":{"input":2,"output":10},"in-house-model":{"input":0.1,"output":0.1}}`), 0o644)
	tbl, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if tbl["claude-sonnet-4"].Input != 2 || tbl["in-house-model"].Output != 0.1 || tbl["gpt-5"].Input != Default["gpt-5"].Input {
		t.Fatalf("merge wrong: %+v", tbl)
	}
}
