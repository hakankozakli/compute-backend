use std::{collections::HashMap, sync::Arc, time::Duration};

use anyhow::{anyhow, Context};
use chrono::{DateTime, Utc};
use deadpool_postgres::{
    Manager, ManagerConfig, Object, Pool, RecyclingMethod, Runtime as DeadpoolRuntime,
};
use redis::AsyncCommands;
use serde::{Deserialize, Serialize};
use tokio::sync::{watch, RwLock};
use tokio_stream::{wrappers::WatchStream, StreamExt};
use uuid::Uuid;

use crate::{
    config::Config,
    control_plane::{ControlPlaneClient, NodeSelection},
    registry::{ModelRegistryCache, RegistryModel},
};

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum JobStatus {
    InQueue,
    InProgress,
    Completed,
    Error,
    Canceled,
}

impl JobStatus {
    fn as_str(&self) -> &'static str {
        match self {
            JobStatus::InQueue => "IN_QUEUE",
            JobStatus::InProgress => "IN_PROGRESS",
            JobStatus::Completed => "COMPLETED",
            JobStatus::Error => "ERROR",
            JobStatus::Canceled => "CANCELED",
        }
    }

    fn from_str(value: &str) -> anyhow::Result<Self> {
        match value {
            "IN_QUEUE" => Ok(JobStatus::InQueue),
            "IN_PROGRESS" => Ok(JobStatus::InProgress),
            "COMPLETED" => Ok(JobStatus::Completed),
            "ERROR" => Ok(JobStatus::Error),
            "CANCELED" => Ok(JobStatus::Canceled),
            other => Err(anyhow!("unknown job status: {other}")),
        }
    }
}

#[derive(Debug, Clone, Serialize)]
pub struct JobSnapshot {
    pub request_id: Uuid,
    pub model_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub version_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub node_id: Option<String>,
    pub status: JobStatus,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub queue_position: Option<i32>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub logs: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<serde_json::Value>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub artifacts: Vec<serde_json::Value>,
}

impl JobSnapshot {
    fn from_job(data: &JobData) -> Self {
        Self {
            request_id: data.request_id,
            model_id: data.model_id.clone(),
            version_id: data.version_id.clone(),
            node_id: data.node_id.clone(),
            status: data.status.clone(),
            queue_position: data.queue_position,
            logs: data.logs.clone(),
            result: data.result.clone(),
            artifacts: data.artifacts.clone(),
        }
    }
}

