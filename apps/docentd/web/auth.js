"use strict";

// Shared auth helper for the docent dashboard pages.
//
// When docentd is bound to a non-loopback interface it requires a shared-secret
// bearer token on its data endpoints. We capture that token from a one-time
// ?token= query parameter, stash it in sessionStorage (cleared when the tab
// closes), and attach it as an Authorization header on docentd data fetches.
// With no token configured (the loopback default) docentFetch is a no-op
// wrapper around fetch and requests go out unauthenticated exactly as before.
//
// Usage: load this BEFORE the page script, then call docentFetch(url, opts)
// instead of fetch(url, opts) for same-origin docentd endpoints. (The local
// docent-wm /focus calls keep using plain fetch.)
(function () {
  const KEY = "docent_token";

  // Capture a one-time ?token=... and persist it, then strip it from the URL so
  // the secret isn't left in the address bar / history.
  try {
    const params = new URLSearchParams(location.search);
    const t = params.get("token");
    if (t) {
      sessionStorage.setItem(KEY, t);
      params.delete("token");
      const qs = params.toString();
      history.replaceState({}, "", location.pathname + (qs ? "?" + qs : "") + location.hash);
    }
  } catch (e) {
    /* sessionStorage / history may be unavailable; fall through to no token */
  }

  function token() {
    try { return sessionStorage.getItem(KEY); } catch (e) { return null; }
  }

  // fetch() that injects the bearer token (when present) without clobbering
  // caller-supplied headers.
  window.docentFetch = function (url, opts) {
    opts = opts || {};
    const t = token();
    if (t) {
      const headers = new Headers(opts.headers || {});
      if (!headers.has("Authorization")) headers.set("Authorization", "Bearer " + t);
      opts = Object.assign({}, opts, { headers: headers });
    }
    return fetch(url, opts);
  };

  window.docentHasToken = function () { return !!token(); };
})();
