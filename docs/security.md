# Security

This document describes the security model for Limen -- its threat model, isolation guarantees, and operational safeguards.

## Threat Model

Limen sits between LLM clients and upstream MCP servers. The key attack surfaces are:

| Surface | Risk | Mitigation |
|---------|------|------------|
| **LLM client prompts** | Malicious prompts injected into tool arguments could attempt sandbox escape | Goja sandbox isolation -- no host access beyond injected tools |
| **Configuration file** | Secrets embedded in config (tokens, API keys) could be leaked | `${ENV_VAR}` substitution; never commit real tokens to version control |
| **Upstream responses** | Malicious or malformed responses from upstream servers could carry payloads | Tool responses pass through the gateway without modification; validate upstream trust |
| **Sandbox JS code** | LLM-generated JavaScript in Code Mode could attempt resource exhaustion or exfiltration | Goja sandbox blocks filesystem, network, process spawning; execution timeout enforced |

## Goja Sandbox Guarantees

Code Mode runs agent-provided JavaScript inside a [Goja](https://github.com/dop251/goja) runtime with strict isolation:

### Blocked APIs

The following are **not available** inside the sandbox:

| Category | Blocked APIs |
|----------|-------------|
| Filesystem | `os`, `fs`, `path` module equivalents |
| Network | `fetch`, `XMLHttpRequest`, `http`, `net` module equivalents |
| Processes | `child_process`, `exec`, `spawn` equivalents |
| Modules | `require()`, `import` -- no module resolution |
| Go runtime | Direct access to Go heap, goroutines, or reflection |

### Available APIs

Only the `codemode` object is explicitly injected:

- `codemode.tools()` -- enumerate upstream tools
- `codemode.call(name, args)` -- invoke a tool by name
- `codemode.toolName(args)` -- invoke tools as direct methods

Nothing else. The sandbox contains no globals beyond standard JavaScript built-ins (objects, arrays, strings, math, dates).

## Execution Timeout

To prevent infinite loops and resource exhaustion, Goja execution is interrupted via `vm.Interrupt` after a configurable timeout:

```yaml
codemode:
  timeout: 30s    # JS execution limit
  memory: 64MB    # JS heap cap
```

When the timeout fires, the Goja runtime raises an interrupt that immediately halts script execution. The gateway returns an error to the client -- the loop never completes.

## Authentication

Limen validates client requests through JWT Bearer token authentication:

### Current State

The auth middleware in `auth/middleware.go` extracts and validates Bearer tokens from the `Authorization` header. **JWKS (JSON Web Key Set) validation is currently stubbed** and needs real implementation before production use.

### Planned: JWKS Validation

The intended flow:

1. Client sends request with `Authorization: Bearer <token>`
2. Gateway extracts the JWT
3. Gateway fetches the public key from the configured JWKS endpoint
4. Gateway validates the token signature, expiry, and claims
5. Request proceeds only if validation succeeds

### Configuration

```yaml
auth:
  jwks_url: "https://auth.example.com/.well-known/jwks.json"
  required_claims:
    - "aud"
    - "sub"
```

## Secret Management

Secrets in the configuration file use environment variable substitution to avoid committing tokens:

```yaml
upstreams:
  - name: github
    url: https://api.example.com/mcp
    headers:
      Authorization: "Bearer ${GITHUB_TOKEN}"

  - name: jira
    url: https://jira.example.com/mcp
    headers:
      Authorization: "Bearer ${JIRA_API_KEY}"
```

At startup, Limen replaces `${VAR_NAME}` patterns with values from the process environment:

```bash
export GITHUB_TOKEN="ghp_xxxx"
export JIRA_API_KEY="jira_xxxx"
./limen -config config.yaml
```

### Rules

- **Never** commit real tokens or API keys to the repository
- Use `.gitignore` to exclude local config files with embedded secrets
- Use `.env` files for local development (not committed)
- Rotating tokens requires a gateway restart

## Remote Upstreams Only

Limen supports **only remote MCP servers** via the Streamable HTTP transport. Local process execution (stdio transport) is intentionally disabled:

- Eliminates local supply chain risk from arbitrary MCP server binaries
- Forces network-level isolation between Limen and upstream servers
- Enables centralized authentication and monitoring

## What Code Mode CANNOT Do

The Goja sandbox is intentionally narrow. Agent-generated JavaScript in Code Mode:

- **Cannot** read or write files on disk
- **Cannot** make HTTP requests or open network connections
- **Cannot** spawn subprocesses or execute shell commands
- **Cannot** access the Go runtime, memory, or goroutines
- **Cannot** persist state between invocations
- **Cannot** require or import external modules
- **Cannot** exceed configured memory or timeout limits

Code Mode exists solely to orchestrate tool calls across upstream MCP servers. Any computation beyond that must happen in the LLM's context.

## Security Checklist

Before deploying Limen to production:

- [ ] Implement JWKS token validation in `auth/middleware.go`
- [ ] Set strong execution timeout (30s or less)
- [ ] Set memory limit appropriate for workload (64MB default)
- [ ] Rotate all tokens used in `${ENV_VAR}` substitutions
- [ ] Verify no secrets in configuration files under version control
- [ ] Audit upstream server trustworthiness (remote-only means you depend on their security)
- [ ] Review planned DLP scanning for tool response inspection (roadmap item)
