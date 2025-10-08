CREATE TABLE IF NOT EXISTS jobs (
    id UUID PRIMARY KEY,
    model_id TEXT NOT NULL,
    version_id TEXT,
    node_id TEXT,
    status TEXT NOT NULL,
    queue_position INT,
    payload JSONB,
    artifacts JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS jobs_status_idx ON jobs (status);

CREATE TABLE IF NOT EXISTS job_events (
    id BIGSERIAL PRIMARY KEY,
    job_id UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS job_events_job_id_idx ON job_events (job_id);
