use std::time::Duration;

use serde::Deserialize;

#[derive(Debug, Clone, Deserialize)]
pub struct Config {
    pub redis_url: String,
    pub postgres_url: Option<String>,
    pub registry_url: Option<String>,
    pub control_plane_url: Option<String>,
    #[serde(default = "default_registry_ttl_secs")]
    pub registry_ttl_secs: u64,
}

fn default_registry_ttl_secs() -> u64 {
    60
}

impl Config {
    pub fn load() -> anyhow::Result<Self> {
        let redis_url =
            std::env::var("REDIS_URL").unwrap_or_else(|_| "redis://redis:6379".to_string());
        let postgres_url = std::env::var("ORCHESTRATOR_DATABASE_URL").ok();
        let registry_url = std::env::var("MODEL_REGISTRY_URL").ok();
        let control_plane_url = std::env::var("CONTROL_PLANE_URL").ok();
        let registry_ttl_secs = std::env::var("MODEL_REGISTRY_TTL_SECS")
            .ok()
            .and_then(|v| v.parse::<u64>().ok())
            .unwrap_or_else(default_registry_ttl_secs);

        Ok(Self {
            redis_url,
            postgres_url,
            registry_url,
            control_plane_url,
            registry_ttl_secs,
        })
    }

    pub fn registry_ttl(&self) -> Duration {
        Duration::from_secs(self.registry_ttl_secs)
    }
}
