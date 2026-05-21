<script setup lang="ts">
// IDEExamples — copy-paste MCP configuration snippets for the
// common AI IDEs.
//
// Shared between the admin SPA (IDE Configuration page) and the
// portal SPA (user dashboard). The host page passes in the MCP
// gateway URL — the component owns the snippet catalog, the JSON
// highlighter, and the per-snippet copy buttons.

import { computed, ref } from 'vue'
import { ChevronDown, Copy } from '@lucide/vue'
import SuccessModal from './SuccessModal.vue'

const props = defineProps<{
    mcpUrl: string
}>()

// Lightweight JSON / JSONC highlighter. Snippets are tiny so a
// regex tokenizer keeps the SPA bundle untouched compared to
// pulling in highlight.js or shiki. The source is HTML-escaped
// before the regex pass so injected markup cannot reach the DOM.
function escapeHTML(s: string): string {
    return s.replace(/[&<>]/g, (c) => (c === '&' ? '&amp;' : c === '<' ? '&lt;' : '&gt;'))
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

interface Example {
    key: string
    name: string
    body: string
    language: string
}

const examples = computed<Example[]>(() => {
    const url = props.mcpUrl
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

const copied = ref(false)

async function copy(text: string) {
    try {
        await navigator.clipboard.writeText(text)
        copied.value = true
    } catch {
        copied.value = false
    }
}
</script>

<template>
    <div class="space-y-2">
        <details v-for="ex in examples" :key="ex.key" class="group rounded border border-outline-variant bg-surface"
            :data-testid="`example-${ex.key}`">
            <summary
                class="flex cursor-pointer items-center justify-between gap-2 px-4 py-3 text-sm font-medium text-on-surface select-none">
                <span class="flex items-center gap-2">
                    <ChevronDown class="h-4 w-4 transition-transform group-open:rotate-180" />
                    {{ ex.name }}
                </span>
                <button type="button"
                    class="inline-flex items-center gap-1 rounded border border-outline px-2 py-1 text-xs text-on-surface-variant hover:text-on-surface hover:bg-surface-variant"
                    :data-testid="`example-copy-${ex.key}`" @click.prevent.stop="copy(ex.body)">
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

        <SuccessModal :open="copied" title="Copied" message="The configuration was copied to your clipboard."
            @close="copied = false" />
    </div>
</template>
