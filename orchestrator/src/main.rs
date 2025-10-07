mod state;

use std::{convert::Infallible, net::SocketAddr};

use axum::{
    extract::{Path, State},
    http::StatusCode,
    response::sse::{Event, KeepAlive, Sse},
    response::IntoResponse,
    routing::{get, post, put},
    Json, Router,
};
use serde_json::json;
use tokio_stream::StreamExt;
use tracing_subscriber::{layer::SubscriberExt, util::SubscriberInitExt};
use uuid::Uuid;

use crate::state::{
    self,
    AppState,
    CancelResponse,
    CancelStatus,
    JobDetailsResponse,
    SubmitJobRequest,
    SubmitJobResponse,
    SyncRequest,
    SyncResponse,
};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    setup_tracing()?;

    let redis_url = std::env::var("REDIS_URL").ok();
    let state = AppState::new(redis_url)?;

    let app = Router::new()
        .route("/healthz", get(healthz))
        .route("/v1/internal/jobs", post(submit_job))
        .route("/v1/internal/jobs/:request_id", get(get_job))
        .route("/v1/internal/jobs/:request_id/status", get(get_job_status))
        .route("/v1/internal/jobs/:request_id/stream", get(stream_job_status))
        .route("/v1/internal/jobs/:request_id/cancel", put(cancel_job))
        .route("/v1/internal/sync", post(sync_invoke))
        .with_state(state);

    let addr: SocketAddr = "0.0.0.0:8081".parse()?;
    tracing::info!("orchestrator listening", %addr);

    axum::Server::bind(&addr)
        .serve(app.into_make_service())
        .await?;

    Ok(())
}

async fn healthz() -> impl IntoResponse {
    Json(json!({ "status": "ok" }))
}

async fn submit_job(State(state): State<AppState>, Json(payload): Json<SubmitJobRequest>) -> Result<(StatusCode, Json<SubmitJobResponse>), ApiError> {
    let response = state.create_job(payload).await.map_err(ApiError::internal)?;
    Ok((StatusCode::CREATED, Json(response)))
}

async fn get_job(State(state): State<AppState>, Path(request_id): Path<Uuid>) -> Result<Json<JobDetailsResponse>, ApiError> {
    let details = state
        .job_details(request_id)
        .await
        .map_err(|_| ApiError::not_found())?;
    Ok(Json(details))
}

async fn get_job_status(State(state): State<AppState>, Path(request_id): Path<Uuid>) -> Result<Json<JobDetailsResponse>, ApiError> {
    let details = state
        .job_details(request_id)
        .await
        .map_err(|_| ApiError::not_found())?;
    Ok(Json(details))
}

async fn stream_job_status(State(state): State<AppState>, Path(request_id): Path<Uuid>) -> Result<Sse<impl tokio_stream::Stream<Item = Result<Event, Infallible>>>, ApiError> {
    let receiver = state
        .subscribe(request_id)
        .await
        .map_err(|_| ApiError::not_found())?;

    let stream = state::stream_from_receiver(receiver).filter_map(|snapshot| {
        match serde_json::to_string(&snapshot) {
            Ok(payload) => {
                let event = Event::default().data(payload);
                Some(Ok::<Event, Infallible>(event))
            }
            Err(err) => {
                tracing::error!("serialize snapshot for SSE failed: {err}");
                None
            }
        }
    });

    Ok(Sse::new(stream).keep_alive(KeepAlive::new()))
}

async fn cancel_job(State(state): State<AppState>, Path(request_id): Path<Uuid>) -> Result<(StatusCode, Json<CancelResponse>), ApiError> {
    let status = state.cancel_job(request_id).await;
    if matches!(status, CancelStatus::NotFound) {
        return Err(ApiError::not_found());
    }

    let status_code = match status {
        CancelStatus::CancellationRequested => StatusCode::ACCEPTED,
        CancelStatus::AlreadyCompleted | CancelStatus::TooLate => StatusCode::BAD_REQUEST,
        CancelStatus::NotFound => StatusCode::NOT_FOUND,
    };

    Ok((status_code, Json(CancelResponse { status })))
}

async fn sync_invoke(State(state): State<AppState>, Json(payload): Json<SyncRequest>) -> Result<Json<SyncResponse>, ApiError> {
    let result = state.run_sync(payload).await;
    Ok(Json(result))
}

#[derive(Debug)]
struct ApiError {
    status: StatusCode,
    message: String,
}

impl ApiError {
    fn not_found() -> Self {
        Self {
            status: StatusCode::NOT_FOUND,
            message: "resource not found".into(),
        }
    }

    fn internal<T: Into<anyhow::Error>>(err: T) -> Self {
        let err = err.into();
        tracing::error!("{err:?}");
        Self {
            status: StatusCode::INTERNAL_SERVER_ERROR,
            message: "internal error".into(),
        }
    }
}

impl IntoResponse for ApiError {
    fn into_response(self) -> axum::response::Response {
        let body = Json(json!({ "error": self.message }));
        (self.status, body).into_response()
    }
}

fn setup_tracing() -> anyhow::Result<()> {
    let filter_layer = tracing_subscriber::EnvFilter::try_from_default_env()
        .or_else(|_| tracing_subscriber::EnvFilter::try_new("info"))?;

    let fmt_layer = tracing_subscriber::fmt::layer()
        .with_target(true)
        .with_timer(tracing_subscriber::fmt::time::UtcTime::rfc_3339());

    tracing_subscriber::registry()
        .with(filter_layer)
        .with(fmt_layer)
        .try_init()
        .ok();

    Ok(())
}
