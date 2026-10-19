// Package identity answers "who made this request?".
//
// On a tailnet the answer comes from the network itself: tsnet's WhoIs maps
// the caller's Tailscale IP to a user and a machine, so nobody has to hand
// out per-person API keys or trust a header. Off a tailnet (local dev,
// tests) the gateway falls back to a header.
package identity

import (
	"context"
	"net/http"
	"strings"
)

// Who is the caller of one request.
type Who struct {
	User string
	Node string
}

// Resolver identifies the caller of an HTTP request.
type Resolver interface {
	Resolve(r *http.Request) Who
}

// Header trusts X-Agentgate-User / X-Agentgate-Node. Only use it when the
// gateway is not reachable by untrusted clients.
type Header struct{}

func (Header) Resolve(r *http.Request) Who {
	u := strings.TrimSpace(r.Header.Get("X-Agentgate-User"))
	if u == "" {
		u = "anonymous"
	}
	n := strings.TrimSpace(r.Header.Get("X-Agentgate-Node"))
	if n == "" {
		n = "unknown"
	}
	return Who{User: u, Node: n}
}

// WhoIser is the subset of tailscale.com/client/local.Client we need. Keeping
// it an interface lets tests run without a tailnet.
type WhoIser interface {
	WhoIs(ctx context.Context, remoteAddr string) (user, node string, err error)
}

// Tailnet resolves callers with WhoIs and falls back to "unknown" on error.
type Tailnet struct{ C WhoIser }

func (t Tailnet) Resolve(r *http.Request) Who {
	user, node, err := t.C.WhoIs(r.Context(), r.RemoteAddr)
	if err != nil || user == "" {
		return Who{User: "unknown", Node: "unknown"}
	}
	return Who{User: user, Node: node}
}
