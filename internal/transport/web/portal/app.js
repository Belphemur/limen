// Limen Portal POC — pulls /portal/me and renders the live ID-token claims.
//
// Path layout: this page is served at /t/{slug}/portal/. Every API call is
// relative ("me") so the tenant prefix follows the URL and the portal
// cookie (scoped Path=/t/{slug}) is sent automatically.

(() => {
  const tenantPrefix = window.location.pathname.replace(/\/portal\/?.*$/, "");
  const slug = tenantPrefix.replace(/^\/t\//, "");

  document.getElementById("slug").textContent = slug || "(unknown)";
  document.getElementById("logout").href = `${tenantPrefix}/auth/logout`;
  document.getElementById("login").href = `${tenantPrefix}/auth/login`;

  const $loading = document.getElementById("loading");
  const $signedIn = document.getElementById("signed-in");
  const $error = document.getElementById("error");

  function show(section) {
    for (const el of [$loading, $signedIn, $error]) el.hidden = el !== section;
  }

  function fmtExp(unix) {
    if (!unix) return "(none)";
    const d = new Date(unix * 1000);
    const delta = Math.round((d - Date.now()) / 1000);
    return `${d.toISOString()} (${delta >= 0 ? "+" : ""}${delta}s)`;
  }

  async function fetchMe() {
    show($loading);
    try {
      const r = await fetch("me", { credentials: "same-origin" });
      if (r.status === 401 || r.status === 302 || r.redirected) {
        document.getElementById("error-msg").textContent =
          "Not signed in (or session expired).";
        show($error);
        return;
      }
      if (!r.ok) {
        document.getElementById("error-msg").textContent =
          `me failed: HTTP ${r.status}`;
        show($error);
        return;
      }
      const me = await r.json();
      document.getElementById("sub").textContent = me.sub || "";
      document.getElementById("email").textContent = me.email || "";
      document.getElementById("name").textContent = me.name || "";
      document.getElementById("roles").textContent =
        (me.roles && me.roles.length ? me.roles.join(", ") : "(none)");
      document.getElementById("exp").textContent = fmtExp(me.exp);
      document.getElementById("raw").textContent = JSON.stringify(me, null, 2);
      show($signedIn);
    } catch (e) {
      document.getElementById("error-msg").textContent = String(e);
      show($error);
    }
  }

  document.getElementById("refresh").addEventListener("click", fetchMe);
  fetchMe();
})();
