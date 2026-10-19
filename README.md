# agentgate

A small gateway that sits between coding agents (Claude Code, Codex, Cursor, Aider) and the model APIs, so an engineering org can see and steer how it uses AI without getting in engineers' way.

- **Who is using what.** Every call is attributed to a person and machine. On a tailnet, agentgate runs as a [tsnet](https://tailscale.com/kb/1244/tsnet) node and identifies callers with `WhoIs` on their Tailscale IP, so there are no per-person API keys to issue, rotate or revoke.
- **What it costs.** Token usage is parsed from both providers' JSON and streaming (SSE) responses and priced per model. Prices are a JSON file you can override.
- **Secrets stay home.** Request bodies are scanned for credentials (AWS keys, GitHub and Slack tokens, provider API keys, Tailscale auth keys, private keys) before they leave the network. `-guard block` returns a clear error in the provider's own format, `redact` swaps the secret for a placeholder, `log` only records it. Secret values are never stored.
- **Zero client changes.** Agents keep talking to the normal API; only the base URL changes. Responses stream through untouched.
- **A dashboard** for spend per day, per engineer, per tool and per model, latency (p50/p95), error rates, and every blocked request.

Live demo of the dashboard with 30 days of synthetic traffic: https://dilpreetbansi.github.io/agentgate/

## Why

AI enablement works when using AI is the easy, safe default instead of a mandate. That needs three things a platform team usually doesn't have on day one: visibility into what people actually use, a way to stop credentials leaking into prompts, and a setup cost close to zero for engineers. agentgate is a sketch of that layer.

## Run it

```sh
# On a tailnet (identity from WhoIs):
TS_AUTHKEY=tskey-auth-... go run ./cmd/agentgate -tsnet agentgate

# Locally (identity from the X-Agentgate-User header, for dev only):
go run ./cmd/agentgate -listen :8080
```

Point agents at it:

```sh
ANTHROPIC_BASE_URL=http://agentgate/anthropic claude
OPENAI_BASE_URL=http://agentgate/openai/v1 codex
```

Optional: set `AGENTGATE_ANTHROPIC_KEY` / `AGENTGATE_OPENAI_KEY` and the gateway uses one org key upstream, so laptops never hold a provider key at all.

Flags: `-guard off|log|redact|block` (default `block`), `-prices prices.json`, `-db agentgate.db`, `-anthropic-upstream`, `-openai-upstream`.

## Demo data

```sh
go run ./cmd/agentgate-demo -db demo.db -export web/demo.json
go run ./cmd/agentgate -db demo.db
```

The demo command doesn't insert rows directly. It replays about 3,500 synthetic calls from 12 engineers through the real gateway against a mock model API (JSON and streaming responses, rate limits, overloads, and the occasional pasted credential), so the dashboard reflects the same proxying, usage parsing, pricing and secret scanning that production traffic goes through.

## Layout

```
cmd/agentgate          the server (tsnet or plain HTTP)
cmd/agentgate-demo     synthetic traffic through the real gateway
internal/gateway       reverse proxy, usage parsing (JSON + SSE)
internal/guard         secret scanning and redaction
internal/identity      tailnet WhoIs and header identity
internal/pricing       per-model prices, longest-prefix match
internal/store         SQLite storage and dashboard aggregates
web                    the dashboard (one static page)
```

## Tests

```sh
go test ./...
```

19 tests cover streaming and non-streaming usage for both providers, the server-side key swap, blocking and redaction (the secret never reaches the upstream or the logs), error passthrough, WhoIs attribution (a spoofed header is ignored on a tailnet), pricing, and the aggregates.

## Not done yet

- Per-team budgets and alerts.
- Tailscale ACL grants to decide who may use which models.
- Prompt and response sampling for quality review (opt-in, with retention limits).

---

Built by Dilpreet Singh Bansi. Not affiliated with Tailscale.
