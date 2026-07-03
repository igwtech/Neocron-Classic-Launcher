package auth

import (
	"encoding/json"
	"testing"
)

// RFC 7636 Appendix B: verifier -> S256 challenge known-answer.
func TestPKCEChallengeKnownAnswer(t *testing.T) {
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const want = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := pkceChallenge(verifier); got != want {
		t.Fatalf("pkceChallenge = %q, want %q", got, want)
	}
}

func TestParseAccountsToleratesFieldNames(t *testing.T) {
	raw := json.RawMessage(`[
		{"id": 12, "name": "Krafteo"},
		{"account_id": 34, "username": "Norman", "disabled": true},
		{"user_id": "56", "account_name": "Hannibal", "banned": true}
	]`)
	got := parseAccounts(raw)
	if len(got) != 3 {
		t.Fatalf("got %d accounts, want 3", len(got))
	}
	if got[0].ID != 12 || got[0].Name != "Krafteo" || got[0].Disabled {
		t.Errorf("acct0 = %+v", got[0])
	}
	if got[1].ID != 34 || got[1].Name != "Norman" || !got[1].Disabled {
		t.Errorf("acct1 = %+v", got[1])
	}
	if got[2].ID != 56 || got[2].Name != "Hannibal" || !got[2].Disabled {
		t.Errorf("acct2 = %+v", got[2])
	}
}

// GET /me/accounts wraps the list in an {"accounts":[...]} envelope (verified
// against the live auth service). It must parse the same as the bare array the
// exchange reply embeds — otherwise linked accounts are forgotten on restart.
func TestParseAccountsWrappedEnvelope(t *testing.T) {
	raw := json.RawMessage(`{"accounts":[{"user_id":315,"name":"msn2wolf","banned":false,"expired":false}]}`)
	got := parseAccounts(raw)
	if len(got) != 1 {
		t.Fatalf("got %d accounts, want 1", len(got))
	}
	if got[0].ID != 315 || got[0].Name != "msn2wolf" || got[0].Disabled {
		t.Errorf("acct = %+v", got[0])
	}
}

func TestParseDiscordFallbacks(t *testing.T) {
	d := parseDiscord(json.RawMessage(`{"id":"9","global_name":"Ada","avatar":"a.png"}`))
	if d.ID != "9" || d.Name != "Ada" || d.Avatar != "a.png" {
		t.Fatalf("discord = %+v", d)
	}
}