#[derive(Debug, Clone, Deserialize)]
pub struct SubmitJobRequest {
    pub model_id: String,
    #[serde(default)]
    pub version_id: Option<String>,
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
    #[serde(skip_serializing_if = "Option::is_none")]
    pub version_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub node_id: Option<String>,
    pub status: JobStatus,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub queue_position: Option<i32>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub logs: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<serde_json::Value>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub artifacts: Vec<serde_json::Value>,
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

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum RunnerEventStatus {
    Started,
    InProgress,
    Log,
    Completed,
    Error,
}

#[derive(Debug, Clone, Deserialize)]
pub struct RunnerEventRequest {
    pub status: RunnerEventStatus,
    #[serde(default)]
    pub message: Option<String>,
    #[serde(default)]
    pub logs: Vec<String>,
    #[serde(default)]
    pub artifacts: Vec<serde_json::Value>,
    #[serde(default)]
    pub result: Option<serde_json::Value>,
}

#[derive(Clone)]
pub struct AppState {
    inner: Arc<StateInner>,
}

struct StateInner {
    jobs: RwLock<HashMap<Uuid, JobRecord>>,
    redis: redis::Client,
    registry: ModelRegistryCache,
    control_plane: Option<ControlPlaneClient>,
    db: Option<Pool>,
}

struct JobRecord {
    data: JobData,
    notifier: watch::Sender<JobSnapshot>,
}

#[derive(Debug, Clone)]
struct JobData {
    request_id: Uuid,
    model_id: String,
    version_id: Option<String>,
    node_id: Option<String>,
    payload: serde_json::Value,
    status: JobStatus,
    result: Option<serde_json::Value>,
    logs: Vec<String>,
    artifacts: Vec<serde_json::Value>,
    queue_position: Option<i32>,
    created_at: DateTime<Utc>,
    updated_at: DateTime<Utc>,
    store_io: bool,
    fal_webhook: Option<String>,
}

#[derive(Debug, Clone)]
struct JobEventRecord {
    status: JobStatus,
    message: Option<String>,
}

impl AppState {
    pub async fn new(cfg: Config) -> anyhow::Result<Self> {
        let redis =
            redis::Client::open(cfg.redis_url.as_str()).context("failed to create redis client")?;

        let registry_cache = ModelRegistryCache::new(cfg.registry_url.clone(), cfg.registry_ttl());
        let control_plane = cfg
            .control_plane_url
            .as_deref()
            .map(ControlPlaneClient::new);

        let db_pool = if let Some(pg_url) = cfg.postgres_url.as_ref() {
            let mgr_config = ManagerConfig {
                recycling_method: RecyclingMethod::Fast,
            };
            let mgr = Manager::from_config(pg_url.parse()?, tokio_postgres::NoTls, mgr_config);
            let pool = Pool::builder(mgr)
                .runtime(DeadpoolRuntime::Tokio1)
                .create_timeout(Some(Duration::from_secs(5)))
                .recycle_timeout(Some(Duration::from_secs(3)))
                .max_size(16)
                .build()
                .context("create postgres pool")?;
            Some(pool)
        } else {
            None
        };

        Ok(Self {
            inner: Arc::new(StateInner {
                jobs: RwLock::new(HashMap::new()),
                redis,
                registry: registry_cache,
                control_plane,
                db: db_pool,
            }),
        })
    }

    pub async fn create_job(&self, req: SubmitJobRequest) -> anyhow::Result<SubmitJobResponse> {
        let model = self
            .resolve_model(&req.model_id)
            .await
            .with_context(|| format!("model {} not found", req.model_id))?;

        let version_id = req
            .version_id
            .clone()
            .or_else(|| default_version_id(&model))
            .ok_or_else(|| anyhow!("no active version configured for model {}", req.model_id))?;

        let node_selection = self
            .select_node(&model, &version_id)
            .await
            .with_context(|| format!("control-plane selection failed for {}", req.model_id))?;

        let request_id = Uuid::new_v4();
        let created_at = Utc::now();

        let mut data = JobData {
            request_id,
            model_id: req.model_id.clone(),
            version_id: Some(version_id.clone()),
            node_id: node_selection.as_ref().map(|sel| sel.node_id.clone()),
            payload: req.payload.clone(),
            status: JobStatus::InQueue,
            result: None,
            logs: Vec::new(),
            artifacts: Vec::new(),
            queue_position: Some(0),
            created_at,
            updated_at: created_at,
            store_io: req.store_io,
            fal_webhook: req.fal_webhook.clone(),
        };

        let (tx, _rx) = {
            let mut jobs = self.inner.jobs.write().await;
            if data.status == JobStatus::InQueue {
                let ahead = jobs
                    .values()
                    .filter(|other| {
                        other.data.status == JobStatus::InQueue
                            && other.data.created_at < data.created_at
                    })
                    .count() as i32;
                data.queue_position = Some(ahead);
            } else {
                data.queue_position = None;
            }
            let snapshot = JobSnapshot::from_job(&data);
            let (tx, rx) = watch::channel(snapshot.clone());
            jobs.insert(
                request_id,
                JobRecord {
                    data: data.clone(),
                    notifier: tx.clone(),
                },
            );
            let _ = tx.send_replace(snapshot);
            (tx, rx)
        };

        self.persist_new_job(&data).await?;

        self.enqueue_job(&data, node_selection.as_ref()).await?;

        Ok(SubmitJobResponse { request_id })
    }

