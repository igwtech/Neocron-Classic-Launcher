// Package auth implements the Neocron Classic launcher's Discord sign-in and
// account/ticket flow, reproduced byte-for-byte from the official launcher's
// service contract (see docs/RE_LAUNCHER.md §5).
//
// Flow:
//  1. Generate a PKCE verifier; challenge = base64url(SHA256(verifier)), S256.
//  2. Bind a loopback HTTP listener on 127.0.0.1:<port>/callback.
//  3. POST {base}/auth/discord/start {code_challenge, code_challenge_method,
//     loopback_port, launcher_version} -> {authorize_url, state, expires_in}.
//  4. Open authorize_url in the browser; catch the /callback redirect.
//  5. POST {base}/auth/discord/exchange {code, code_verifier, state}
//     -> {session_token, expires_at, discord{...}, accounts[...]}.
//  6. GET {base}/me/accounts (Bearer) to refresh; POST {base}/launch-tickets
//     {user_id} (Bearer) to mint a launch ticket; POST {base}/auth/logout.
package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// LauncherVersion is sent as launcher_version in the start request and as the
// User-Agent. Kept in sync with the official launcher build we mirror.
const LauncherVersion = "2.2.6"

// Account is one linked game account for the signed-in Discord user.
type Account struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
}

// Discord holds the signed-in Discord identity shown in the launcher.
type Discord struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Avatar   string `json:"avatar"`
	FullName string `json:"fullName"`
}

// Session is the persisted result of a successful sign-in.
type Session struct {
	Token     string    `json:"session_token"`
	ExpiresAt string    `json:"expires_at"`
	Discord   Discord   `json:"discord"`
	Accounts  []Account `json:"accounts"`
}

// startResponse is {base}/auth/discord/start's reply.
type startResponse struct {
	AuthorizeURL string `json:"authorize_url"`
	State        string `json:"state"`
	ExpiresIn    int    `json:"expires_in"`
}

// exchangeResponse is {base}/auth/discord/exchange's reply. Mirrors the
// decompiled field names (session_token, expires_at, discord, accounts).
type exchangeResponse struct {
	SessionToken string          `json:"session_token"`
	ExpiresAt    string          `json:"expires_at"`
	Discord      json.RawMessage `json:"discord"`
	Accounts     json.RawMessage `json:"accounts"`
}

// Client talks to the auth service. Zero value is not usable; use New.
type Client struct {
	baseURL string
	http    *http.Client
	ua      string

	mu       sync.Mutex
	pending  *pendingSignIn // in-flight interactive sign-in, if any
}

type pendingSignIn struct {
	cancel   context.CancelFunc
	listener net.Listener
}

// New returns a Client for the given auth base URL (e.g.
// https://auth.neocron.org). An empty base falls back to the official host.
func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://auth.neocron.org"
	}
	return &Client{
		baseURL: baseURL,
		ua:      "NeocronLauncher-cpp/" + LauncherVersion,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// --- PKCE -------------------------------------------------------------------

func randomB64URL(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// --- interactive sign-in ----------------------------------------------------

// callback HTML served on the loopback listener (matches the official launcher).
const (
	pageSignedIn = `<!doctype html><html><head><meta charset="utf-8"><title>Neocron Launcher</title>` +
		`<style>body{background:#0c0f14;color:#cdd6e4;font-family:system-ui,sans-serif;display:grid;place-items:center;height:100vh;margin:0}main{text-align:center}</style>` +
		`</head><body><main><h1>Signed in</h1><p>You can close this tab and return to the launcher.</p></main></body></html>`
	pageFailed = `<!doctype html><html><head><meta charset="utf-8"><title>Neocron Launcher</title>` +
		`<style>body{background:#0c0f14;color:#cdd6e4;font-family:system-ui,sans-serif;display:grid;place-items:center;height:100vh;margin:0}main{text-align:center}</style>` +
		`</head><body><main><h1>Sign-in not completed</h1><p>Return to the launcher and try again.</p></main></body></html>`
)

// SignIn runs the full interactive PKCE loopback flow. openBrowser is called
// with the Discord authorize URL (typically wails runtime.BrowserOpenURL). It
// blocks until the browser redirect completes, the context is cancelled, or the
// start-response TTL elapses. On success it returns and persists nothing itself
// — the caller stores the Session.
func (c *Client) SignIn(ctx context.Context, openBrowser func(string)) (*Session, error) {
	// 1. Loopback listener on an OS-chosen port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("bind loopback: %w", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	if c.pending != nil {
		c.pending.cancel()
	}
	c.pending = &pendingSignIn{cancel: cancel, listener: ln}
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.pending = nil; c.mu.Unlock() }()

	// 2. PKCE.
	verifier, err := randomB64URL(48)
	if err != nil {
		return nil, err
	}
	challenge := pkceChallenge(verifier)

	// 3. start.
	start, err := c.startDiscord(ctx, challenge, port)
	if err != nil {
		return nil, err
	}

	// 4. Serve the callback while the user authorizes in the browser.
	type cbResult struct {
		code  string
		state string
		err   error
	}
	resCh := make(chan cbResult, 1)
	srv := &http.Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code, state, oauthErr := q.Get("code"), q.Get("state"), q.Get("error")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if oauthErr != "" || code == "" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, pageFailed)
			resCh <- cbResult{err: fmt.Errorf("discord authorization failed: %s", firstNonEmpty(oauthErr, "no code"))}
			return
		}
		io.WriteString(w, pageSignedIn)
		resCh <- cbResult{code: code, state: state}
	})
	srv.Handler = mux
	go srv.Serve(ln)
	defer srv.Close()

	openBrowser(start.AuthorizeURL)

	ttl := time.Duration(start.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(ttl):
		return nil, fmt.Errorf("sign-in timed out")
	case res := <-resCh:
		if res.err != nil {
			return nil, res.err
		}
		if res.state != start.State {
			return nil, fmt.Errorf("oauth state mismatch")
		}
		// 5. exchange.
		return c.exchangeDiscord(ctx, res.code, verifier, start.State)
	}
}

