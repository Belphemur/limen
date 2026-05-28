// faviconUrl uses Google's s2 favicon service to resolve a favicon for
// the root domain behind mcpUrl (e.g. api.github.com -> github.com) so
// vendor icons resolve even when the MCP endpoint lives on a subdomain.
// Returns null when the URL cannot be parsed.
export function faviconUrl(mcpUrl: string): string | null {
  try {
    const host = new URL(mcpUrl).hostname
    if (!host) return null
    const parts = host.split('.').filter(Boolean)
    const root = parts.length >= 2 ? parts.slice(-2).join('.') : host
    return `https://www.google.com/s2/favicons?domain=${encodeURIComponent(root)}&sz=64`
  } catch {
    return null
  }
}

// onFaviconError hides the favicon <img> element on load failure,
// allowing a fallback icon to show through.
export function onFaviconError(ev: Event) {
  ;(ev.target as HTMLImageElement).style.display = 'none'
}
