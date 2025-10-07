use std::{collections::HashMap, sync::Arc, time::Duration};

use anyhow::Context;
use chrono::{DateTime, Utc};
use redis::AsyncCommands;
use serde::{Deserialize, Serialize};
use tokio::sync::{RwLock, watch};
use tokio_stream::wrappers::WatchStream;
use tokio_stream::StreamExt;
use uuid::Uuid;

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum JobStatus {
    InQueue,
    InProgress,
    Completed,
    Error,
    Canceled,
}

#[derive(Debug, Clone, Serialize)]
pub struct JobSnapshot {
    pub request_id: Uuid,
    pub model_id: String,
    pub status: JobStatus,
    pub queue_position: Option<usize>,
    pub logs: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<serde_json::Value>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct SubmitJobRequest {
    pub model_id: String,
    #[serde(default)]
    pub payload: serde_json::Value,
    #[serde(default)]
    pub store_io: bool,
    #[serde(default)]
    pub fal_webhook: Option<String>,
}

#[derive(Debug, Serialize)]
pub struct SubmitJobResponse {
    pub request_id: Uuid,
}

#[derive(Debug, Serialize)]
pub struct JobDetailsResponse {
    pub request_id: Uuid,
    pub model_id: String,
    pub status: JobStatus,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub queue_position: Option<usize>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub logs: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<serde_json::Value>,
}

#[derive(Debug, Serialize)]
pub struct CancelResponse {
    pub status: CancelStatus,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum CancelStatus {
    CancellationRequested,
    AlreadyCompleted,
    TooLate,
    NotFound,
}

#[derive(Debug, Deserialize)]
pub struct SyncRequest {
    pub model_id: String,
    #[serde(default)]
    pub payload: serde_json::Value,
}

#[derive(Debug, Serialize)]
pub struct SyncResponse {
    pub result: serde_json::Value,
    pub logs: Vec<String>,
}

#[derive(Clone)]
pub struct AppState {
    inner: Arc<StateInner>,
}

struct StateInner {
    jobs: RwLock<HashMap<Uuid, JobRecord>>,
    redis: redis::Client,
}

struct JobRecord {
    data: JobData,
    notifier: watch::Sender<JobSnapshot>,
}

#[derive(Debug, Clone)]
struct JobData {
    request_id: Uuid,
    model_id: String,
    payload: serde_json::Value,
    status: JobStatus,
    result: Option<serde_json::Value>,
    logs: Vec<String>,
    created_at: DateTime<Utc>,
    updated_at: DateTime<Utc>,
    store_io: bool,
    fal_webhook: Option<String>,
}

impl AppState {
    pub fn new(redis_url: Option<String>) -> anyhow::Result<Self> {
        let redis_url = redis_url.unwrap_or_else(|| "redis://redis:6379".to_string());
        let redis = redis::Client::open(redis_url.as_str())
            .context("failed to create redis client")?;

        Ok(Self {
            inner: Arc::new(StateInner {
                jobs: RwLock::new(HashMap::new()),
                redis,
            }),
        })
    }

    pub async fn create_job(&self, req: SubmitJobRequest) -> anyhow::Result<SubmitJobResponse> {
        let request_id = Uuid::new_v4();
        let created_at = Utc::now();

        let data = JobData {
            request_id,
            model_id: req.model_id.clone(),
            payload: req.payload,
            status: JobStatus::InQueue,
            result: None,
            logs: Vec::new(),
            created_at,
            updated_at: created_at,
            store_io: req.store_io,
            fal_webhook: req.fal_webhook,
        };

        let (tx, _rx) = watch::channel(JobSnapshot {
            request_id,
            model_id: data.model_id.clone(),
            status: data.status.clone(),
            queue_position: Some(0),
            logs: data.logs.clone(),
            result: None,
        });

        // Store job in memory for status tracking
        {
            let mut jobs = self.inner.jobs.write().await;
            jobs.insert(request_id, JobRecord { data: data.clone(), notifier: tx.clone() });
        }

        // Enqueue in Redis for workers to pick up
        let mut conn = self.inner.redis.get_multiplexed_async_connection().await?;
        let queue_key = format!("queue:{}", data.model_id);
        let job_key = format!("job:{}", request_id);

        // Store job data in Redis
        let job_json = serde_json::to_string(&serde_json::json!({
            "id": request_id.to_string(),
            "model": data.model_id,
            "status": "pending",
            "params": data.payload,
            "created_at": created_at.timestamp(),
        }))?;

        conn.set_ex(&job_key, job_json, 86400).await?; // 24 hour TTL
        conn.rpush(&queue_key, request_id.to_string()).await?;

        self.refresh_snapshot(request_id).await?;

        Ok(SubmitJobResponse { request_id })
    }

