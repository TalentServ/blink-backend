-- Canonical lifecycle foundation (Blink-owned; wizard JSON remains draft-compatible).

CREATE TABLE IF NOT EXISTS blink_project_aggregate (
    project_id BIGINT PRIMARY KEY REFERENCES project(id) ON DELETE CASCADE,
    revision BIGINT NOT NULL DEFAULT 1,
    wizard_digest TEXT NOT NULL DEFAULT '',
    stage_spine TEXT NOT NULL DEFAULT 'nine-stage-v1',
    eligibility_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    blockers_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    migrated_from_wizard BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS blink_domain_event (
    id BIGSERIAL PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    revision BIGINT NOT NULL,
    event_type TEXT NOT NULL,
    payload_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    actor_email TEXT,
    correlation_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS blink_domain_event_project_revision
    ON blink_domain_event (project_id, revision);

CREATE TABLE IF NOT EXISTS blink_command_run (
    id UUID PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    command_name TEXT NOT NULL,
    idempotency_key TEXT,
    status TEXT NOT NULL,
    expected_revision BIGINT,
    result_json JSONB,
    error_text TEXT,
    actor_email TEXT,
    correlation_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS blink_command_run_idempotency
    ON blink_command_run (project_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS blink_human_decision (
    id UUID PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    decision_kind TEXT NOT NULL,
    bound_digest TEXT NOT NULL,
    bound_revision BIGINT NOT NULL,
    actor_email TEXT NOT NULL,
    payload_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS blink_projection_outbox (
    id BIGSERIAL PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    action_type TEXT NOT NULL,
    payload_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'queued',
    attempts INT NOT NULL DEFAULT 0,
    last_error TEXT,
    correlation_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS blink_projection_outbox_project_status
    ON blink_projection_outbox (project_id, status);

CREATE TABLE IF NOT EXISTS blink_projection_attempt (
    id BIGSERIAL PRIMARY KEY,
    outbox_id BIGINT NOT NULL REFERENCES blink_projection_outbox(id) ON DELETE CASCADE,
    attempt_no INT NOT NULL,
    status TEXT NOT NULL,
    response_json JSONB,
    error_text TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS blink_ai_run (
    id UUID PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    command_name TEXT NOT NULL,
    model TEXT,
    input_digest TEXT,
    output_digest TEXT,
    framework_runtime BOOLEAN NOT NULL DEFAULT TRUE,
    correlation_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS blink_ai_run_project_created
    ON blink_ai_run (project_id, created_at DESC);
