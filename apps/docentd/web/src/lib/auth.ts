// Auth helper for the docent dashboard.
//
// When docentd is bound to a non-loopback interface it requires a shared-secret
// bearer token on its data endpoints. We capture that token from a one-time
// ?token= query parameter, stash it in sessionStorage (cleared when the tab
// closes), and attach it as an Authorization header on docentd data fetches.
// With no token configured (the loopback default) docentFetch is a plain fetch
// and requests go out unauthenticated exactly as before.
//
// The ?token capture runs at module load. Import this module first (before the
// router) so the token is stashed and stripped from the URL before anything
// else reads window.location.

const KEY = "docent_token";

(function captureToken() {
  try {
    const params = new URLSearchParams(location.search);
    const t = params.get("token");
    if (t) {
      sessionStorage.setItem(KEY, t);
      params.delete("token");
      const qs = params.toString();
      history.replaceState({}, "", location.pathname + (qs ? "?" + qs : "") + location.hash);
    }
  } catch {
    /* sessionStorage / history may be unavailable; fall through to no token */
  }
})();

function token(): string | null {
  try {
    return sessionStorage.getItem(KEY);
  } catch {
    return null;
  }
}

// fetch() that injects the bearer token (when present) without clobbering
// caller-supplied headers.
export function docentFetch(url: string, opts: RequestInit = {}): Promise<Response> {
  const t = token();
  if (!t) return fetch(url, opts);
  const headers = new Headers(opts.headers ?? {});
  if (!headers.has("Authorization")) headers.set("Authorization", "Bearer " + t);
  return fetch(url, { ...opts, headers });
}

export function docentHasToken(): boolean {
  return !!token();
}
