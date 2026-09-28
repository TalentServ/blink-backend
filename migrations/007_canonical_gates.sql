-- Canonical gate snapshots (Blink-owned; agents remain in Framework runtime).

CREATE TABLE IF NOT EXISTS blink_stakeholder_registry (
    project_id BIGINT PRIMARY KEY REFERENCES project(id) ON DELETE CASCADE,
    revision BIGINT NOT NULL DEFAULT 1,
    assignments_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    registry_digest TEXT NOT NULL DEFAULT '',
    confirmed_revision BIGINT,
    confirmed_digest TEXT,
    confirmed_by TEXT,
    confirmed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS blink_product_scope_gate (
    project_id BIGINT PRIMARY KEY REFERENCES project(id) ON DELETE CASCADE,
    source_digest TEXT NOT NULL DEFAULT '',
    scope_digest TEXT NOT NULL DEFAULT '',
    confirmed_digest TEXT,
    confirmed_by TEXT,
    confirmed_at TIMESTAMPTZ,
    sdlc_start_issue_id TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS blink_shape_gate (
    project_id BIGINT PRIMARY KEY REFERENCES project(id) ON DELETE CASCADE,
    shape_digest TEXT NOT NULL DEFAULT '',
    confirmed_digest TEXT,
    confirmed_by TEXT,
    confirmed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS blink_work_plan_gate (
    project_id BIGINT PRIMARY KEY REFERENCES project(id) ON DELETE CASCADE,
    package_digest TEXT NOT NULL DEFAULT '',
    confirmed_digest TEXT,
    confirmed_by TEXT,
    confirmed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
