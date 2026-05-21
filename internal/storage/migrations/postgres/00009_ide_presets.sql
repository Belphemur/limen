-- +goose Up
-- +goose StatementBegin
-- Phase 9f — IDE presets catalog.
--
-- ide_presets / ide_preset_patterns are created by GORM AutoMigrate from
-- storage.IDEPreset + storage.IDEPresetPattern. Both tables are GLOBAL:
-- no RLS, every tenant reads the same catalog. This migration owns the
-- runtime SELECT grant + seed data only.
--
-- Re-applying this migration is safe: every INSERT uses
-- ON CONFLICT (...) DO UPDATE so admin renames / sort_order tweaks land
-- cleanly without destroying linked tenant_redirect_uri_allowlist rows
-- (FK is ON DELETE SET NULL).

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'limen_app') THEN
        EXECUTE 'GRANT SELECT ON ide_presets TO limen_app';
        EXECUTE 'GRANT SELECT ON ide_preset_patterns TO limen_app';
        EXECUTE 'GRANT USAGE, SELECT ON SEQUENCE ide_preset_patterns_id_seq TO limen_app';
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
-- Dedup constraint that GORM cannot express via tag without surprise.
-- Must exist BEFORE the ON CONFLICT (ide_key, pattern) seed below.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'ide_preset_patterns_ide_key_pattern_key'
    ) THEN
        ALTER TABLE ide_preset_patterns
            ADD CONSTRAINT ide_preset_patterns_ide_key_pattern_key UNIQUE (ide_key, pattern);
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
-- Seed: well-known MCP-client IDEs, with their officially-documented
-- DCR redirect URIs. See phase-09f for the source table.
INSERT INTO
    ide_presets (
        key,
        display_name,
        icon,
        sort_order
    )
VALUES (
        'cursor',
        'Cursor',
        'terminal',
        10
    ),
    (
        'vscode',
        'VS Code',
        'code',
        20
    ),
    (
        'claude_code',
        'Claude Code',
        'bot',
        30
    ),
    (
        'codex',
        'OpenAI Codex',
        'brain',
        40
    ),
    (
        'opencode',
        'OpenCode',
        'package',
        50
    ),
    (
        'gemini_cli',
        'Gemini CLI',
        'sparkles',
        60
    ),
    (
        'windsurf',
        'Windsurf',
        'wind',
        70
    ),
    (
        'cline',
        'Cline (VS Code ext)',
        'code-2',
        80
    ),
    ('kiro', 'Kiro', 'monitor', 90)
ON CONFLICT (key) DO
UPDATE
SET
    display_name = EXCLUDED.display_name,
    icon = EXCLUDED.icon,
    sort_order = EXCLUDED.sort_order,
    updated_at = now();
-- +goose StatementEnd

-- +goose StatementBegin
INSERT INTO
    ide_preset_patterns (ide_key, pattern, sort_order)
VALUES (
        'cursor',
        'cursor://anysphere.cursor-mcp/oauth/callback',
        0
    ),
    (
        'cursor',
        'http://127.0.0.1:54321/callback',
        1
    ),
    (
        'vscode',
        'http://127.0.0.1:33418',
        0
    ),
    (
        'vscode',
        'https://vscode.dev/redirect',
        1
    ),
    (
        'claude_code',
        'http://localhost:*/callback',
        0
    ),
    (
        'claude_code',
        'http://127.0.0.1:*/callback',
        1
    ),
    (
        'codex',
        'http://localhost:1455/auth/callback',
        0
    ),
    (
        'opencode',
        'http://127.0.0.1:19876/mcp/oauth/callback',
        0
    ),
    (
        'gemini_cli',
        'http://localhost:*/oauth/callback',
        0
    ),
    (
        'windsurf',
        'http://localhost:*/callback',
        0
    ),
    (
        'cline',
        'vscode://saoudrizwan.claude-dev/mcp-auth/callback/*',
        0
    ),
    (
        'kiro',
        'http://localhost:*/callback',
        0
    )
ON CONFLICT (ide_key, pattern) DO
UPDATE
SET
    sort_order = EXCLUDED.sort_order;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM ide_preset_patterns;

DELETE FROM ide_presets;

ALTER TABLE IF EXISTS ide_preset_patterns
DROP CONSTRAINT IF EXISTS ide_preset_patterns_ide_key_pattern_key;
-- +goose StatementEnd