-- Governed command domains. Backend owns these facts, confirmations, leases,
-- and provider reconciliation records; Framework can only return proposals.

ALTER TABLE blink_grooming_session
    ADD COLUMN IF NOT EXISTS inherited_context_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS invalidated_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS invalidation_reason TEXT;

CREATE TABLE IF NOT EXISTS blink_grooming_question (
    id UUID PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    question_key TEXT NOT NULL,
    prompt TEXT NOT NULL,
    mandatory BOOLEAN NOT NULL DEFAULT FALSE,
    assigned_role_id TEXT,
    status TEXT NOT NULL DEFAULT 'open',
    source_digest TEXT NOT NULL DEFAULT '',
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS blink_grooming_question_project_key
    ON blink_grooming_question (project_id, question_key);

CREATE TABLE IF NOT EXISTS blink_grooming_answer (
    id UUID PRIMARY KEY,
    question_id UUID NOT NULL REFERENCES blink_grooming_question(id) ON DELETE CASCADE,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    answer_text TEXT NOT NULL,
    answer_status TEXT NOT NULL DEFAULT 'answered',
    evidence_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    answer_digest TEXT NOT NULL,
    answered_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    superseded_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS blink_grooming_answer_question_current
    ON blink_grooming_answer (question_id, created_at DESC) WHERE superseded_at IS NULL;

CREATE TABLE IF NOT EXISTS blink_grooming_decision (
    id UUID PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    decision_key TEXT NOT NULL,
    decision_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    decision_digest TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'accepted',
    decided_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    invalidated_at TIMESTAMPTZ,
    invalidation_reason TEXT
);
CREATE INDEX IF NOT EXISTS blink_grooming_decision_project_current
    ON blink_grooming_decision (project_id, decision_key, created_at DESC) WHERE invalidated_at IS NULL;

CREATE TABLE IF NOT EXISTS blink_grooming_blocker (
    id UUID PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    blocker_key TEXT NOT NULL,
    severity TEXT NOT NULL DEFAULT 'blocking',
    message TEXT NOT NULL,
    source_digest TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'open',
    details_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS blink_grooming_blocker_project_key_open
    ON blink_grooming_blocker (project_id, blocker_key) WHERE resolved_at IS NULL;

CREATE TABLE IF NOT EXISTS blink_grooming_context (
    id UUID PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    context_key TEXT NOT NULL,
    context_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    context_digest TEXT NOT NULL,
    inherited_from_project_id BIGINT REFERENCES project(id) ON DELETE SET NULL,
    inherited_from_revision BIGINT,
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    invalidated_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS blink_grooming_context_project_key_current
    ON blink_grooming_context (project_id, context_key) WHERE invalidated_at IS NULL;

CREATE TABLE IF NOT EXISTS blink_grooming_revision (
    id BIGSERIAL PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    revision_no BIGINT NOT NULL,
    reason TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    payload_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS blink_grooming_revision_project_no
    ON blink_grooming_revision (project_id, revision_no);

ALTER TABLE blink_dependency_graph
    ADD COLUMN IF NOT EXISTS source_kind TEXT NOT NULL DEFAULT 'wizard-derived',
    ADD COLUMN IF NOT EXISTS source_run_id TEXT,
    ADD COLUMN IF NOT EXISTS adopted_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS invalidated_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS invalidation_reason TEXT;

CREATE TABLE IF NOT EXISTS blink_framework_graph_adoption (
    id UUID PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    framework_run_id TEXT,
    proposal_digest TEXT NOT NULL,
    accepted_digest TEXT NOT NULL,
    proposal_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    adopted_by TEXT NOT NULL,
    adopted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    invalidated_at TIMESTAMPTZ,
    invalidation_reason TEXT
);
CREATE INDEX IF NOT EXISTS blink_framework_graph_adoption_project_current
    ON blink_framework_graph_adoption (project_id, adopted_at DESC) WHERE invalidated_at IS NULL;

CREATE TABLE IF NOT EXISTS blink_architecture_snapshot (
    project_id BIGINT PRIMARY KEY REFERENCES project(id) ON DELETE CASCADE,
    revision BIGINT NOT NULL DEFAULT 1,
    architecture_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    architecture_digest TEXT NOT NULL DEFAULT '',
    confirmed_digest TEXT,
    confirmed_by TEXT,
    confirmed_at TIMESTAMPTZ,
    invalidated_at TIMESTAMPTZ,
    invalidation_reason TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS blink_architecture_pin (
    id UUID PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    pin_key TEXT NOT NULL,
    pin_value_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    pin_digest TEXT NOT NULL,
    pinned_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    invalidated_at TIMESTAMPTZ,
    invalidation_reason TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS blink_architecture_pin_project_key_current
    ON blink_architecture_pin (project_id, pin_key) WHERE invalidated_at IS NULL;

CREATE SEQUENCE IF NOT EXISTS blink_execution_fencing_seq;
CREATE TABLE IF NOT EXISTS blink_execution_scope (
    id UUID PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    scope_kind TEXT NOT NULL,
    scope_ref TEXT NOT NULL DEFAULT '',
    scope_digest TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    recovery_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS blink_execution_scope_project_open
    ON blink_execution_scope (project_id, scope_kind, scope_ref) WHERE status IN ('active', 'recovering');

CREATE TABLE IF NOT EXISTS blink_execution_lease (
    id UUID PRIMARY KEY,
    scope_id UUID NOT NULL REFERENCES blink_execution_scope(id) ON DELETE CASCADE,
    holder TEXT NOT NULL,
    fencing_token BIGINT NOT NULL DEFAULT nextval('blink_execution_fencing_seq'),
    status TEXT NOT NULL DEFAULT 'active',
    lease_expires_at TIMESTAMPTZ NOT NULL,
    acquired_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    released_at TIMESTAMPTZ,
    recovery_of UUID REFERENCES blink_execution_lease(id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS blink_execution_lease_scope_active
    ON blink_execution_lease (scope_id) WHERE status = 'active';

CREATE TABLE IF NOT EXISTS blink_execution_evidence (
    id UUID PRIMARY KEY,
    scope_id UUID NOT NULL REFERENCES blink_execution_scope(id) ON DELETE CASCADE,
    lease_id UUID REFERENCES blink_execution_lease(id) ON DELETE SET NULL,
    fencing_token BIGINT NOT NULL,
    evidence_kind TEXT NOT NULL,
    evidence_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    evidence_digest TEXT NOT NULL,
    recorded_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS blink_execution_evidence_scope_created
    ON blink_execution_evidence (scope_id, created_at DESC);

CREATE TABLE IF NOT EXISTS blink_provider_reconciliation (
    id BIGSERIAL PRIMARY KEY,
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    resource_key TEXT NOT NULL,
    desired_digest TEXT NOT NULL DEFAULT '',
    observed_digest TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    backend_write BOOLEAN NOT NULL DEFAULT TRUE,
    last_outbox_id BIGINT REFERENCES blink_projection_outbox(id) ON DELETE SET NULL,
    details_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_reconciled_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS blink_provider_reconciliation_resource
    ON blink_provider_reconciliation (project_id, provider, resource_key);
