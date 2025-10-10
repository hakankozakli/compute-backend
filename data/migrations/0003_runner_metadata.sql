CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

ALTER TABLE model_versions
    ADD COLUMN IF NOT EXISTS runner_family TEXT,
    ADD COLUMN IF NOT EXISTS runner_manifest JSONB DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS default_parameters JSONB DEFAULT '{}'::jsonb;

CREATE TABLE IF NOT EXISTS node_runner_assignments (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    node_id TEXT NOT NULL,
    model_version_id UUID NOT NULL REFERENCES model_versions(id) ON DELETE CASCADE,
    runner_image TEXT NOT NULL,
    parameter_overrides JSONB DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS node_runner_assignments_node_model_idx
    ON node_runner_assignments (node_id, model_version_id);
