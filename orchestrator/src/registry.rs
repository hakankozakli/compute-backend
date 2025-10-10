use std::{collections::HashMap, sync::Arc, time::Instant};

use anyhow::{anyhow, Context};
use serde::Deserialize;
use tokio::sync::RwLock;

#[derive(Debug, Clone, Deserialize)]
pub struct RegistryModel {
    pub id: String,
    pub external_id: String,
    pub display_name: String,
    pub status: String,
    #[serde(default)]
    pub default_version_id: Option<String>,
    #[serde(default)]
    pub latest_version: Option<RegistryModelVersionInfo>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct RegistryModelVersionInfo {
    pub id: String,
    pub version: String,
    pub status: String,
    pub runner_image: String,
}

#[derive(Debug, Clone, Deserialize)]
struct RegistryResponse {
    models: Vec<RegistryModel>,
}

#[derive(Debug, Clone)]
pub struct ModelRegistryCache {
    inner: Arc<RwLock<CacheState>>,
    client: Option<reqwest::Client>,
    endpoint: Option<String>,
    ttl: std::time::Duration,
}

#[derive(Debug, Default)]
struct CacheState {
    by_external: HashMap<String, RegistryModel>,
    id_index: HashMap<String, String>,
    fetched_at: Option<Instant>,
}

impl ModelRegistryCache {
    pub fn new(endpoint: Option<String>, ttl: std::time::Duration) -> Self {
        let client = endpoint.as_ref().map(|_| reqwest::Client::new());
        Self {
            inner: Arc::new(RwLock::new(CacheState::default())),
            client,
            endpoint,
            ttl,
        }
    }

    pub async fn get_model_by_external(
        &self,
        external_id: &str,
    ) -> anyhow::Result<Option<RegistryModel>> {
        if self.endpoint.is_none() {
            return Ok(None);
        }

        let cache = self.ensure_cache().await?;
        Ok(cache.by_external.get(external_id).cloned())
    }

    pub async fn get_model_by_id(&self, id: &str) -> anyhow::Result<Option<RegistryModel>> {
        if self.endpoint.is_none() {
            return Ok(None);
        }

        let cache = self.ensure_cache().await?;
        if let Some(ext_id) = cache.id_index.get(id) {
            return Ok(cache.by_external.get(ext_id).cloned());
        }
        Ok(None)
    }

    pub async fn refresh(&self) -> anyhow::Result<()> {
        if self.endpoint.is_none() {
            return Ok(());
        }
        let models = self.fetch_remote().await?;
        let mut guard = self.inner.write().await;
        Self::store_models(&mut *guard, models);
        guard.fetched_at = Some(Instant::now());
        Ok(())
    }

    async fn ensure_cache(&self) -> anyhow::Result<CacheState> {
        {
            let guard = self.inner.read().await;
            if let Some(fetched_at) = guard.fetched_at {
                if fetched_at.elapsed() < self.ttl {
                    return Ok(guard.clone());
                }
            }
        }

        let models = self.fetch_remote().await.unwrap_or_else(|err| {
            tracing::warn!("registry fetch failed: {err}");
            Vec::new()
        });

        let mut guard = self.inner.write().await;
        Self::store_models(&mut *guard, models);
        guard.fetched_at = Some(Instant::now());
        Ok(guard.clone())
    }

    async fn fetch_remote(&self) -> anyhow::Result<Vec<RegistryModel>> {
        let endpoint = self
            .endpoint
            .as_ref()
            .ok_or_else(|| anyhow!("registry endpoint not configured"))?;
        let client = self
            .client
            .as_ref()
            .ok_or_else(|| anyhow!("registry client missing"))?;
        let url = format!("{}/api/models", endpoint.trim_end_matches('/'));
        let resp = client.get(url).send().await?.error_for_status()?;
        let body: RegistryResponse = resp.json().await.context("decode registry response")?;
        Ok(body.models)
    }

    fn store_models(cache: &mut CacheState, models: Vec<RegistryModel>) {
        cache.by_external.clear();
        cache.id_index.clear();
        for model in models {
            cache
                .id_index
                .insert(model.id.clone(), model.external_id.clone());
            cache.by_external.insert(model.external_id.clone(), model);
        }
    }
}

impl Clone for CacheState {
    fn clone(&self) -> Self {
        Self {
            by_external: self.by_external.clone(),
            id_index: self.id_index.clone(),
            fetched_at: self.fetched_at,
        }
    }
}