    pub async fn job_details(&self, request_id: Uuid) -> anyhow::Result<JobDetailsResponse> {
        let snapshot = self.refresh_snapshot(request_id).await?;
        Ok(JobDetailsResponse {
            request_id: snapshot.request_id,
            model_id: snapshot.model_id,
            version_id: snapshot.version_id,
            node_id: snapshot.node_id,
            status: snapshot.status,
            queue_position: snapshot.queue_position,
            logs: snapshot.logs,
            result: snapshot.result,
            artifacts: snapshot.artifacts,
        })
    }

    pub async fn cancel_job(&self, request_id: Uuid) -> CancelStatus {
        if self.ensure_job_cached(request_id).await.is_err() {
            return CancelStatus::NotFound;
        }

        let outcome = self
            .mutate_job(request_id, |data| match data.status {
                JobStatus::InQueue => {
                    let message = "cancellation requested".to_string();
                    data.status = JobStatus::Canceled;
                    data.logs.push(message.clone());
                    Some(JobEventRecord {
                        status: JobStatus::Canceled,
                        message: Some(message),
                    })
                }
                JobStatus::Completed | JobStatus::Error | JobStatus::Canceled => None,
                JobStatus::InProgress => None,
            })
            .await;

        match outcome {
            Ok((_, data_clone, Some(event))) => {
                let _ = self.update_job_row(&data_clone).await;
                let _ = self
                    .record_event(request_id, &event.status, event.message.as_deref())
                    .await;
                CancelStatus::CancellationRequested
            }
            Ok((_, data_clone, None)) => match data_clone.status {
                JobStatus::InProgress => CancelStatus::TooLate,
                JobStatus::Completed | JobStatus::Error | JobStatus::Canceled => {
                    CancelStatus::AlreadyCompleted
                }
                JobStatus::InQueue => CancelStatus::NotFound,
            },
            Err(_) => CancelStatus::NotFound,
        }
    }

