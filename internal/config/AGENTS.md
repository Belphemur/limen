# AGENTS.md — `internal/config`

## What this package is

The YAML configuration loader for Limen. One job: read a `config.yaml`,
substitute `${ENV_VAR}` references, fill in sensible defaults, return a
`*Config`. Every other package consumes a concrete typed struct from here —
nothing else parses raw config.

## Public surface

| Symbol                                                                                 | Purpose                                                        |
| -------------------------------------------------------------------------------------- | -------------------------------------------------------------- |
| `Config`                                                                               | Top-level aggregate. Add a new section by adding a field here. |
| `ServerConfig` / `DatabaseConfig` / `AuthConfig` / `CodeModeConfig` / `UpstreamConfig` | Section-level structs.                                         |
| `Load(path string) (*Config, error)`                                                   | Parse YAML, apply defaults.                                    |

## Conventions

- YAML keys are `snake_case` (`max_open_conns`, `jwks_url`); Go fields are
  PascalCase. The `yaml:` tag is the only source of truth for the key name.
- Secrets do **not** live in YAML literals. Use `${ENV_VAR}` substitution in
  the YAML and supply the env var at runtime. Never log a `*Config` directly
  (DSNs contain passwords) — redact at the call site.
- Defaults are applied at the bottom of `Load`. Keep them simple — anything
  more nuanced (cross-field validation, dynamic ports) belongs in the
  consumer.

## When to extend

- **Adding a new config section**: append a field to `Config`, define the
  section struct with `yaml:` tags, add defaults in `Load`. Update `.env.example`
  and `config.yaml` in lockstep.
- **Adding a new environment variable**: keep substitution patterns as
  `${NAME}`. Document the variable in `.env.example` with a one-line comment.
- **Phase-driven growth**: Phases 2 (crypto key), 4 (OIDC), 5 (Zitadel mgmt
  PAT), and 11 (production TLS) each add new sections. Resist the temptation
  to merge sections — one struct per concern keeps the surface inspectable.

## What this package is NOT

- Not a secrets manager. Use a real one in production; here we only template
  env vars into strings.
- Not a validator. Out-of-range / empty-required checks live with the
  consumer of the config (`storage.Open` rejects an empty DSN, etc.).
