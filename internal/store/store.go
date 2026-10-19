// Package store keeps one row per proxied LLM call in SQLite and computes
// the aggregates the dashboard shows.
package store

import (
	"database/sql"
	"encoding/json"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

// Record is one LLM call as seen by the gateway.
type Record struct {
	Time      time.Time `json:"time"`
	User      string    `json:"user"`
	Node      string    `json:"node"`
	Tool      string    `json:"tool"` // e.g. claude-code, codex, from User-Agent
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	InputTok  int64     `json:"input_tokens"`
	OutputTok int64     `json:"output_tokens"`
	CostUSD   float64   `json:"cost_usd"`
	Priced    bool      `json:"priced"`
	LatencyMS int64     `json:"latency_ms"`
	Status    int       `json:"status"`
	Blocked   bool      `json:"blocked"`
	Redacted  bool      `json:"redacted"`
	Findings  string    `json:"findings"` // JSON []guard.Finding
}

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer keeps SQLite simple and safe
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS calls (
		id INTEGER PRIMARY KEY,
		ts INTEGER NOT NULL,
		user TEXT NOT NULL, node TEXT NOT NULL, tool TEXT NOT NULL,
		provider TEXT NOT NULL, model TEXT NOT NULL,
		input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL,
		cost_usd REAL NOT NULL, priced INTEGER NOT NULL,
		latency_ms INTEGER NOT NULL, status INTEGER NOT NULL,
		blocked INTEGER NOT NULL, redacted INTEGER NOT NULL, findings TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS calls_ts ON calls(ts);`)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Insert(r Record) error {
	_, err := s.db.Exec(`INSERT INTO calls (ts,user,node,tool,provider,model,input_tokens,output_tokens,cost_usd,priced,latency_ms,status,blocked,redacted,findings)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.Time.UnixMilli(), r.User, r.Node, r.Tool, r.Provider, r.Model, r.InputTok, r.OutputTok,
		r.CostUSD, b2i(r.Priced), r.LatencyMS, r.Status, b2i(r.Blocked), b2i(r.Redacted), r.Findings)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Group is a row in a breakdown table (per user, per model, per tool).
type Group struct {
	Key       string  `json:"key"`
	Calls     int64   `json:"calls"`
	InputTok  int64   `json:"input_tokens"`
	OutputTok int64   `json:"output_tokens"`
	CostUSD   float64 `json:"cost_usd"`
	Errors    int64   `json:"errors"`
	Blocked   int64   `json:"blocked"`
}

// Day is one point in the daily series.
type Day struct {
	Date    string  `json:"date"`
	Calls   int64   `json:"calls"`
	CostUSD float64 `json:"cost_usd"`
	Users   int64   `json:"users"`
}

// Stats is everything the dashboard needs in one payload.
type Stats struct {
	From         time.Time `json:"from"`
	To           time.Time `json:"to"`
	Calls        int64     `json:"calls"`
	Users        int64     `json:"users"`
	InputTok     int64     `json:"input_tokens"`
	OutputTok    int64     `json:"output_tokens"`
	CostUSD      float64   `json:"cost_usd"`
	ErrorRate    float64   `json:"error_rate"`
	P50MS        int64     `json:"p50_ms"`
	P95MS        int64     `json:"p95_ms"`
	Blocked      int64     `json:"blocked"`
	Redacted     int64     `json:"redacted"`
	Unpriced     int64     `json:"unpriced"`
	ByUser       []Group   `json:"by_user"`
	ByModel      []Group   `json:"by_model"`
	ByTool       []Group   `json:"by_tool"`
	Daily        []Day     `json:"daily"`
	SecretKinds  []Group   `json:"secret_kinds"`
	RecentEvents []Record  `json:"recent_events"` // blocked/redacted/errored calls
}

// Stats aggregates calls in [from, to).
func (s *Store) Stats(from, to time.Time) (*Stats, error) {
	rows, err := s.db.Query(`SELECT ts,user,node,tool,provider,model,input_tokens,output_tokens,cost_usd,priced,latency_ms,status,blocked,redacted,findings
		FROM calls WHERE ts >= ? AND ts < ? ORDER BY ts`, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	st := &Stats{From: from, To: to}
	users := map[string]*Group{}
	models := map[string]*Group{}
	tools := map[string]*Group{}
	kinds := map[string]*Group{}
	days := map[string]*Day{}
	dayUsers := map[string]map[string]bool{}
	var lat []int64
	var errs int64

	for rows.Next() {
		var r Record
		var ts int64
		var priced, blocked, redacted int
		if err := rows.Scan(&ts, &r.User, &r.Node, &r.Tool, &r.Provider, &r.Model, &r.InputTok, &r.OutputTok,
			&r.CostUSD, &priced, &r.LatencyMS, &r.Status, &blocked, &redacted, &r.Findings); err != nil {
			return nil, err
		}
		r.Time = time.UnixMilli(ts).UTC()
		r.Priced, r.Blocked, r.Redacted = priced == 1, blocked == 1, redacted == 1
		isErr := !r.Blocked && r.Status >= 400

		st.Calls++
		st.InputTok += r.InputTok
		st.OutputTok += r.OutputTok
		st.CostUSD += r.CostUSD
		if r.Blocked {
			st.Blocked++
		}
		if r.Redacted {
			st.Redacted++
		}
		if !r.Priced && !r.Blocked && r.Status < 400 {
			st.Unpriced++
		}
		if isErr {
			errs++
		}
		if !r.Blocked {
			lat = append(lat, r.LatencyMS)
		}
		for _, g := range []*Group{get(users, r.User), get(models, orUnknown(r.Model)), get(tools, r.Tool)} {
			g.Calls++
			g.InputTok += r.InputTok
			g.OutputTok += r.OutputTok
			g.CostUSD += r.CostUSD
			if isErr {
				g.Errors++
			}
			if r.Blocked {
				g.Blocked++
			}
		}
		var fs []struct {
			Kind  string `json:"kind"`
			Count int64  `json:"count"`
		}
		if r.Findings != "" && json.Unmarshal([]byte(r.Findings), &fs) == nil {
			for _, f := range fs {
				get(kinds, f.Kind).Calls += f.Count
			}
		}
		d := r.Time.Format("2006-01-02")
		if days[d] == nil {
			days[d] = &Day{Date: d}
			dayUsers[d] = map[string]bool{}
		}
		days[d].Calls++
		days[d].CostUSD += r.CostUSD
		dayUsers[d][r.User] = true
		if r.Blocked || r.Redacted || isErr {
			st.RecentEvents = append(st.RecentEvents, r)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	st.Users = int64(len(users))
	if st.Calls > 0 {
		st.ErrorRate = float64(errs) / float64(st.Calls)
	}
	st.P50MS, st.P95MS = percentile(lat, 0.50), percentile(lat, 0.95)
	st.ByUser, st.ByModel, st.ByTool = sorted(users), sorted(models), sorted(tools)
	st.SecretKinds = sorted(kinds)
	for d, v := range days {
		v.Users = int64(len(dayUsers[d]))
		st.Daily = append(st.Daily, *v)
	}
	sort.Slice(st.Daily, func(i, j int) bool { return st.Daily[i].Date < st.Daily[j].Date })
	// newest first, capped
	for i, j := 0, len(st.RecentEvents)-1; i < j; i, j = i+1, j-1 {
		st.RecentEvents[i], st.RecentEvents[j] = st.RecentEvents[j], st.RecentEvents[i]
	}
	if len(st.RecentEvents) > 25 {
		st.RecentEvents = st.RecentEvents[:25]
	}
	return st, nil
}

func get(m map[string]*Group, k string) *Group {
	if m[k] == nil {
		m[k] = &Group{Key: k}
	}
	return m[k]
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

func sorted(m map[string]*Group) []Group {
	out := make([]Group, 0, len(m))
	for _, g := range m {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CostUSD != out[j].CostUSD {
			return out[i].CostUSD > out[j].CostUSD
		}
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func percentile(xs []int64, p float64) int64 {
	if len(xs) == 0 {
		return 0
	}
	c := append([]int64(nil), xs...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	idx := int(p*float64(len(c)-1) + 0.5)
	return c[idx]
}
