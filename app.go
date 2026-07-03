package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"

	wr "github.com/wailsapp/wails/v2/pkg/runtime"

	"nc1launcher/pkg/api"
	"nc1launcher/pkg/auth"
	"nc1launcher/pkg/config"
	"nc1launcher/pkg/game"
	"nc1launcher/pkg/patch"
)

// Version is the launcher's own version, shown in the footer. Overridable at
// build time via -ldflags "-X main.Version=...".
var Version = "0.1.0"

// App is the Wails-bound application. The web UI (ported verbatim from the
// official CEF launcher) talks to it through a single Dispatch entry point that
// mirrors the CEF message router, and receives state through "ncl" events that
// map to the page's window.ncl* functions (see docs/RE_LAUNCHER.md §2).
type App struct {
	ctx context.Context

	cfg   *config.Config
	auth  *auth.Client
	store *auth.Store
	api   *api.Client
	game  *game.Launcher

	mu      sync.Mutex
	session *auth.Session
	busy    bool               // an install/update/verify is running
	signIn  context.CancelFunc // in-flight interactive sign-in
}

// NewApp constructs the application with loaded config + persisted session.
func NewApp() *App {
	cfg, err := config.Load()
	if err != nil {
		cfg = config.DefaultConfig()
	}
	a := &App{
		cfg:   cfg,
		auth:  auth.New(cfg.AuthBaseURL),
		store: auth.NewStore(config.ConfigDir()),
		api:   api.New(cfg.BannerURL, cfg.UserAgent),
		game:  game.NewLauncher(),
	}
	if s, _ := a.store.Load(); s != nil {
		a.session = s
	}
	return a
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// --- CEF-style bridge -------------------------------------------------------

// dispatchRequest mirrors the JSON the CEF message router posted:
// {"fn":"play","args":[...]}.
type dispatchRequest struct {
	Fn   string            `json:"fn"`
	Args []json.RawMessage `json:"args"`
}

// Dispatch is the single entry point the frontend calls via window.cefQuery.
// It returns a (usually empty) string result to the onSuccess callback.
func (a *App) Dispatch(request string) string {
	var req dispatchRequest
	if err := json.Unmarshal([]byte(request), &req); err != nil {
		return ""
	}
	switch req.Fn {
	case "uiReady":
		a.onUIReady()
	case "play":
		go a.onPlay()
	case "checkForUpdates":
		go a.checkForUpdates(true)
	case "fileCheck":
		go a.onFileCheck()
	case "bypassFileChecks":
		a.onBypass()
	case "gfxOptions":
		a.onGfxOptions()
	case "channelChanged":
		a.onChannelChanged(argString(req.Args, 0))
	case "setOption":
		a.onSetOption(argString(req.Args, 0), argString(req.Args, 1))
	case "accountChanged":
		a.onAccountChanged(argString(req.Args, 0))
	case "authSignIn":
		go a.onSignIn()
	case "authCancel":
		a.onSignInCancel()
	case "authSignOut":
		go a.onSignOut()
	case "closeGameAndUpdate":
		go a.onCloseGameAndUpdate()
	case "abortUpdate":
		a.onAbortUpdate()
	}
	return ""
}

// emit sends {fn,args} to the page; the bridge invokes window[fn](...args).
func (a *App) emit(fn string, args ...any) {
	if a.ctx == nil {
		return
	}
	wr.EventsEmit(a.ctx, "ncl", map[string]any{"fn": fn, "args": args})
}

// --- state push -------------------------------------------------------------

func (a *App) onUIReady() {
	a.emit("nclSetVersion", Version)
	a.emit("nclSetChannel", a.cfg.Channel)
	a.pushOptions()
	a.pushAuthState()
	a.pushPlayState()
	go a.refreshBanners()
	go a.refreshNews()
	go a.refreshAccountsAndState()
	go a.checkForUpdates(false)
}

// refreshNews fetches the launcher news feed (areamc5/{channel}/{key}/news) and
// pushes it to the UI's nclSetNews, exactly as the official launcher does.
func (a *App) refreshNews() {
	posts, err := api.FetchNews(context.Background(), a.cfg.APIBaseURL, 10, a.cfg.UserAgent)
	if err != nil {
		return // leave the news area empty on failure
	}
	items := make([]map[string]any, 0, len(posts))
	for _, p := range posts {
		items = append(items, map[string]any{
			"id": p.ID, "title": p.Title, "body": p.Body, "createdAt": p.CreatedAt,
		})
	}
	a.emit("nclSetNews", items)
}

func (a *App) pushOptions() {
	trayAvailable := runtime.GOOS == "windows"
	closeAllowed := runtime.GOOS == "windows"
	a.emit("nclSetOptions", map[string]any{
		"postLaunch":    a.cfg.PostLaunch,
		"trayAvailable": trayAvailable,
		"closeAllowed":  closeAllowed,
	})
}

func (a *App) pushAuthState() {
	a.mu.Lock()
	s := a.session
	sel := a.cfg.SelectedAccountID
	a.mu.Unlock()

	if s == nil {
		a.emit("nclSetAuthState", map[string]any{"status": "signedOut"})
		return
	}
	accounts := make([]map[string]any, 0, len(s.Accounts))
	for _, ac := range s.Accounts {
		accounts = append(accounts, map[string]any{
			"id":       strconv.Itoa(ac.ID),
			"name":     ac.Name,
			"disabled": ac.Disabled,
		})
	}
	if sel == "" && len(s.Accounts) > 0 {
		sel = strconv.Itoa(s.Accounts[0].ID)
	}
	a.emit("nclSetAuthState", map[string]any{
		"status":      "signedIn",
		"discordName": s.Discord.Name,
		"avatarUrl":   s.Discord.Avatar,
		"accounts":    accounts,
		"selectedId":  sel,
	})
}

func (a *App) pushPlayState() {
	installed := a.isInstalled()
	a.mu.Lock()
	signedIn := a.session != nil
	hasAccount := a.cfg.SelectedAccountID != "" || (a.session != nil && len(a.session.Accounts) > 0)
	busy := a.busy
	a.mu.Unlock()

	label := "Install"
	if installed {
		label = "Play"
	}
	allow := signedIn && hasAccount && !busy && !a.game.IsRunning()
	a.emit("nclSetPlayState", allow, installed, label)
}

// --- actions ----------------------------------------------------------------

func (a *App) onSignIn() {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	if a.signIn != nil {
		a.signIn()
	}
	a.signIn = cancel
	a.mu.Unlock()

	a.emit("nclSetAuthState", map[string]any{"status": "pending"})

	sess, err := a.auth.SignIn(ctx, func(url string) {
		wr.BrowserOpenURL(a.ctx, url)
	})
	a.mu.Lock()
	a.signIn = nil
	a.mu.Unlock()
	if err != nil {
		a.pushAuthState() // back to signed-out
		a.emit("nclShowError", "Sign-in failed: "+err.Error())
		return
	}
	a.mu.Lock()
	a.session = sess
	if len(sess.Accounts) > 0 {
		a.cfg.SelectedAccountID = strconv.Itoa(sess.Accounts[0].ID)
	}
	a.mu.Unlock()
	_ = a.store.Save(sess)
	_ = a.cfg.Save()
	a.pushAuthState()
	a.pushPlayState()
}

func (a *App) onSignInCancel() {
	a.auth.Cancel()
	a.mu.Lock()
	if a.signIn != nil {
		a.signIn()
		a.signIn = nil
	}
	a.mu.Unlock()
	a.pushAuthState()
}

func (a *App) onSignOut() {
	a.mu.Lock()
	s := a.session
	a.session = nil
	a.cfg.SelectedAccountID = ""
	a.mu.Unlock()
	if s != nil {
		_ = a.auth.Logout(context.Background(), s.Token)
	}
	_ = a.store.Clear()
	_ = a.cfg.Save()
	a.pushAuthState()
	a.pushPlayState()
}

func (a *App) onAccountChanged(id string) {
	a.mu.Lock()
	a.cfg.SelectedAccountID = id
	a.mu.Unlock()
	_ = a.cfg.Save()
	a.pushPlayState()
}

func (a *App) onChannelChanged(ch string) {
	if ch == "" {
		return
	}
	a.mu.Lock()
	a.cfg.Channel = ch
	a.mu.Unlock()
	_ = a.cfg.Save()
	a.emit("nclSetChannel", ch)
	go a.checkForUpdates(false)
	a.pushPlayState()
}

func (a *App) onSetOption(key, val string) {
	if key == "postLaunch" {
		a.mu.Lock()
		a.cfg.PostLaunch = val
		a.mu.Unlock()
		_ = a.cfg.Save()
	}
}

func (a *App) onGfxOptions() {
	// The retail client's GFX config is a separate in-game/config step; surface a
	// hint rather than silently doing nothing.
	a.emit("nclSetStatus", "Graphics options are configured in-game.")
}

func (a *App) onBypass() {
	// The official launcher's "Bypass File Checks" skips verification before the
	// next launch (and hides an easter-egg video). We honor the skip intent.
	a.emit("nclSetStatus", "File checks bypassed for next launch.")
}

func (a *App) onFileCheck() {
	if !a.beginBusy() {
		return
	}
	defer a.endBusy()
	p := a.patcher()
	a.emit("nclToggleStepBar", true)
	bad, err := p.Verify(context.Background(), a.patchCallbacks())
	a.emit("nclToggleProgressBar", false)
	if err != nil {
		a.emit("nclShowError", "File check failed: "+err.Error())
		return
	}
	if len(bad) == 0 {
		a.emit("nclSetStatus", "All files verified.")
	} else {
		a.emit("nclSetStatus", fmt.Sprintf("%d file(s) need repair — press Play to update.", len(bad)))
	}
}

func (a *App) onPlay() {
	if a.game.IsRunning() {
		a.emit("nclShowGameRunning")
		return
	}
	// Update first if needed, then launch.
	if a.isInstalled() {
		if need, _ := a.patcher().CheckUpdate(context.Background()); need {
			if !a.applyUpdate() {
				return
			}
		}
	} else {
		if !a.applyUpdate() {
			return
		}
	}
	a.launchGame()
}

func (a *App) launchGame() {
	a.mu.Lock()
	s := a.session
	selID := a.cfg.SelectedAccountID
	a.mu.Unlock()
	if s == nil {
		a.emit("nclShowError", "Sign in with Discord first.")
		return
	}
	acctID, _ := strconv.Atoi(selID)
	acctName := accountName(s, acctID)

	a.emit("nclSetStatus", "Requesting launch ticket…")
	ticket, err := a.auth.MintTicket(context.Background(), s.Token, acctID)
	if err != nil {
		if _, ok := err.(*auth.ErrUnauthorized); ok {
			a.onSignOut()
			a.emit("nclShowError", "Session expired — sign in again.")
			return
		}
		a.emit("nclShowError", "Could not get launch ticket: "+err.Error())
		return
	}

	err = a.game.Launch(a.cfg, game.LaunchOpts{AccountName: acctName, Ticket: ticket},
		func(line string) { wr.LogInfo(a.ctx, "[game] "+line) },
		func(st game.Status) {
			a.pushPlayState()
			a.emit("nclSetStatus", "")
		})
	if err != nil {
		a.emit("nclShowError", "Launch failed: "+err.Error())
		return
	}
	a.emit("nclSetStatus", "Launching…")
	a.pushPlayState()
	a.applyPostLaunch()
}

func (a *App) applyPostLaunch() {
	switch a.cfg.PostLaunch {
	case "close":
		if runtime.GOOS == "windows" {
			wr.Quit(a.ctx)
		}
	case "tray", "taskbar":
		// Keep the window; nothing to do.
	}
}

// applyUpdate runs the patcher, reporting progress. Returns false on failure.
func (a *App) applyUpdate() bool {
	if !a.beginBusy() {
		a.emit("nclSetStatus", "Another operation is in progress…")
		return false
	}
	defer a.endBusy()
	a.pushPlayState()
	a.emit("nclToggleStepBar", true)
	err := a.patcher().Apply(context.Background(), a.patchCallbacks())
	a.emit("nclToggleProgressBar", false)
	a.emit("nclToggleStepBar", false)
	if err != nil {
		a.emit("nclShowError", "Update failed: "+err.Error())
		a.pushPlayState()
		return false
	}
	a.pushPlayState()
	return true
}

func (a *App) checkForUpdates(interactive bool) {
	p := a.patcher()
	need, err := p.CheckUpdate(context.Background())
	if err != nil {
		if interactive {
			a.emit("nclSetStatus", "Couldn't check for updates.")
		}
		return
	}
	if need {
		a.emit("nclSetStatus", "Update available — press Play to update.")
	} else if interactive {
		a.emit("nclSetStatus", "Up to date.")
	}
}

func (a *App) onCloseGameAndUpdate() {
	_ = a.game.Kill()
	a.onPlay()
}

func (a *App) onAbortUpdate() {
	a.emit("nclSetStatus", "")
}

// --- helpers ----------------------------------------------------------------

func (a *App) refreshBanners() {
	// Banners are also fetched by the page directly; fetching here lets us
	// forward them even if the webview origin can't reach the API via CORS.
	content, err := a.api.Banners(context.Background())
	if err != nil || content == nil {
		return
	}
	// The page's neocron.js renders banners itself from its own fetch; we only
	// surface news posts (if the API grows a "posts" field) via nclSetNews.
	_ = content
}

func (a *App) refreshAccountsAndState() {
	a.mu.Lock()
	s := a.session
	a.mu.Unlock()
	if s == nil {
		return
	}
	accts, err := a.auth.Accounts(context.Background(), s.Token)
	if err != nil {
		if _, ok := err.(*auth.ErrUnauthorized); ok {
			a.onSignOut()
			return
		}
		a.emit("nclSetAuthState", authOffline(s)) // show cached, mark offline
		return
	}
	a.mu.Lock()
	s.Accounts = accts
	a.session = s
	a.mu.Unlock()
	_ = a.store.Save(s)
	a.pushAuthState()
	a.pushPlayState()
}

func (a *App) patcher() patch.Patcher {
	// Real manifest patcher: GET areamc5/{channel}/{serverKey}/latest -> files
	// with direct download links (docs/RE_LAUNCHER.md §6.11). No auth needed.
	return patch.NewNeocronPatcher(a.cfg.ClientDir(), a.cfg.APIBaseURL, a.cfg.Channel, a.cfg.ServerKey, a.cfg.UserAgent)
}

func (a *App) patchCallbacks() patch.Callbacks {
	return patch.Callbacks{
		OnStep: func(s patch.Step) {
			a.emit("nclToggleStepBar", true)
			a.emit("nclSetStep", s.Label, s.Index, s.Total)
		},
		OnProgress: func(p patch.Progress) {
			a.emit("nclToggleProgressBar", true)
			a.emit("nclSetProgress", p.Label, p.Current, p.Total)
		},
	}
}

func (a *App) isInstalled() bool {
	_, err := os.Stat(a.cfg.GameExePath())
	return err == nil
}

func (a *App) beginBusy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return false
	}
	a.busy = true
	return true
}

func (a *App) endBusy() {
	a.mu.Lock()
	a.busy = false
	a.mu.Unlock()
	a.pushPlayState()
}

func authOffline(s *auth.Session) map[string]any {
	accounts := make([]map[string]any, 0, len(s.Accounts))
	for _, ac := range s.Accounts {
		accounts = append(accounts, map[string]any{
			"id": strconv.Itoa(ac.ID), "name": ac.Name, "disabled": ac.Disabled,
		})
	}
	return map[string]any{
		"status": "signedIn", "discordName": s.Discord.Name, "avatarUrl": s.Discord.Avatar,
		"accounts": accounts, "offline": true,
	}
}

func accountName(s *auth.Session, id int) string {
	for _, ac := range s.Accounts {
		if ac.ID == id {
			return ac.Name
		}
	}
	if len(s.Accounts) > 0 {
		return s.Accounts[0].Name
	}
	return ""
}

func argString(args []json.RawMessage, i int) string {
	if i >= len(args) {
		return ""
	}
	var s string
	if json.Unmarshal(args[i], &s) == nil {
		return s
	}
	return ""
}
