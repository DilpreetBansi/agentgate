package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStatsAggregates(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	day := time.Date(2026, 10, 14, 15, 0, 0, 0, time.UTC)
	rows := []Record{
		{Time: day, User: "a", Tool: "claude-code", Model: "claude-sonnet-4-5", InputTok: 100, OutputTok: 10, CostUSD: 1.0, Priced: true, LatencyMS: 1000, Status: 200, Findings: "[]"},
		{Time: day.Add(time.Hour), User: "a", Tool: "claude-code", Model: "claude-sonnet-4-5", InputTok: 100, OutputTok: 10, CostUSD: 2.0, Priced: true, LatencyMS: 3000, Status: 200, Findings: "[]"},
		{Time: day.Add(24 * time.Hour), User: "b", Tool: "codex", Model: "gpt-5", CostUSD: 0, LatencyMS: 500, Status: 529, Findings: "[]"},
		{Time: day.Add(25 * time.Hour), User: "b", Tool: "codex", Model: "gpt-5", Status: 403, Blocked: true, LatencyMS: 1, Findings: `[{"kind":"aws_access_key","count":2}]`},
	}
	for _, r := range rows {
		if err := s.Insert(r); err != nil {
			t.Fatal(err)
		}
	}
	st, err := s.Stats(day.Add(-time.Hour), day.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if st.Calls != 4 || st.Users != 2 || st.CostUSD != 3 || st.Blocked != 1 {
		t.Errorf("totals wrong: %+v", st)
	}
	if st.ErrorRate != 0.25 { // the 529; a blocked request is not an error
		t.Errorf("error rate = %v", st.ErrorRate)
	}
	if st.ByUser[0].Key != "a" || st.ByUser[0].CostUSD != 3 {
		t.Errorf("by user = %+v", st.ByUser)
	}
	if len(st.Daily) != 2 || st.Daily[0].Calls != 2 || st.Daily[1].Users != 1 {
		t.Errorf("daily = %+v", st.Daily)
	}
	if len(st.SecretKinds) != 1 || st.SecretKinds[0].Calls != 2 {
		t.Errorf("secret kinds = %+v", st.SecretKinds)
	}
	if st.P50MS != 1000 || st.P95MS != 3000 {
		t.Errorf("latency p50=%d p95=%d", st.P50MS, st.P95MS)
	}
	if len(st.RecentEvents) != 2 || !st.RecentEvents[0].Blocked {
		t.Errorf("events = %+v", st.RecentEvents)
	}
}
