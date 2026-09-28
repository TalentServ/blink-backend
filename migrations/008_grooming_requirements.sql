-- Project grooming session and requirement revision history (Blink canonical).

CREATE TABLE IF NOT EXISTS blink_grooming_session (
    project_id BIGINT PRIMARY KEY REFERENCES project(id) ON DELETE CASCADE,
    revision BIGINT NOT NULL DEFAULT 1,
    session_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    readiness_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    session_digest TEXT NOT NULL DEFAULT '',
    groom_confirmed_digest TEXT,
    confirmed_by TEXT,
    confirmed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS blink_requirement_revision (
    id BIGSERIAL PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    revision_no INT NOT NULL,
    view_kind TEXT NOT NULL DEFAULT 'source',
    content_digest TEXT NOT NULL DEFAULT '',
    payload_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    superseded_by BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS blink_requirement_revision_project_no
    ON blink_requirement_revision (project_id, revision_no, view_kind);

CREATE TABLE IF NOT EXISTS blink_groom_gate (
    project_id BIGINT PRIMARY KEY REFERENCES project(id) ON DELETE CASCADE,
    session_digest TEXT NOT NULL DEFAULT '',
    confirmed_digest TEXT,
    confirmed_by TEXT,
    confirmed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
