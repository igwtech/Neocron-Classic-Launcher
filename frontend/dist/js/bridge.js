// bridge.js — adapts the original CEF-targeted UI to the Wails runtime.
//
// The page (launcher.js) was written for the official CEF launcher: it calls
// window.cefQuery({request,onSuccess,onFailure}) to reach native code, and the
// native side pushes state by evaluating window.ncl*(...) in the page. We keep
// launcher.js/neocron.js byte-for-byte and provide both halves here:
//   * window.cefQuery  -> App.Dispatch(request) (the Go dispatcher)
//   * Wails "ncl" event -> window[fn](...args)   (the Go state push)
(function () {
    "use strict";

    // JS -> Go. Mirrors the CEF message router shape exactly.
    window.cefQuery = function (opts) {
        opts = opts || {};
        var invoke = window.go && window.go.main && window.go.main.App
            ? window.go.main.App.Dispatch
            : null;
        if (!invoke) {
            // Not running under Wails (e.g. previewed in a plain browser).
            if (opts.onSuccess) opts.onSuccess("");
            return;
        }
        invoke(opts.request)
            .then(function (res) { if (opts.onSuccess) opts.onSuccess(res); })
            .catch(function (err) {
                if (opts.onFailure) opts.onFailure(-1, String(err));
            });
    };

    // Go -> JS. The backend emits {fn, args}; call the matching window.ncl*.
    function wire() {
        if (!(window.runtime && window.runtime.EventsOn)) {
            return setTimeout(wire, 20);
        }
        window.runtime.EventsOn("ncl", function (msg) {
            if (!msg || typeof msg.fn !== "string") return;
            var fn = window[msg.fn];
            if (typeof fn === "function") {
                try { fn.apply(null, msg.args || []); }
                catch (e) { console.error(msg.fn, e); }
            }
        });
    }
    wire();
})();