    pub async fn subscribe(
        &self,
        request_id: Uuid,
    ) -> anyhow::Result<watch::Receiver<JobSnapshot>> {
        self.ensure_job_cached(request_id).await?;
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

    pub async fn transition_status(
        &self,
        request_id: Uuid,
        status: JobStatus,
        log: Option<String>,
    ) -> anyhow::Result<JobSnapshot> {
        self.ensure_job_cached(request_id).await?;
        let log_clone = log.clone();
        let (snapshot, data_clone, event) = self
            .mutate_job(request_id, move |data| {
                data.status = status.clone();
                if let Some(ref msg) = log_clone {
                    data.logs.push(msg.clone());
                }
                Some(JobEventRecord {
                    status: status.clone(),
                    message: log_clone.clone(),
                })
            })
            .await?;

        self.update_job_row(&data_clone).await?;
        if let Some(event) = event {
            self.record_event(request_id, &event.status, event.message.as_deref())
                .await?;
        }
        Ok(snapshot)
    }

    pub async fn set_result(
        &self,
        request_id: Uuid,
        result: serde_json::Value,
        log: Option<String>,
    ) -> anyhow::Result<JobSnapshot> {
        self.ensure_job_cached(request_id).await?;
        let log_clone = log.clone();
        let (snapshot, data_clone, event) = self
            .mutate_job(request_id, move |data| {
                data.status = JobStatus::Completed;
                data.result = Some(result.clone());
                if let Some(ref msg) = log_clone {
                    data.logs.push(msg.clone());
                }
                Some(JobEventRecord {
                    status: JobStatus::Completed,
                    message: log_clone.clone(),
                })
            })
            .await?;
        self.update_job_row(&data_clone).await?;
        if let Some(event) = event {
            self.record_event(request_id, &event.status, event.message.as_deref())
                .await?;
        }
        Ok(snapshot)
    }

    pub async fn handle_runner_event(
        &self,
        request_id: Uuid,
        event: RunnerEventRequest,
    ) -> anyhow::Result<JobSnapshot> {
        self.ensure_job_cached(request_id).await?;
        let RunnerEventRequest {
            status,
            message,
            logs,
            artifacts,
            result,
        } = event;
        let message_clone = message.clone();
        let (snapshot, data_clone, event_record) = self
            .mutate_job(request_id, move |data| {
                apply_logs(data, &message, &logs);
                match status {
                    RunnerEventStatus::Started | RunnerEventStatus::InProgress => {
                        data.status = JobStatus::InProgress;
                        Some(JobEventRecord {
                            status: JobStatus::InProgress,
                            message: message.clone(),
                        })
                    }
                    RunnerEventStatus::Log => Some(JobEventRecord {
                        status: data.status.clone(),
                        message: message.clone(),
                    }),
                    RunnerEventStatus::Completed => {
                        data.status = JobStatus::Completed;
                        data.result = result.clone();
                        data.artifacts = artifacts.clone();
                        Some(JobEventRecord {
                            status: JobStatus::Completed,
                            message: message.clone(),
                        })
                    }
                    RunnerEventStatus::Error => {
                        data.status = JobStatus::Error;
                        if let Some(res) = result.clone() {
                            data.result = Some(res);
                        }
                        Some(JobEventRecord {
                            status: JobStatus::Error,
                            message: message.clone(),
                        })
                    }
                }
            })
            .await?;

        self.update_job_row(&data_clone).await?;
        if let Some(event_record) = event_record {
            self.record_event(
                request_id,
                &event_record.status,
                event_record.message.as_deref(),
            )
            .await?;
        }
        Ok(snapshot)
    }

    async fn refresh_snapshot(&self, request_id: Uuid) -> anyhow::Result<JobSnapshot> {
        self.ensure_job_cached(request_id).await?;
        let (snapshot, sender) = {
            let mut jobs = self.inner.jobs.write().await;
            let mut record = jobs.remove(&request_id).context("job not found")?;
            if record.data.status == JobStatus::InQueue {
                let ahead = jobs
                    .values()
                    .filter(|other| {
                        other.data.status == JobStatus::InQueue
                            && other.data.created_at < record.data.created_at
                    })
                    .count() as i32;
                record.data.queue_position = Some(ahead);
            } else {
                record.data.queue_position = None;
            }
            let snapshot = JobSnapshot::from_job(&record.data);
            let sender = record.notifier.clone();
            jobs.insert(request_id, record);
            (snapshot, sender)
        };
        let _prev = sender.send_replace(snapshot.clone());
        Ok(snapshot)
    }

    async fn resolve_model(&self, identifier: &str) -> anyhow::Result<RegistryModel> {
        if let Some(model) = self
            .inner
            .registry
            .get_model_by_external(identifier)
            .await?
        {
            return Ok(model);
        }
        if let Some(model) = self.inner.registry.get_model_by_id(identifier).await? {
            return Ok(model);
        }
        self.inner.registry.refresh().await?;
        if let Some(model) = self
            .inner
            .registry
            .get_model_by_external(identifier)
            .await?
        {
            return Ok(model);
        }
        if let Some(model) = self.inner.registry.get_model_by_id(identifier).await? {
            return Ok(model);
        }
        Err(anyhow!("model {identifier} not found"))
    }

    async fn select_node(
        &self,
        model: &RegistryModel,
        version_id: &str,
    ) -> anyhow::Result<Option<NodeSelection>> {
        let Some(control_plane) = self.inner.control_plane.as_ref() else {
            return Ok(None);
        };
        let selection = control_plane
            .select_assignment(&model.external_id, Some(version_id))
            .await?;
        if selection.is_none() {
            return Err(anyhow!(
                "no ready nodes assigned for model {} version {}",
                model.external_id,
                version_id
            ));
        }
        Ok(selection)
    }

    async fn enqueue_job(
        &self,
        data: &JobData,
        selection: Option<&NodeSelection>,
    ) -> anyhow::Result<()> {
        let mut conn = self.inner.redis.get_multiplexed_async_connection().await?;
        let queue_key = match selection {
            Some(sel) => format!("queue:{}:{}", sel.node_id, data.model_id),
            None => format!("queue:{}", data.model_id),
        };
        let job_key = format!("job:{}", data.request_id);

        let job_payload = serde_json::json!({
            "id": data.request_id,
            "model": data.model_id,
            "version_id": data.version_id,
            "node_id": selection.map(|s| s.node_id.clone()),
            "payload": data.payload,
            "created_at": data.created_at.to_rfc3339(),
        });
        conn.set_ex(&job_key, serde_json::to_string(&job_payload)?, 86400)
            .await?;
        conn.rpush(&queue_key, data.request_id.to_string()).await?;
        Ok(())
    }

    async fn persist_new_job(&self, data: &JobData) -> anyhow::Result<()> {
        let Some(pool) = &self.inner.db else {
            tracing::warn!("postgres pool not configured; job persistence disabled");
            return Ok(());
        };
        let mut client = pool.get().await?;
        let logs_json = serde_json::Value::Array(vec![]);
        let artifacts_json = serde_json::Value::Array(vec![]);
        let request_id = data.request_id.to_string();
        client
            .execute(
                "INSERT INTO jobs (id, model_id, version_id, node_id, status, queue_position, payload, result, logs, artifacts, created_at, updated_at)
                 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)",
                &[
                    &request_id,
                    &data.model_id,
                    &data.version_id,
                    &data.node_id,
                    &data.status.as_str(),
                    &data.queue_position,
                    &data.payload,
                    &data.result,
                    &logs_json,
                    &artifacts_json,
                    &data.created_at,
                    &data.updated_at,
                ],
            )
            .await?;
        self.record_event_inner(
            &mut client,
            data.request_id,
            data.status.as_str(),
            Some("job queued"),
        )
        .await?;
        Ok(())
    }

