CREATE TABLE IF NOT EXISTS models (
    id UUID PRIMARY KEY,
    external_id TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    family TEXT NOT NULL,
    description TEXT DEFAULT '',
    metadata JSONB DEFAULT '{}'::jsonb,
    default_version_id UUID,
    status TEXT NOT NULL DEFAULT 'INACTIVE',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS model_versions (
    id UUID PRIMARY KEY,
    model_id UUID NOT NULL REFERENCES models(id) ON DELETE CASCADE,
    version TEXT NOT NULL,
    runner_image TEXT NOT NULL,
    weights_uri TEXT,
    min_gpu_mem_gb INTEGER,
    min_vram_gb INTEGER,
    max_batch_size INTEGER,
    parameters JSONB DEFAULT '{}'::jsonb,
    capabilities TEXT[] DEFAULT ARRAY[]::TEXT[],
    latency_tier TEXT,
    artifact_uri TEXT,
    status TEXT NOT NULL DEFAULT 'DRAFT',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(model_id, version)
);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'models_default_version_fk'
    ) THEN
        ALTER TABLE models
            ADD CONSTRAINT models_default_version_fk
            FOREIGN KEY (default_version_id)
            REFERENCES model_versions(id)
            ON DELETE SET NULL;
    END IF;
END $$;
