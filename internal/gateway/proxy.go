// Package gateway is the reverse proxy that sits between coding agents and
// model providers. Every call is attributed to a person, priced, timed and
// scanned for secrets, without changing how the agent talks to the API.
package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/DilpreetBansi/agentgate/internal/guard"
	"github.com/DilpreetBansi/agentgate/internal/identity"
	"github.com/DilpreetBansi/agentgate/internal/pricing"
	"github.com/DilpreetBansi/agentgate/internal/store"
)

const maxBody = 32 << 20 // 32 MiB: large enough for big agent contexts

// Provider is one upstream model API.
type Provider struct {
	Name     string   // "anthropic" or "openai"; also the URL prefix
	Upstream *url.URL // e.g. https://api.anthropic.com
	// APIKey, if set, replaces whatever key the client sent. This lets a
	// team share one org key without handing it to every laptop.
	APIKey string
}

// Config wires the gateway together.
type Config struct {
	Providers []Provider
	Guard     guard.Mode
	Prices    pricing.Table
	Identity  identity.Resolver
	Record    func(store.Record) // called once per finished call
	Now       func() time.Time
}

type Gateway struct {
	cfg     Config
	proxies map[string]*httputil.ReverseProxy
	byName  map[string]Provider
}

func New(cfg Config) *Gateway {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Identity == nil {
		cfg.Identity = identity.Header{}
	}
	g := &Gateway{cfg: cfg, proxies: map[string]*httputil.ReverseProxy{}, byName: map[string]Provider{}}
	for _, p := range cfg.Providers {
		p := p
		g.byName[p.Name] = p
		g.proxies[p.Name] = &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(p.Upstream)
				pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, "/"+p.Name)
				pr.Out.URL.RawPath = ""
				pr.Out.Host = p.Upstream.Host
				pr.Out.Header.Del("X-Agentgate-User")
				pr.Out.Header.Del("X-Agentgate-Node")
				if p.APIKey != "" {
					if p.Name == "anthropic" {
						pr.Out.Header.Set("x-api-key", p.APIKey)
						pr.Out.Header.Del("Authorization")
					} else {
						pr.Out.Header.Set("Authorization", "Bearer "+p.APIKey)
					}
				}
			},
			FlushInterval: -1, // stream SSE tokens to the agent as they arrive
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				log.Printf("agentgate: upstream %s: %v", p.Name, err)
				writeError(w, p.Name, http.StatusBadGateway, "api_error", "agentgate: upstream unavailable")
				if c, ok := r.Context().Value(callKey{}).(*call); ok {
					c.finish(http.StatusBadGateway, nil, "")
				}
			},
			ModifyResponse: func(resp *http.Response) error {
				c, ok := resp.Request.Context().Value(callKey{}).(*call)
				if !ok {
					return nil
				}
				resp.Body = &tee{rc: resp.Body, c: c, status: resp.StatusCode, ctype: resp.Header.Get("Content-Type")}
				return nil
			},
		}
	}
	return g
}

type callKey struct{}

// call tracks one request from arrival until the response body is closed.
type call struct {
	g      *Gateway
	start  time.Time
	rec    store.Record
	reqMod string
	once   sync.Once
}

func (c *call) finish(status int, body []byte, ctype string) {
	c.once.Do(func() {
		c.rec.Status = status
		c.rec.LatencyMS = c.g.cfg.Now().Sub(c.start).Milliseconds()
		if body != nil {
			u := parseUsage(ctype, body)
			c.rec.InputTok, c.rec.OutputTok = u.InputTok, u.OutputTok
			if u.Model != "" {
				c.rec.Model = u.Model
			}
		}
		if c.rec.Model == "" {
			c.rec.Model = c.reqMod
		}
		c.rec.CostUSD, c.rec.Priced = c.g.cfg.Prices.Cost(c.rec.Model, c.rec.InputTok, c.rec.OutputTok)
		if c.g.cfg.Record != nil {
			c.g.cfg.Record(c.rec)
		}
	})
}

// tee passes the upstream body through untouched while keeping a copy
// (capped) for usage parsing once the agent has read it all.
type tee struct {
	rc     io.ReadCloser
	c      *call
	buf    bytes.Buffer
	status int
	ctype  string
}

func (t *tee) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	if n > 0 && t.buf.Len() < maxBody {
		t.buf.Write(p[:n])
	}
	return n, err
}

func (t *tee) Close() error {
	err := t.rc.Close()
	t.c.finish(t.status, t.buf.Bytes(), t.ctype)
	return err
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)[0]
	p, ok := g.byName[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	who := g.cfg.Identity.Resolve(r)
	c := &call{g: g, start: g.cfg.Now(), rec: store.Record{
		Time: g.cfg.Now().UTC(), User: who.User, Node: who.Node,
		Tool: toolFromUA(r.UserAgent()), Provider: p.Name, Findings: "[]",
	}}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	r.Body.Close()
	if err != nil || len(body) > maxBody {
		writeError(w, p.Name, http.StatusRequestEntityTooLarge, "request_too_large", "agentgate: request body too large")
		c.finish(http.StatusRequestEntityTooLarge, nil, "")
		return
	}
	c.reqMod = requestModel(body)

	if g.cfg.Guard != guard.ModeOff && g.cfg.Guard != "" {
		if fs := guard.Scan(body); len(fs) > 0 {
			b, _ := json.Marshal(fs)
			c.rec.Findings = string(b)
			switch g.cfg.Guard {
			case guard.ModeBlock:
				c.rec.Blocked = true
				writeError(w, p.Name, http.StatusForbidden, "permission_error",
					fmt.Sprintf("agentgate: request blocked, it contains %d secret(s): %s. Remove them and retry.", guard.Total(fs), kinds(fs)))
				c.finish(http.StatusForbidden, nil, "")
				return
			case guard.ModeRedact:
				body = guard.Redact(body)
				c.rec.Redacted = true
			}
		}
	}

	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Del("Accept-Encoding") // keep responses uncompressed so usage can be parsed
	ctx := r.Context()
	r = r.WithContext(contextWith(ctx, c))
	g.proxies[p.Name].ServeHTTP(w, r)
}

func kinds(fs []guard.Finding) string {
	ks := make([]string, len(fs))
	for i, f := range fs {
		ks[i] = f.Kind
	}
	return strings.Join(ks, ", ")
}

// toolFromUA labels which agent made the call so teams can compare tools.
func toolFromUA(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case strings.Contains(l, "claude-cli"), strings.Contains(l, "claude-code"):
		return "claude-code"
	case strings.Contains(l, "codex"):
		return "codex"
	case strings.Contains(l, "cursor"):
		return "cursor"
	case strings.Contains(l, "aider"):
		return "aider"
	case l == "":
		return "unknown"
	default:
		return "other"
	}
}

// writeError replies in the provider's own error shape so agents surface
// the message to the user instead of crashing on an unknown format.
func writeError(w http.ResponseWriter, provider string, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var v any
	if provider == "anthropic" {
		v = map[string]any{"type": "error", "error": map[string]string{"type": typ, "message": msg}}
	} else {
		v = map[string]any{"error": map[string]string{"type": typ, "message": msg, "code": typ}}
	}
	_ = json.NewEncoder(w).Encode(v)
}
