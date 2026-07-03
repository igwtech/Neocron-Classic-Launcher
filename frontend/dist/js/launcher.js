// UI logic for the C++ launcher. Replaces the Blazor @code render loop in
// Main.razor: the C++ backend pushes state via the window.ncl* functions below,
// and user actions call the webview-bound backend functions (window.play, etc).
(function () {
    "use strict";

    const state = {
        version: "0.0.0",
        channel: "ncc-pts",
        allowLaunch: false,
        isInstalled: false,
        playLabel: "Install",
        showStepBar: false,
        showProgressBar: false,
        stepLabel: "",
        stepPct: 0,
        progressLabel: "",
        progressPct: 0,
        postLaunch: "close",
        trayAvailable: false,
        closeAllowed: true,
        auth: {
            status: "signedOut",   // signedOut | pending | signedIn
            discordName: "",
            avatarUrl: "",
            accounts: [],          // [{id, name, disabled}]
            selectedId: "",
            offline: false,
        },
    };

    const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun",
                    "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

    function $(id) { return document.getElementById(id); }

    function call(name, ...args) {
        // CEF message router: window.cefQuery posts {fn,args} to the C++ browser
        // process. Guarded so the page still loads in a plain browser.
        return new Promise((resolve) => {
            if (typeof window.cefQuery !== "function") { resolve(); return; }
            window.cefQuery({
                request: JSON.stringify({ fn: name, args: args }),
                onSuccess: (response) => resolve(response),
                onFailure: (code, msg) => { console.error(name, code, msg); resolve(); },
            });
        });
    }

    function formatDate(iso) {
        const d = new Date(iso);
        if (isNaN(d.getTime())) return "";
        return `${MONTHS[d.getMonth()]} ${String(d.getDate()).padStart(2, "0")}, ${d.getFullYear()}`;
    }

    // --- rendering -----------------------------------------------------------

    function renderPlayGroup() {
        const group = $("play-group");
        if (!group) return;
        if (state.allowLaunch && state.isInstalled) {
            // The primary button always posts "play"; the C++ side decides whether
            // that applies a pending update or launches. Only the label changes
            // ("Play" vs "Update") -- never the color or size.
            group.innerHTML = `
                <button type="button" class="btn play-btn" data-action="play">${state.playLabel || "Play"}</button>
                <button type="button" class="btn play-btn dropdown-toggle dropdown-toggle-split"
                        data-bs-toggle="dropdown" aria-expanded="false">
                    <span class="visually-hidden">Toggle Dropdown</span>
                </button>
                <ul class="dropdown-menu">
                    <li><a class="dropdown-item" data-action="checkupdates">Check for Updates</a></li>
                    <li><a class="dropdown-item" data-action="gfx">GFX Options</a></li>
                    <li><a class="dropdown-item" data-action="filecheck">File Check</a></li>
                    <li><a class="dropdown-item" data-action="bypass">Bypass File Checks</a></li>
                </ul>`;
        } else if (state.allowLaunch && !state.isInstalled) {
            group.innerHTML = `<button type="button" class="btn play-btn" data-action="play">Install</button>`;
        } else {
            group.innerHTML = `<button type="button" class="btn play-btn" disabled>${state.playLabel}</button>`;
        }

        group.querySelectorAll("[data-action]").forEach((el) => {
            el.addEventListener("click", () => {
                switch (el.getAttribute("data-action")) {
                    case "play": call("play"); break;
                    case "checkupdates": call("checkForUpdates"); break;
                    case "gfx": call("gfxOptions"); break;
                    case "filecheck": call("fileCheck"); break;
                    case "bypass": call("bypassFileChecks"); break;
                }
            });
        });
    }

    function renderProgress() {
        const col = $("progress-col");
        if (!col) return;
        if (!state.showStepBar) { col.innerHTML = ""; return; }
        const pct = state.showProgressBar ? state.progressPct : state.stepPct;
        const detail = (state.showProgressBar && state.progressLabel)
            ? `<div class="nc-progress-detail">${state.progressLabel}</div>` : "";
        col.innerHTML = `
            <div class="nc-progress-wrap">
                <div class="nc-progress-track">
                    <div class="nc-progress-fill" style="width: ${pct}%;"></div>
                </div>
                <div class="nc-progress-info">
                    <span class="nc-progress-label">${state.stepLabel}</span>
                    <span class="nc-progress-pct">${Math.round(pct)}%</span>
                </div>
                ${detail}
            </div>`;
    }

    function escapeHtml(s) {
        return String(s).replace(/[&<>"']/g, (c) => ({
            "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
        }[c]));
    }

    function renderAuthArea() {
        const area = $("auth-area");
        if (!area) return;
        const a = state.auth;

        if (a.status === "pending") {
            area.innerHTML = `
                <div class="auth-pending">
                    <span class="auth-spinner"></span>
                    <span>Waiting for browser&hellip;</span>
                    <a class="auth-link" data-auth="cancel">Cancel</a>
                </div>`;
        } else if (a.status === "signedIn") {
            const options = a.accounts.length
                ? a.accounts.map((acc) =>
                    `<option value="${escapeHtml(acc.id)}"${acc.disabled ? " disabled" : ""}` +
                    `${acc.id === a.selectedId ? " selected" : ""}>${escapeHtml(acc.name)}</option>`).join("")
                : `<option value="" disabled selected>No linked accounts</option>`;
            area.innerHTML = `
                <div class="auth-user">
                    <span class="auth-name" title="Signed in with Discord">${escapeHtml(a.discordName)}</span>
                    ${a.offline ? `<span class="auth-offline" title="Sign-in service unreachable">offline</span>` : ""}
                    <a class="auth-link" data-auth="signout">Sign out</a>
                </div>
                <select id="account-select" class="channel-select account-select" title="Account used at launch">
                    ${options}
                </select>`;
            const sel = area.querySelector("#account-select");
            if (sel) sel.addEventListener("change", () => call("accountChanged", sel.value));
        } else {
            area.innerHTML = `
                <button type="button" class="btn auth-btn" data-auth="signin">
                    Sign in with Discord
                </button>`;
        }

        area.querySelectorAll("[data-auth]").forEach((el) => {
            el.addEventListener("click", () => {
                switch (el.getAttribute("data-auth")) {
                    case "signin": call("authSignIn"); break;
                    case "cancel": call("authCancel"); break;
                    case "signout": call("authSignOut"); break;
                }
            });
        });
    }

    function renderNews(posts) {
        const sec = $("news-section");
        if (!sec) return;
        if (!posts || !posts.length) { sec.innerHTML = ""; return; }
        sec.innerHTML = posts.map((p) => `
            <div class="col-12 news-post">
                <div class="news-post-header">
                    <span class="news-post-title">${p.title || ""}</span>
                    <span class="news-post-date">${formatDate(p.createdAt || p.created_at)}</span>
                </div>
                <div class="news-post-body">${p.body || ""}</div>
            </div>`).join("");
    }

    // --- backend -> UI bridge (called from C++ via eval) ---------------------

    window.nclSetVersion = function (v) {
        state.version = v;
        const el = $("version-label");
        if (el) el.textContent = "v" + v;
    };
    window.nclSetChannel = function (ch) {
        state.channel = ch;
        const sel = $("channel-select");
        if (sel) sel.value = ch;
    };
    window.nclSetNews = function (posts) { renderNews(posts); };
    window.nclSetPlayState = function (allowLaunch, isInstalled, label) {
        state.allowLaunch = !!allowLaunch;
        state.isInstalled = !!isInstalled;
        state.playLabel = label || (isInstalled ? "Play" : "Install");
        renderPlayGroup();
    };
    window.nclSetStep = function (label, step, steps) {
        state.stepLabel = `Step ${step} of ${steps} - ${label}`;
        state.stepPct = steps > 0 ? (step / steps) * 100 : 0;
        renderProgress();
    };
    window.nclToggleStepBar = function (active) {
        state.showStepBar = !!active;
        renderProgress();
    };
    window.nclSetProgress = function (label, current, total) {
        state.progressLabel = label || "";
        state.progressPct = total > 0 ? (current / total) * 100 : 0;
        renderProgress();
    };
    window.nclToggleProgressBar = function (active) {
        state.showProgressBar = !!active;
        renderProgress();
    };
    window.nclShowError = function (msg) {
        state.stepLabel = "Error: " + msg;
        state.showStepBar = true;
        renderProgress();
    };
    // A short idle status line (e.g. "Update available", "Ready to Launch"),
    // shown in the same area as step labels. Pass "" to clear it.
    window.nclSetStatus = function (msg) {
        state.stepLabel = msg || "";
        state.showStepBar = !!msg;
        state.showProgressBar = false;
        renderProgress();
    };
    // The game-running guard dialog: the backend asks the user to close the game
    // or abort before an update can be applied.
    window.nclShowGameRunning = function () { openGameRunning(); };
    window.nclSetAuthState = function (auth) {
        auth = auth || {};
        state.auth.status = auth.status || "signedOut";
        state.auth.discordName = auth.discordName || "";
        state.auth.avatarUrl = auth.avatarUrl || "";
        state.auth.accounts = Array.isArray(auth.accounts) ? auth.accounts : [];
        state.auth.selectedId = auth.selectedId || "";
        state.auth.offline = !!auth.offline;
        renderAuthArea();
        if (auth.error) window.nclShowError(auth.error);
    };
    window.nclSetOptions = function (opts) {
        opts = opts || {};
        if (typeof opts.postLaunch === "string") state.postLaunch = opts.postLaunch;
        state.trayAvailable = !!opts.trayAvailable;
        state.closeAllowed = opts.closeAllowed !== false;  // default true
        applyOptionsToUi();
    };

    // --- options dialog ------------------------------------------------------

    function applyOptionsToUi() {
        // Tray choice is Windows-only; hide it elsewhere and demote a stale
        // "tray" selection to "taskbar".
        const trayRow = $("option-tray-row");
        if (trayRow) trayRow.style.display = state.trayAvailable ? "" : "none";

        let selected = state.postLaunch;
        if (selected === "tray" && !state.trayAvailable) selected = "taskbar";
        if (selected === "close" && !state.closeAllowed) selected = "taskbar";
        document.querySelectorAll('input[name="postLaunch"]').forEach((el) => {
            el.checked = el.value === selected;
        });

        // Disable "close on launch" under Wine/Proton: closing the launcher
        // there can take the running game down with it.
        const closeInput = document.querySelector('input[name="postLaunch"][value="close"]');
        if (closeInput) {
            closeInput.disabled = !state.closeAllowed;
            const row = closeInput.closest(".option-row");
            if (row) {
                row.classList.toggle("option-disabled", !state.closeAllowed);
                row.title = state.closeAllowed ? "" : "Not available under Proton/Wine";
            }
        }
    }

    function openOptions() {
        applyOptionsToUi();
        const modal = $("options-modal");
        if (modal) modal.style.display = "flex";
    }

    function closeOptions() {
        const modal = $("options-modal");
        if (modal) modal.style.display = "none";
    }

    // --- game-running dialog -------------------------------------------------

    function openGameRunning() {
        const modal = $("game-running-modal");
        if (modal) modal.style.display = "flex";
    }

    function closeGameRunning() {
        const modal = $("game-running-modal");
        if (modal) modal.style.display = "none";
    }

    // --- init ---------------------------------------------------------------

    document.addEventListener("DOMContentLoaded", function () {
        const sel = $("channel-select");
        if (sel) {
            sel.addEventListener("change", function () {
                call("channelChanged", sel.value);
            });
        }

        const gear = $("options-link");
        if (gear) gear.addEventListener("click", openOptions);
        const closeBtn = $("options-close");
        if (closeBtn) closeBtn.addEventListener("click", closeOptions);

        // Game-running dialog buttons: close all copies + update, or abort.
        const grClose = $("game-running-close");
        if (grClose) grClose.addEventListener("click", function () {
            closeGameRunning();
            call("closeGameAndUpdate");
        });
        const grAbort = $("game-running-abort");
        if (grAbort) grAbort.addEventListener("click", function () {
            closeGameRunning();
            call("abortUpdate");
        });
        const overlay = $("options-modal");
        if (overlay) {
            // Click outside the box closes the dialog.
            overlay.addEventListener("click", function (e) {
                if (e.target === overlay) closeOptions();
            });
        }
        document.querySelectorAll('input[name="postLaunch"]').forEach((el) => {
            el.addEventListener("change", function () {
                if (!el.checked) return;
                state.postLaunch = el.value;
                call("setOption", "postLaunch", el.value);
            });
        });

        renderPlayGroup();
        renderAuthArea();
        // Tell the backend the UI is ready; it pushes version/channel/news/state.
        call("uiReady");
    });
})();