    async fn update_job_row(&self, data: &JobData) -> anyhow::Result<()> {
        let Some(pool) = &self.inner.db else {
            return Ok(());
        };
        let mut client = pool.get().await?;
        let logs_json = serde_json::Value::Array(
            data.logs
                .iter()
                .cloned()
                .map(serde_json::Value::String)
                .collect(),
        );
        let artifacts_json = serde_json::Value::Array(data.artifacts.clone());
        let request_id = data.request_id.to_string();
        client
            .execute(
                "UPDATE jobs SET status=$2, queue_position=$3, result=$4, logs=$5, artifacts=$6, updated_at=$7, node_id=$8, version_id=$9
                 WHERE id=$1",
                &[&request_id, &data.status.as_str(), &data.queue_position, &data.result, &logs_json, &artifacts_json, &data.updated_at, &data.node_id, &data.version_id],
            )
            .await?;
        Ok(())
    }

    async fn record_event(
        &self,
        job_id: Uuid,
        status: &JobStatus,
        message: Option<&str>,
    ) -> anyhow::Result<()> {
        let Some(pool) = &self.inner.db else {
            return Ok(());
        };
        let mut client = pool.get().await?;
        self.record_event_inner(&mut client, job_id, status.as_str(), message)
            .await
    }

    async fn record_event_inner(
        &self,
        client: &mut Object,
        job_id: Uuid,
        status: &str,
        message: Option<&str>,
    ) -> anyhow::Result<()> {
        let job_id_str = job_id.to_string();
        client
            .execute(
                "INSERT INTO job_events (job_id, status, message) VALUES ($1,$2,$3)",
                &[&job_id_str, &status, &message],
            )
            .await?;
        Ok(())
    }

    async fn ensure_job_cached(&self, request_id: Uuid) -> anyhow::Result<()> {
        if self.inner.jobs.read().await.contains_key(&request_id) {
            return Ok(());
        }
        let Some(pool) = &self.inner.db else {
            return Err(anyhow!("job {request_id} not found"));
        };
        let mut client = pool.get().await?;
        let request_id_str = request_id.to_string();
        let row = client
            .query_opt(
                "SELECT id, model_id, version_id, node_id, status, queue_position, payload, result, logs, artifacts, created_at, updated_at FROM jobs WHERE id=$1",
                &[&request_id_str],
            )
            .await?;
        let Some(row) = row else {
            return Err(anyhow!("job {request_id} not found"));
        };
        let mut data = job_data_from_row(&row)?;
        let mut jobs = self.inner.jobs.write().await;
        if data.status == JobStatus::InQueue {
            let ahead = jobs
                .values()
                .filter(|other| {
                    other.data.status == JobStatus::InQueue
                        && other.data.created_at < data.created_at
                })
                .count() as i32;
            data.queue_position = Some(ahead);
        } else {
            data.queue_position = None;
        }
        let snapshot = JobSnapshot::from_job(&data);
        let (tx, _rx) = watch::channel(snapshot.clone());
        jobs.insert(
            request_id,
            JobRecord {
                data,
                notifier: tx.clone(),
            },
        );
        let _prev = tx.send_replace(snapshot);
        Ok(())
    }

