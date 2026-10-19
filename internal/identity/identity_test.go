package identity

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

type fakeWhoIs struct {
	user, node string
	err        error
	gotAddr    string
}

func (f *fakeWhoIs) WhoIs(_ context.Context, addr string) (string, string, error) {
	f.gotAddr = addr
	return f.user, f.node, f.err
}

func TestTailnetUsesWhoIsOnRemoteAddr(t *testing.T) {
	f := &fakeWhoIs{user: "maya@example.com", node: "maya-mbp"}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "100.101.102.103:51234"
	r.Header.Set("X-Agentgate-User", "spoofed@example.com") // must be ignored on a tailnet
	got := Tailnet{C: f}.Resolve(r)
	if got.User != "maya@example.com" || got.Node != "maya-mbp" || f.gotAddr != r.RemoteAddr {
		t.Fatalf("got %+v (asked about %q)", got, f.gotAddr)
	}
}

func TestTailnetFallsBackOnError(t *testing.T) {
	got := Tailnet{C: &fakeWhoIs{err: errors.New("no peer")}}.Resolve(httptest.NewRequest("GET", "/", nil))
	if got.User != "unknown" {
		t.Fatalf("got %+v", got)
	}
}

func TestHeaderDefaults(t *testing.T) {
	got := Header{}.Resolve(httptest.NewRequest("GET", "/", nil))
	if got.User != "anonymous" || got.Node != "unknown" {
		t.Fatalf("got %+v", got)
	}
}
