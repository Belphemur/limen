// Helper for the mcp_spec OAuth popup flow used by Admin/Portal SPAs.
//
// The caller opens the upstream authorize URL in a popup. The popup
// eventually lands on a tenant-scoped `/oauth-popup-close` page that
// reads the result from its own URL params and posts a message back
// to the opener (see `openOAuthPopup` consumer + `OAuthPopupClose.vue`).
//
// Outcomes:
//   - { ok: true }                       — the link was persisted.
//   - { ok: false, error: 'cancelled' }  — the user closed the popup
//                                           before completing the flow.
//   - { ok: false, error: '<code>' }     — the backend / AS returned
//                                           an error; `errorDescription`
//                                           may carry a human message.

export const OAUTH_POPUP_MESSAGE_TYPE = "limen-upstream-oauth-result" as const;
export const OAUTH_POPUP_BROADCAST_CHANNEL = "limen-upstream-oauth" as const;

export interface OAuthPopupResultMessage {
  type: typeof OAUTH_POPUP_MESSAGE_TYPE;
  ok: boolean;
  error?: string;
  errorDescription?: string;
}

export interface OAuthPopupResult {
  ok: boolean;
  error?: string;
  errorDescription?: string;
}

export interface OpenOAuthPopupOptions {
  url: string;
  // Window name; reuse to coalesce duplicate clicks.
  name?: string;
  width?: number;
  height?: number;
}

export function openOAuthPopup(
  opts: OpenOAuthPopupOptions,
): Promise<OAuthPopupResult> {
  const width = opts.width ?? 600;
  const height = opts.height ?? 720;
  const left =
    typeof window !== "undefined" && window.screen
      ? Math.max(0, Math.floor((window.screen.width - width) / 2))
      : 0;
  const top =
    typeof window !== "undefined" && window.screen
      ? Math.max(0, Math.floor((window.screen.height - height) / 2))
      : 0;
  const features = [
    `width=${width}`,
    `height=${height}`,
    `left=${left}`,
    `top=${top}`,
    "resizable=yes",
    "scrollbars=yes",
    "status=no",
    "toolbar=no",
    "menubar=no",
    "location=yes",
  ].join(",");

  const popup = window.open(
    opts.url,
    opts.name ?? "limen-upstream-oauth",
    features,
  );
  if (!popup) {
    return Promise.resolve({
      ok: false,
      error: "popup_blocked",
      errorDescription:
        "The OAuth popup was blocked by the browser. Please allow popups for this site and try again.",
    });
  }
  const opened: Window = popup;

  return new Promise<OAuthPopupResult>((resolve) => {
    let settled = false;
    const origin = window.location.origin;
    const channel =
      typeof BroadcastChannel !== "undefined"
        ? new BroadcastChannel(OAUTH_POPUP_BROADCAST_CHANNEL)
        : null;

    const handle = (data: Partial<OAuthPopupResultMessage> | undefined) => {
      if (!data || data.type !== OAUTH_POPUP_MESSAGE_TYPE) return;
      finish({
        ok: data.ok === true,
        error: data.error,
        errorDescription: data.errorDescription,
      });
    };

    const onMessage = (ev: MessageEvent) => {
      if (ev.origin !== origin) return;
      handle(ev.data as Partial<OAuthPopupResultMessage> | undefined);
    };
    const onChannel = (ev: MessageEvent) => {
      handle(ev.data as Partial<OAuthPopupResultMessage> | undefined);
    };

    const poll = window.setInterval(() => {
      if (opened.closed) {
        finish({
          ok: false,
          error: "cancelled",
          errorDescription:
            "OAuth window was closed before the flow completed.",
        });
      }
    }, 500);

    function finish(result: OAuthPopupResult) {
      if (settled) return;
      settled = true;
      window.clearInterval(poll);
      window.removeEventListener("message", onMessage);
      if (channel) {
        channel.removeEventListener("message", onChannel);
        try {
          channel.close();
        } catch {
          // Ignore — channel may already be closed.
        }
      }
      if (!opened.closed) {
        try {
          opened.close();
        } catch {
          // Ignore — cross-origin popups can't always be closed.
        }
      }
      resolve(result);
    }

    window.addEventListener("message", onMessage);
    if (channel) channel.addEventListener("message", onChannel);
  });
}

// Read OAuth result params injected by the backend callback and post
// the message to the opener, then close the popup window. Returns the
// parsed result so the popup page can also render a static fallback.
export function postOAuthPopupResultAndClose(): OAuthPopupResult {
  const params = new URLSearchParams(window.location.search);
  const errorCode = params.get("upstream_oauth_error");
  const result: OAuthPopupResult = errorCode
    ? {
        ok: false,
        error: errorCode,
        errorDescription:
          params.get("upstream_oauth_error_description") ?? undefined,
      }
    : { ok: true };

  const msg: OAuthPopupResultMessage = {
    type: OAUTH_POPUP_MESSAGE_TYPE,
    ok: result.ok,
    error: result.error,
    errorDescription: result.errorDescription,
  };
  try {
    if (window.opener && !window.opener.closed) {
      window.opener.postMessage(msg, window.location.origin);
    }
  } catch {
    // Ignore — cross-origin opener may reject postMessage.
  }
  try {
    if (typeof BroadcastChannel !== "undefined") {
      const ch = new BroadcastChannel(OAUTH_POPUP_BROADCAST_CHANNEL);
      ch.postMessage(msg);
      ch.close();
    }
  } catch {
    // Ignore — BroadcastChannel may be unavailable in some browsers.
  }
  // Schedule close after the message has had a chance to flush.
  window.setTimeout(() => {
    try {
      window.close();
    } catch {
      // Ignore — only popups can be closed.
    }
  }, 100);
  return result;
}