    async fn mutate_job<F>(
        &self,
        request_id: Uuid,
        mutator: F,
    ) -> anyhow::Result<(JobSnapshot, JobData, Option<JobEventRecord>)>
    where
        F: FnOnce(&mut JobData) -> Option<JobEventRecord>,
    {
        let (snapshot, data_clone, event) = {
            let mut jobs = self.inner.jobs.write().await;
            let mut record = jobs.remove(&request_id).context("job not found")?;
            let event = mutator(&mut record.data);
            record.data.updated_at = Utc::now();
            if record.data.status == JobStatus::InQueue {
                let ahead = jobs
                    .values()
                    .filter(|other| {
                        other.data.status == JobStatus::InQueue
                            && other.data.created_at < record.data.created_at
                    })
                    .count() as i32;
                record.data.queue_position = Some(ahead);
            } else {
                record.data.queue_position = None;
            }
            let snapshot = JobSnapshot::from_job(&record.data);
            let data_clone = record.data.clone();
            let sender = record.notifier.clone();
            jobs.insert(request_id, record);
            let _prev = sender.send_replace(snapshot.clone());
            (snapshot, data_clone, event)
        };
        Ok((snapshot, data_clone, event))
    }
}

fn default_version_id(model: &RegistryModel) -> Option<String> {
    if let Some(default_id) = model.default_version_id.clone() {
        return Some(default_id);
    }
    model.latest_version.as_ref().map(|v| v.id.clone())
}

fn job_data_from_row(row: &tokio_postgres::Row) -> anyhow::Result<JobData> {
    let request_id_str: String = row.get("id");
    let request_id = Uuid::parse_str(&request_id_str).unwrap_or_else(|_| Uuid::nil());
    let model_id: String = row.get("model_id");
    let version_id: Option<String> = row.get("version_id");
    let node_id: Option<String> = row.get("node_id");
    let status_str: String = row.get("status");
    let status = JobStatus::from_str(&status_str)?;
    let queue_position: Option<i32> = row.get("queue_position");
    let payload: serde_json::Value = row.get("payload");
    let result: Option<serde_json::Value> = row.get("result");
    let logs_value: serde_json::Value = row.get("logs");
    let artifacts_value: serde_json::Value = row.get("artifacts");
    let created_at: DateTime<Utc> = row.get("created_at");
    let updated_at: DateTime<Utc> = row.get("updated_at");

    let logs = logs_value
        .as_array()
        .map(|items| {
            items
                .iter()
                .filter_map(|v| v.as_str().map(|s| s.to_string()))
                .collect()
        })
        .unwrap_or_default();
    let artifacts = artifacts_value.as_array().cloned().unwrap_or_else(Vec::new);

    Ok(JobData {
        request_id,
        model_id,
        version_id,
        node_id,
        payload,
        status,
        result,
        logs,
        artifacts,
        queue_position,
        created_at,
        updated_at,
        store_io: false,
        fal_webhook: None,
    })
}

fn apply_logs(data: &mut JobData, message: &Option<String>, logs: &[String]) {
    for entry in logs {
        data.logs.push(entry.clone());
    }
    if let Some(msg) = message {
        if !msg.is_empty() {
            data.logs.push(msg.clone());
        }
    }
}

pub fn stream_from_receiver(
    rx: watch::Receiver<JobSnapshot>,
) -> impl tokio_stream::Stream<Item = JobSnapshot> {
    WatchStream::new(rx).map(|snapshot| snapshot.clone())
}
