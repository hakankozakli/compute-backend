use std::{collections::HashMap, sync::Arc, time::Duration};

use anyhow::Context;
use chrono::{DateTime, Utc};
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
    pub fn new() -> Self {
        Self {
            inner: Arc::new(StateInner {
                jobs: RwLock::new(HashMap::new()),
            }),
        }
    }

    pub async fn create_job(&self, req: SubmitJobRequest) -> anyhow::Result<SubmitJobResponse> {
        let request_id = Uuid::new_v4();
        let data = JobData {
            request_id,
            model_id: req.model_id.clone(),
            payload: req.payload,
            status: JobStatus::InQueue,
            result: None,
            logs: Vec::new(),
            created_at: Utc::now(),
            updated_at: Utc::now(),
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

        {
            let mut jobs = self.inner.jobs.write().await;
            jobs.insert(request_id, JobRecord { data, notifier: tx.clone() });
        }

        self.refresh_snapshot(request_id).await?;

        self.spawn_processing_task(request_id);

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

    fn spawn_processing_task(&self, request_id: Uuid) {
        let state = self.clone();
        tokio::spawn(async move {
            if let Err(err) = state.process_job(request_id).await {
                tracing::error!(%request_id, "job processing failed: {err}");
            }
        });
    }

    async fn process_job(&self, request_id: Uuid) -> anyhow::Result<()> {
        tokio::time::sleep(Duration::from_millis(100)).await;
        self.transition_status(request_id, JobStatus::InProgress, Some("job dispatched to runner".into())).await?;
        tokio::time::sleep(Duration::from_millis(800)).await;

        let result = serde_json::json!({
            "request_id": request_id,
            "outputs": [
                {
                    "type": "image",
                    "url": format!("https://cdn.vyvo.local/outputs/{}.png", Uuid::new_v4()),
                }
            ],
            "metrics": {
                "inference_ms": 800,
                "time_to_first_byte_ms": 120
            }
        });

        self.set_result(request_id, result, Some("job completed".into())).await
    }

    async fn transition_status(&self, request_id: Uuid, status: JobStatus, log: Option<String>) -> anyhow::Result<JobSnapshot> {
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

    async fn set_result(&self, request_id: Uuid, result: serde_json::Value, log: Option<String>) -> anyhow::Result<JobSnapshot> {
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