// Cancel aborts an in-flight interactive sign-in (the UI's "Cancel" link).
func (c *Client) Cancel() {
	c.mu.Lock()
	p := c.pending
	c.mu.Unlock()
	if p != nil {
		p.cancel()
	}
}

func (c *Client) startDiscord(ctx context.Context, challenge string, port int) (*startResponse, error) {
	body := map[string]any{
		"code_challenge":        challenge,
		"code_challenge_method": "S256",
		"loopback_port":         port,
		"launcher_version":      LauncherVersion,
	}
	var out startResponse
	if err := c.postJSON(ctx, "/auth/discord/start", "", body, &out); err != nil {
		return nil, err
	}
	if out.AuthorizeURL == "" || out.State == "" {
		return nil, fmt.Errorf("auth start returned an incomplete response")
	}
	return &out, nil
}

func (c *Client) exchangeDiscord(ctx context.Context, code, verifier, state string) (*Session, error) {
	body := map[string]any{
		"code":          code,
		"code_verifier": verifier,
		"state":         state,
	}
	var raw exchangeResponse
	if err := c.postJSON(ctx, "/auth/discord/exchange", "", body, &raw); err != nil {
		return nil, err
	}
	if raw.SessionToken == "" {
		return nil, fmt.Errorf("auth exchange returned no session token")
	}
	s := &Session{Token: raw.SessionToken, ExpiresAt: raw.ExpiresAt}
	s.Discord = parseDiscord(raw.Discord)
	s.Accounts = parseAccounts(raw.Accounts)
	return s, nil
}

// --- authenticated calls ----------------------------------------------------

// Accounts refreshes the linked account list for a session (GET /me/accounts).
func (c *Client) Accounts(ctx context.Context, token string) ([]Account, error) {
	var raw json.RawMessage
	if err := c.getJSON(ctx, "/me/accounts", token, &raw); err != nil {
		return nil, err
	}
	return parseAccounts(raw), nil
}

// MintTicket requests a launch ticket for one account (POST /launch-tickets).
func (c *Client) MintTicket(ctx context.Context, token string, accountID int) (string, error) {
	body := map[string]any{"user_id": accountID}
	var out struct {
		Ticket string `json:"ticket"`
	}
	if err := c.postJSON(ctx, "/launch-tickets", token, body, &out); err != nil {
		return "", err
	}
	if out.Ticket == "" {
		return "", fmt.Errorf("launch ticket response malformed")
	}
	return out.Ticket, nil
}

// Logout invalidates the session (POST /auth/logout). Best-effort.
func (c *Client) Logout(ctx context.Context, token string) error {
	return c.postJSON(ctx, "/auth/logout", token, map[string]any{}, nil)
}

// --- HTTP helpers -----------------------------------------------------------

// ErrUnauthorized is returned on a 401 so callers can drop the session.
type ErrUnauthorized struct{ Path string }

func (e *ErrUnauthorized) Error() string { return "session rejected (401) at " + e.Path }

func (c *Client) postJSON(ctx context.Context, path, token string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, path, token, out)
}

func (c *Client) getJSON(ctx context.Context, path, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	return c.do(req, path, token, out)
}

func (c *Client) do(req *http.Request, path, token string, out any) error {
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return &ErrUnauthorized{Path: path}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, truncate(string(data), 200))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: bad JSON: %w", path, err)
	}
	return nil
}

// --- lenient JSON parsing (server field names vary; be forgiving) -----------

func parseDiscord(raw json.RawMessage) Discord {
	var d Discord
	if len(raw) == 0 {
		return d
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return d
	}
	d.ID = str(m, "id")
	d.Name = firstNonEmpty(str(m, "username"), str(m, "name"), str(m, "global_name"))
	d.Avatar = firstNonEmpty(str(m, "avatar"), str(m, "avatar_url"))
	d.FullName = firstNonEmpty(str(m, "global_name"), d.Name)
	return d
}

func parseAccounts(raw json.RawMessage) []Account {
	if len(raw) == 0 {
		return nil
	}
	// The list arrives in two shapes: the /auth/discord/exchange reply embeds a
	// bare array ([...]) via its struct field, while GET /me/accounts wraps it in
	// an {"accounts":[...]} envelope. Accept both so the account list survives a
	// restart (otherwise a wrapped reply fails to parse and the session persists
	// with a null account list — the "linked accounts forgotten" bug).
	var arr []map[string]any
	if json.Unmarshal(raw, &arr) != nil {
		var env struct {
			Accounts []map[string]any `json:"accounts"`
			Data     []map[string]any `json:"data"`
		}
		if json.Unmarshal(raw, &env) != nil {
			return nil
		}
		if arr = env.Accounts; arr == nil {
			arr = env.Data
		}
	}
	out := make([]Account, 0, len(arr))
	for _, m := range arr {
		out = append(out, Account{
			ID:       intOf(m, "id", "user_id", "account_id"),
			Name:     firstNonEmpty(str(m, "name"), str(m, "username"), str(m, "account_name")),
			Disabled: boolOf(m, "disabled") || boolOf(m, "banned") || boolOf(m, "locked"),
		})
	}
	return out
}

func str(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func intOf(m map[string]any, keys ...string) int {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return int(v)
		case int:
			return v
		case string:
			var n int
			fmt.Sscan(v, &n)
			if n != 0 {
				return n
			}
		}
	}
	return 0
}

func boolOf(m map[string]any, k string) bool {
	b, _ := m[k].(bool)
	return b
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
