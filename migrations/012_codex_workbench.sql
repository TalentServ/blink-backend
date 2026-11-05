-- Workbench state for the Codex IDE: per-turn files, attachments, and a Neon working tree.

ALTER TABLE blink_codex_session
    ADD COLUMN IF NOT EXISTS base_files_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS staged_files_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS uncommitted_files_json JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE blink_codex_turn
    ADD COLUMN IF NOT EXISTS files_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS attachments_json JSONB NOT NULL DEFAULT '[]'::jsonb;
