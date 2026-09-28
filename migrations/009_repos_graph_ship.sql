-- Repository roster, technology confirmations, dependency graph, ship execution (Blink canonical).

CREATE TABLE IF NOT EXISTS blink_repository_roster (
    project_id BIGINT PRIMARY KEY REFERENCES project(id) ON DELETE CASCADE,
    revision BIGINT NOT NULL DEFAULT 1,
    topology TEXT NOT NULL DEFAULT '',
    roster_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    roster_digest TEXT NOT NULL DEFAULT '',
    confirmed_digest TEXT,
    confirmed_by TEXT,
    confirmed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS blink_repo_technology (
    id BIGSERIAL PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    repo_id TEXT NOT NULL,
    recommendation_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    recommendation_digest TEXT NOT NULL DEFAULT '',
    confirmed_digest TEXT,
    confirmed_by TEXT,
    confirmed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS blink_repo_technology_project_repo
    ON blink_repo_technology (project_id, repo_id);

CREATE TABLE IF NOT EXISTS blink_dependency_graph (
    project_id BIGINT PRIMARY KEY REFERENCES project(id) ON DELETE CASCADE,
    revision BIGINT NOT NULL DEFAULT 1,
    requirement_edges_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    technical_edges_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    effective_edges_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    graph_digest TEXT NOT NULL DEFAULT '',
    cycles_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    frontier_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    execution_order_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS blink_ship_session (
    id UUID PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    scope_kind TEXT NOT NULL DEFAULT 'PROJECT',
    scope_ref TEXT,
    status TEXT NOT NULL DEFAULT 'active',
    checkpoint_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    substage TEXT NOT NULL DEFAULT 'workspace',
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS blink_ship_session_project_status
    ON blink_ship_session (project_id, status);

CREATE TABLE IF NOT EXISTS blink_ship_step (
    id BIGSERIAL PRIMARY KEY,
    session_id UUID NOT NULL REFERENCES blink_ship_session(id) ON DELETE CASCADE,
    step_kind TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    payload_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    result_json JSONB,
    idempotency_key TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS blink_ship_step_idempotency
    ON blink_ship_step (session_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
