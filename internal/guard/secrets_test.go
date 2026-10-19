package guard

import (
	"strings"
	"testing"
)

// Test keys are assembled at runtime so this file never holds a real-looking secret.
var (
	aws    = "AKIA" + strings.Repeat("Q", 16)
	gh     = "ghp_" + strings.Repeat("x", 36)
	ant    = "sk-ant-api03-" + strings.Repeat("y", 40)
	oai    = "sk-proj-" + strings.Repeat("z", 40)
	ts     = "tskey-auth-" + "kXyz12CNTRL" + "-" + strings.Repeat("w", 24)
	pemKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----"
)

func TestScanFindsEachKind(t *testing.T) {
	cases := map[string]string{
		"aws_access_key": aws,
		"github_token":   gh,
		"anthropic_key":  ant,
		"openai_key":     oai,
		"tailscale_key":  ts,
		"private_key":    pemKey,
	}
	for kind, secret := range cases {
		fs := Scan([]byte(`{"content":"here: ` + strings.ReplaceAll(secret, "\n", `\n`) + `"}`))
		if len(fs) != 1 || fs[0].Kind != kind || fs[0].Count != 1 {
			t.Errorf("%s: got %+v", kind, fs)
		}
	}
}

func TestAnthropicKeyNotDoubleCountedAsOpenAI(t *testing.T) {
	fs := Scan([]byte(ant))
	if len(fs) != 1 || fs[0].Kind != "anthropic_key" {
		t.Fatalf("got %+v", fs)
	}
}

func TestCleanCodeHasNoFindings(t *testing.T) {
	src := `func main() { key := os.Getenv("AWS_ACCESS_KEY_ID"); fmt.Println("sk-" + "short") }`
	if fs := Scan([]byte(src)); len(fs) != 0 {
		t.Fatalf("false positive: %+v", fs)
	}
}

func TestRedactRemovesSecretsAndKeepsText(t *testing.T) {
	in := "use " + aws + " and " + ant + " please"
	out := string(Redact([]byte(in)))
	if strings.Contains(out, aws) || strings.Contains(out, ant) {
		t.Fatalf("secret survived: %s", out)
	}
	want := "use [REDACTED:aws_access_key] and [REDACTED:anthropic_key] please"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
	if fs := Scan([]byte(out)); len(fs) != 0 {
		t.Errorf("redacted text still scans dirty: %+v", fs)
	}
}
