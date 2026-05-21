<script setup lang="ts">
// IDE Configuration — phase 9f.
//
// Hosts the IDE allowlist manager plus copy-paste configuration
// snippets so admins can point each common AI IDE at this tenant's
// MCP gateway URL. Limen is OAuth 2.0 Dynamic Client Registration
// compliant (RFC 7591), so most IDEs only need the gateway URL —
// they'll register themselves on first connect.

import { computed, onMounted, ref } from 'vue'
import { ChevronDown, Copy } from '@lucide/vue'
import { tenantPrefix } from '@limen/shared/session'
import { SuccessModal } from '@limen/shared'
import IDEAllowlistManager from '@/components/IDEAllowlistManager.vue'

// Lightweight JSON / JSONC highlighter. The IDE config snippets in
// this page are small (~10 lines each), so a regex-based tokenizer
// keeps the SPA bundle untouched compared to pulling in highlight.js
// or shiki. Output is sanitized HTML — every character that's not
// inside a generated <span> is escaped via escapeHTML before the
// regex pass.
function escapeHTML(s: string): string {
    return s.replace(/[&<>]/g, (c) =>
        c === '&' ? '&amp;' : c === '<' ? '&lt;' : '&gt;',
    )
}

function highlightJSON(src: string): string {
    const escaped = escapeHTML(src)
    return escaped.replace(
        /(\/\/[^\n]*)|("(?:\\.|[^"\\])*"\s*:)|("(?:\\.|[^"\\])*")|\b(true|false|null)\b|(-?\d+(?:\.\d+)?(?:[eE][+\-]?\d+)?)/g,
        (match, comment, key, str, kw, num) => {
            if (comment !== undefined)
                return `<span class="text-on-surface-variant italic">${comment}</span>`
            if (key !== undefined) {
                const colon = key.match(/\s*:$/)?.[0] ?? ':'
                const head = key.slice(0, key.length - colon.length)
                return `<span class="text-primary">${head}</span>${colon}`
            }
            if (str !== undefined) return `<span class="text-success">${str}</span>`
            if (kw !== undefined) return `<span class="text-warning">${kw}</span>`
            if (num !== undefined) return `<span class="text-warning">${num}</span>`
            return match
        },
    )
}

const mcpUrl = computed(() => {
    const prefix = tenantPrefix() ?? ''
    return `${window.location.origin}${prefix}/mcp`
})

const copied = ref(false)

async function copy(text: string) {
    try {
        await navigator.clipboard.writeText(text)
        copied.value = true
    } catch {
        copied.value = false
    }
}

interface Example {
    key: string
    name: string
    body: string
    language: string
}

const examples = computed<Example[]>(() => {
    const url = mcpUrl.value
    return [
        {
            key: 'cursor',
            name: 'Cursor',
            language: 'json',
            body: `// Settings → Cursor Settings → MCP → Add new MCP server
{
  "mcpServers": {
    "limen": {
      "url": "${url}"
    }
  }
}`,
        },
        {
            key: 'vscode',
            name: 'VS Code (MCP extension)',
            language: 'json',
            body: `// .vscode/mcp.json (workspace) or User Settings → MCP servers
{
  "servers": {
    "limen": {
      "url": "${url}",
      "type": "http"
    }
  }
}`,
        },
        {
            key: 'claude_code',
            name: 'Claude Code',
            language: 'bash',
            body: `# claude.ai/code — Settings → MCP servers → Add
claude mcp add --transport http limen ${url}`,
        },
        {
            key: 'codex',
            name: 'OpenAI Codex CLI',
            language: 'toml',
            body: `# ~/.codex/config.toml
[mcp_servers.limen]
url = "${url}"
transport = "http"`,
        },
        {
            key: 'opencode',
            name: 'OpenCode',
            language: 'json',
            body: `// ~/.config/opencode/config.json
{
  "mcp": {
    "limen": {
      "type": "remote",
      "url": "${url}"
    }
  }
}`,
        },
        {
            key: 'gemini_cli',
            name: 'Gemini CLI',
            language: 'json',
            body: `// ~/.gemini/settings.json
{
  "mcpServers": {
    "limen": {
      "httpUrl": "${url}"
    }
  }
}`,
        },
        {
            key: 'windsurf',
            name: 'Windsurf',
            language: 'json',
            body: `// Settings → Cascade → MCP servers → Edit raw config
{
  "mcpServers": {
    "limen": {
      "serverUrl": "${url}"
    }
  }
}`,
        },
        {
            key: 'cline',
            name: 'Cline (VS Code extension)',
            language: 'json',
            body: `// Cline → MCP Servers → Configure MCP Servers
{
  "mcpServers": {
    "limen": {
      "url": "${url}",
      "transportType": "streamableHttp"
    }
  }
}`,
        },
        {
            key: 'kiro',
            name: 'Kiro',
            language: 'json',
            body: `// .kiro/mcp.json (workspace) or ~/.kiro/mcp.json
{
  "mcpServers": {
    "limen": {
      "url": "${url}",
      "transport": "http"
    }
  }
}`,
        },
    ]
})

