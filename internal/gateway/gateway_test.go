package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DilpreetBansi/agentgate/internal/guard"
	"github.com/DilpreetBansi/agentgate/internal/pricing"
	"github.com/DilpreetBansi/agentgate/internal/store"
)

type recorder struct {
	mu   sync.Mutex
	recs []store.Record
}

func (r *recorder) add(x store.Record) { r.mu.Lock(); r.recs = append(r.recs, x); r.mu.Unlock() }
func (r *recorder) last(t *testing.T) store.Record {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.recs) != 1 {
		t.Fatalf("want 1 record, got %d", len(r.recs))
	}
	return r.recs[0]
}

func newGW(t *testing.T, upstream http.HandlerFunc, mode guard.Mode) (*Gateway, *recorder, *string) {
	t.Helper()
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		upstream(w, r)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	rec := &recorder{}
	g := New(Config{
		Providers: []Provider{{Name: "anthropic", Upstream: u, APIKey: "server-key"}, {Name: "openai", Upstream: u}},
		Guard:     mode,
		Prices:    pricing.Default,
		Record:    rec.add,
	})
	return g, rec, &gotBody
}

func do(g http.Handler, path, body, ua string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("User-Agent", ua)
	req.Header.Set("X-Agentgate-User", "maya@example.com")
	req.Header.Set("x-api-key", "client-key")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	return w
}

func TestJSONUsageIsRecordedAndPriced(t *testing.T) {
	var gotPath, gotKey string
	g, rec, _ := newGW(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"claude-sonnet-4-5-20250929","usage":{"input_tokens":1000000,"output_tokens":100000}}`)
	}, guard.ModeBlock)

	w := do(g, "/anthropic/v1/messages", `{"model":"claude-sonnet-4-5","messages":[]}`, "claude-cli/2.0.14")
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("upstream path = %q, want /v1/messages", gotPath)
	}
	if gotKey != "server-key" {
		t.Errorf("upstream key = %q, want the server key to replace the client's", gotKey)
	}
	r := rec.last(t)
	if r.User != "maya@example.com" || r.Tool != "claude-code" || r.Provider != "anthropic" {
		t.Errorf("attribution wrong: %+v", r)
	}
	if r.InputTok != 1_000_000 || r.OutputTok != 100_000 {
		t.Errorf("tokens = %d/%d", r.InputTok, r.OutputTok)
	}
	// 1M in * $3 + 0.1M out * $15 = $4.50
	if !r.Priced || r.CostUSD < 4.499 || r.CostUSD > 4.501 {
		t.Errorf("cost = %v priced=%v, want 4.50", r.CostUSD, r.Priced)
	}
}

func TestAnthropicStreamPassesThroughAndCountsTokens(t *testing.T) {
	stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-haiku-4-5\",\"usage\":{\"input_tokens\":2500,\"cache_read_input_tokens\":500,\"output_tokens\":1}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":321}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	g, rec, _ := newGW(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, stream)
	}, guard.ModeBlock)

	w := do(g, "/anthropic/v1/messages", `{"model":"claude-haiku-4-5","stream":true}`, "claude-cli/2.0")
	if w.Body.String() != stream {
		t.Fatalf("stream was altered:\n%q", w.Body.String())
	}
	r := rec.last(t)
	if r.InputTok != 3000 || r.OutputTok != 321 || r.Model != "claude-haiku-4-5" {
		t.Errorf("got in=%d out=%d model=%q", r.InputTok, r.OutputTok, r.Model)
	}
}

func TestOpenAIStreamFinalUsageChunk(t *testing.T) {
	g, rec, _ := newGW(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"model\":\"gpt-5\",\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"model\":\"gpt-5\",\"choices\":[],\"usage\":{\"prompt_tokens\":800,\"completion_tokens\":90}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}, guard.ModeBlock)
	do(g, "/openai/v1/chat/completions", `{"model":"gpt-5","stream":true}`, "codex_cli_rs/0.46.0")
	r := rec.last(t)
	if r.InputTok != 800 || r.OutputTok != 90 || r.Tool != "codex" {
		t.Errorf("got %+v", r)
	}
}

func TestSecretIsBlockedBeforeLeaving(t *testing.T) {
	called := false
	g, rec, _ := newGW(t, func(w http.ResponseWriter, r *http.Request) { called = true }, guard.ModeBlock)
	key := "AKIA" + strings.Repeat("Z", 16)
	w := do(g, "/anthropic/v1/messages", `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"deploy with `+key+`"}]}`, "claude-cli/2.0")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", w.Code)
	}
	if called {
		t.Fatal("request reached the upstream")
	}
	if !strings.Contains(w.Body.String(), `"type":"error"`) || !strings.Contains(w.Body.String(), "aws_access_key") {
		t.Errorf("error body not in Anthropic format: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), key) {
		t.Error("error echoes the secret")
	}
	r := rec.last(t)
	if !r.Blocked || !strings.Contains(r.Findings, "aws_access_key") || strings.Contains(r.Findings, key) {
		t.Errorf("record = %+v", r)
	}
}

func TestSecretIsRedactedInRedactMode(t *testing.T) {
	g, rec, got := newGW(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"usage":{"input_tokens":5,"output_tokens":1}}`)
	}, guard.ModeRedact)
	tok := "ghp_" + strings.Repeat("a", 36)
	w := do(g, "/anthropic/v1/messages", `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"token `+tok+`"}]}`, "")
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if strings.Contains(*got, tok) || !strings.Contains(*got, "[REDACTED:github_token]") {
		t.Errorf("upstream saw %s", *got)
	}
	if r := rec.last(t); !r.Redacted || r.Blocked {
		t.Errorf("record = %+v", r)
	}
}

func TestUpstreamErrorRecorded(t *testing.T) {
	g, rec, _ := newGW(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(529)
		fmt.Fprint(w, `{"type":"error","error":{"type":"overloaded_error"}}`)
	}, guard.ModeBlock)
	w := do(g, "/anthropic/v1/messages", `{"model":"claude-opus-4-1"}`, "claude-cli/2.0")
	if w.Code != 529 {
		t.Fatalf("status %d", w.Code)
	}
	r := rec.last(t)
	if r.Status != 529 || r.Model != "claude-opus-4-1" {
		t.Errorf("record = %+v", r)
	}
}

func TestUnknownPrefixIs404(t *testing.T) {
	g, rec, _ := newGW(t, func(w http.ResponseWriter, r *http.Request) {}, guard.ModeBlock)
	if w := do(g, "/gemini/v1", `{}`, ""); w.Code != 404 {
		t.Fatalf("status %d", w.Code)
	}
	if len(rec.recs) != 0 {
		t.Fatal("unexpected record")
	}
}

func TestLatencyUsesClock(t *testing.T) {
	now := time.Unix(0, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now = now.Add(1500 * time.Millisecond)
		fmt.Fprint(w, `{"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	rec := &recorder{}
	g := New(Config{Providers: []Provider{{Name: "anthropic", Upstream: u}}, Prices: pricing.Default, Record: rec.add, Now: func() time.Time { return now }})
	do(g, "/anthropic/v1/messages", `{"model":"claude-sonnet-4-5"}`, "")
	if got := rec.last(t).LatencyMS; got != 1500 {
		t.Errorf("latency = %d, want 1500", got)
	}
}
