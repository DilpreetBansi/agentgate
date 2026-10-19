// Package guard scans outbound LLM requests for credentials before they
// leave the network. Coding agents read whole repos and paste terminal
// output into prompts, so secrets leak into model providers far more
// often than people expect.
package guard

import (
	"regexp"
	"sort"
)

// Mode decides what happens when a secret is found.
type Mode string

const (
	ModeOff    Mode = "off"    // scan nothing
	ModeLog    Mode = "log"    // record findings, forward the request unchanged
	ModeRedact Mode = "redact" // replace each secret with a placeholder, then forward
	ModeBlock  Mode = "block"  // reject the request
)

// Finding is one kind of secret and how many times it appeared.
// The secret value itself is never stored or logged.
type Finding struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

type rule struct {
	kind string
	re   *regexp.Regexp
}

var rules = []rule{
	{"aws_access_key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"github_token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,255}\b`)},
	{"github_pat", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{50,255}\b`)},
	{"anthropic_key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}`)},
	{"openai_key", regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_\-]{32,}`)},
	{"tailscale_key", regexp.MustCompile(`\btskey-[a-z]+-[A-Za-z0-9]{6,}-[A-Za-z0-9]{10,}`)},
	{"slack_token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`)},
	{"private_key", regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY-----[\s\S]*?-----END (?:RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY-----`)},
}

// Scan returns the kinds of secrets found in body, sorted by kind.
func Scan(body []byte) []Finding {
	var out []Finding
	seen := map[string]bool{}
	for _, r := range rules {
		n := len(r.re.FindAllIndex(body, -1))
		if n == 0 {
			continue
		}
		// openai_key also matches the tail of sk-ant- keys; don't double count.
		if r.kind == "openai_key" && seen["anthropic_key"] {
			n -= len(rules[3].re.FindAllIndex(body, -1))
			if n <= 0 {
				continue
			}
		}
		seen[r.kind] = true
		out = append(out, Finding{Kind: r.kind, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// Redact replaces every secret in body with [REDACTED:<kind>].
func Redact(body []byte) []byte {
	for _, r := range rules {
		kind := r.kind
		body = r.re.ReplaceAllFunc(body, func(m []byte) []byte {
			// An earlier rule may already have redacted this span.
			if len(m) > 0 && m[0] == '[' {
				return m
			}
			return []byte("[REDACTED:" + kind + "]")
		})
	}
	return body
}

// Total returns the number of secrets across all findings.
func Total(f []Finding) int {
	n := 0
	for _, x := range f {
		n += x.Count
	}
	return n
}