onMounted(() => {
    // No data load — IDEAllowlistManager owns its own state.
})
</script>

<template>
    <div class="space-y-stack-lg">
        <header>
            <h1 class="font-display text-3xl font-bold tracking-tight text-on-surface">
                IDE Configuration
            </h1>
            <p class="mt-2 text-sm text-on-surface-variant">
                Pick the AI IDEs your users will connect from, then point them at the
                tenant's MCP gateway URL below. Limen is OAuth 2.0 Dynamic Client
                Registration compliant (RFC 7591), so each IDE registers itself on
                first connect — no static client IDs or secrets to distribute.
            </p>
        </header>

        <!-- Gateway URL -->
        <section aria-labelledby="gateway-url-heading"
            class="space-y-3 rounded-lg border border-outline-variant bg-surface p-6"
            data-testid="section-gateway-url">
            <h2 id="gateway-url-heading" class="text-lg font-semibold text-on-surface">
                Gateway URL
            </h2>
            <p class="text-sm text-on-surface-variant">
                This is the single endpoint every IDE for this tenant connects to.
            </p>
            <div class="flex items-center gap-2">
                <code
                    class="flex-1 rounded border border-outline-variant bg-surface-variant px-3 py-2 font-mono text-sm text-on-surface break-all"
                    data-testid="gateway-url-value">{{ mcpUrl }}</code>
                <button type="button"
                    class="inline-flex items-center gap-1 rounded border border-outline px-3 py-2 text-sm text-on-surface hover:bg-surface-variant"
                    data-testid="gateway-url-copy" @click="copy(mcpUrl)">
                    <Copy class="h-4 w-4" />
                    Copy
                </button>
            </div>
        </section>

        <!-- Allowlist -->
        <section aria-labelledby="allowlist-heading"
            class="space-y-3 rounded-lg border border-outline-variant bg-surface p-6"
            data-testid="section-allowlist">
            <h2 id="allowlist-heading" class="text-lg font-semibold text-on-surface">
                Redirect URI allowlist
            </h2>
            <p class="text-sm text-on-surface-variant">
                Each preset adds the official redirect URIs the IDE will register via
                DCR. The global HTTPS / loopback floor always applies, so you only
                need to enable the IDEs your team actually uses.
            </p>
            <IDEAllowlistManager />
        </section>

        <!-- Examples -->
        <section aria-labelledby="examples-heading"
            class="space-y-3 rounded-lg border border-outline-variant bg-surface p-6"
            data-testid="section-examples">
            <h2 id="examples-heading" class="text-lg font-semibold text-on-surface">
                Configuration examples
            </h2>
            <p class="text-sm text-on-surface-variant">
                Drop these snippets into the matching IDE's config. Field names track
                each IDE's current documentation — check the vendor docs if a release
                renames a key.
            </p>
            <div class="space-y-2">
                <details v-for="ex in examples" :key="ex.key"
                    class="group rounded border border-outline-variant bg-surface"
                    :data-testid="`example-${ex.key}`">
                    <summary
                        class="flex cursor-pointer items-center justify-between gap-2 px-4 py-3 text-sm font-medium text-on-surface select-none">
                        <span class="flex items-center gap-2">
                            <ChevronDown class="h-4 w-4 transition-transform group-open:rotate-180" />
                            {{ ex.name }}
                        </span>
                        <button type="button"
                            class="inline-flex items-center gap-1 rounded border border-outline px-2 py-1 text-xs text-on-surface-variant hover:text-on-surface hover:bg-surface-variant"
                            :data-testid="`example-copy-${ex.key}`"
                            @click.prevent.stop="copy(ex.body)">
                            <Copy class="h-3 w-3" />
                            Copy
                        </button>
                    </summary>
                    <pre v-if="ex.language === 'json'"
                        class="overflow-x-auto border-t border-outline-variant bg-surface-variant px-4 py-3 font-mono text-xs leading-relaxed text-on-surface"
                        :data-testid="`example-body-${ex.key}`"><code v-html="highlightJSON(ex.body)"></code></pre>
                    <pre v-else
                        class="overflow-x-auto border-t border-outline-variant bg-surface-variant px-4 py-3 font-mono text-xs leading-relaxed text-on-surface"
                        :data-testid="`example-body-${ex.key}`"><code>{{ ex.body }}</code></pre>
                </details>
            </div>
        </section>

        <SuccessModal :open="copied" title="Copied"
            message="The configuration was copied to your clipboard."
            @close="copied = false" />
    </div>
</template>
