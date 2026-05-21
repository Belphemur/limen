// Per-vendor hints for the upstream defaults_json editor.
//
// The admin SPA pre-fills a few well-known keys depending on the
// MCP URL the user just pasted. Keys are deliberately empty so the
// admin still has to fill them in — we just save them the click of
// typing the key name.

export interface ContextHint {
  // Object shape suggested for defaults_json.
  template: Record<string, string>
  // Short caption shown under the editor explaining why.
  caption: string
}

// hostMatches returns true when the URL's host matches one of the
// provided suffixes (case-insensitive). Returns false on parse errors.
function hostMatches(rawUrl: string, suffixes: string[]): boolean {
  try {
    const host = new URL(rawUrl).host.toLowerCase()
    return suffixes.some((s) => host === s || host.endsWith('.' + s))
  } catch {
    return false
  }
}

export function hintsFor(mcpUrl: string): ContextHint | null {
  if (!mcpUrl) return null
  if (hostMatches(mcpUrl, ['atlassian.com', 'atlassian.net'])) {
    return {
      template: { cloudId: '' },
      caption: 'Atlassian upstreams typically need a cloudId on each tool call.',
    }
  }
  if (hostMatches(mcpUrl, ['github.com'])) {
    return {
      template: { organization_slug: '' },
      caption: 'GitHub upstreams typically need an organization_slug.',
    }
  }
  if (hostMatches(mcpUrl, ['linear.app'])) {
    return {
      template: { workspace_slug: '' },
      caption: 'Linear upstreams typically need a workspace_slug.',
    }
  }
  if (hostMatches(mcpUrl, ['salesforce.com', 'force.com'])) {
    return {
      template: { account_id: '' },
      caption: 'Salesforce upstreams typically need an account_id.',
    }
  }
  return null
}
