-- Codex Cloud Agents: durable session/turn history for OpenAI Agents API.
-- OpenAI sandboxes are deleted after each turn; Neon keeps conversation + file state.

CREATE TABLE IF NOT EXISTS blink_codex_session (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    issue_id TEXT NOT NULL DEFAULT '',
    workflow_kind TEXT NOT NULL CHECK (
        workflow_kind IN (
            'implement-step',
            'implement-batch',
            'pr-review',
            'fix-review-findings'
        )
    ),
    active_openai_session_id TEXT,
    model TEXT NOT NULL DEFAULT 'gpt-6-luna',
    container_size TEXT NOT NULL DEFAULT 'small',
    status TEXT NOT NULL DEFAULT 'idle' CHECK (
        status IN ('idle', 'running', 'completed', 'failed', 'cancelled')
    ),
    history_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    latest_files_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS blink_codex_session_project_idx
    ON blink_codex_session (project_id, updated_at DESC);

CREATE INDEX IF NOT EXISTS blink_codex_session_issue_idx
    ON blink_codex_session (project_id, issue_id, workflow_kind, updated_at DESC);

CREATE TABLE IF NOT EXISTS blink_codex_turn (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    codex_session_id UUID NOT NULL REFERENCES blink_codex_session(id) ON DELETE CASCADE,
    turn_number INT NOT NULL DEFAULT 1,
    openai_session_id TEXT,
    openai_turn_id TEXT,
    idempotency_key TEXT,
    user_prompt TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'running' CHECK (
        status IN ('running', 'completed', 'failed', 'cancelled')
    ),
    output_text TEXT NOT NULL DEFAULT '',
    commands_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    artifacts_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    error_code TEXT,
    error_message TEXT,
    usage_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS blink_codex_turn_idempotency_uidx
    ON blink_codex_turn (idempotency_key)
    WHERE idempotency_key IS NOT NULL AND idempotency_key <> '';

CREATE INDEX IF NOT EXISTS blink_codex_turn_session_idx
    ON blink_codex_turn (codex_session_id, turn_number DESC);