    pub async fn job_details(&self, request_id: Uuid) -> anyhow::Result<JobDetailsResponse> {
        let snapshot = self.refresh_snapshot(request_id).await?;
        Ok(JobDetailsResponse {
            request_id: snapshot.request_id,
            model_id: snapshot.model_id,
            status: snapshot.status,
            queue_position: snapshot.queue_position,
            logs: snapshot.logs,
            result: snapshot.result,
        })
    }

    pub async fn cancel_job(&self, request_id: Uuid) -> CancelStatus {
        let mut jobs = self.inner.jobs.write().await;
        let Some(record) = jobs.get_mut(&request_id) else {
            return CancelStatus::NotFound;
        };

        match record.data.status {
            JobStatus::InQueue => {
                record.data.status = JobStatus::Canceled;
                record.data.updated_at = Utc::now();
                record.data.logs.push("cancellation requested".into());
                drop(jobs);
                let _ = self.refresh_snapshot(request_id).await;
                CancelStatus::CancellationRequested
            }
            JobStatus::Completed | JobStatus::Error | JobStatus::Canceled => CancelStatus::AlreadyCompleted,
            JobStatus::InProgress => CancelStatus::TooLate,
        }
    }

    pub async fn subscribe(&self, request_id: Uuid) -> anyhow::Result<watch::Receiver<JobSnapshot>> {
        let jobs = self.inner.jobs.read().await;
        let record = jobs.get(&request_id).context("job not found")?;
        Ok(record.notifier.subscribe())
    }

    pub async fn run_sync(&self, req: SyncRequest) -> SyncResponse {
        let mut logs = vec![format!("model {} invocation accepted", req.model_id)];
        tokio::time::sleep(Duration::from_millis(200)).await;
        logs.push("model inference started".into());
        tokio::time::sleep(Duration::from_millis(400)).await;
        logs.push("model inference completed".into());

        let result = serde_json::json!({
            "model_id": req.model_id,
            "outputs": [
                {
                    "type": "artifact",
                    "url": format!("https://cdn.vyvo.local/artifacts/{}.json", Uuid::new_v4()),
                }
            ],
            "echo": req.payload,
            "timings": {
                "total_ms": 600
            }
        });

        SyncResponse { result, logs }
    }

    async fn refresh_snapshot(&self, request_id: Uuid) -> anyhow::Result<JobSnapshot> {
        let (snapshot, sender) = {
            let jobs = self.inner.jobs.read().await;
            let record = jobs.get(&request_id).context("job not found")?;
            let snapshot = compute_snapshot(&jobs, &record.data);
            let sender = record.notifier.clone();
            (snapshot, sender)
        };

        let _ = sender.send_replace(snapshot.clone());
        Ok(snapshot)
    }

    pub async fn transition_status(&self, request_id: Uuid, status: JobStatus, log: Option<String>) -> anyhow::Result<JobSnapshot> {
        {
            let mut jobs = self.inner.jobs.write().await;
            let record = jobs.get_mut(&request_id).context("job not found")?;
            record.data.status = status;
            record.data.updated_at = Utc::now();
            if let Some(log_line) = log {
                record.data.logs.push(log_line);
            }
        }
        self.refresh_snapshot(request_id).await
    }

    pub async fn set_result(&self, request_id: Uuid, result: serde_json::Value, log: Option<String>) -> anyhow::Result<JobSnapshot> {
        {
            let mut jobs = self.inner.jobs.write().await;
            let record = jobs.get_mut(&request_id).context("job not found")?;
            record.data.status = JobStatus::Completed;
            record.data.result = Some(result);
            record.data.updated_at = Utc::now();
            if let Some(log_line) = log {
                record.data.logs.push(log_line);
            }
        }
        self.refresh_snapshot(request_id).await
    }
}

fn compute_snapshot(jobs: &HashMap<Uuid, JobRecord>, data: &JobData) -> JobSnapshot {
    let queue_position = if data.status == JobStatus::InQueue {
        let ahead = jobs
            .values()
            .filter(|other| other.data.status == JobStatus::InQueue && other.data.created_at < data.created_at)
            .count();
        Some(ahead)
    } else {
        None
    };

    JobSnapshot {
        request_id: data.request_id,
        model_id: data.model_id.clone(),
        status: data.status.clone(),
        queue_position,
        logs: data.logs.clone(),
        result: data.result.clone(),
    }
}

pub fn stream_from_receiver(rx: watch::Receiver<JobSnapshot>) -> impl tokio_stream::Stream<Item = JobSnapshot> {
    WatchStream::new(rx).map(|snapshot| snapshot.clone())
}
