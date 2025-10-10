use anyhow::Context;
use serde::Deserialize;

#[derive(Debug, Clone)]
pub struct ControlPlaneClient {
    base_url: String,
    client: reqwest::Client,
}

#[derive(Debug, Clone)]
pub struct NodeSelection {
    pub node_id: String,
    pub weights_uri: Option<String>,
}

#[derive(Debug, Deserialize)]
struct NodesResponse {
    #[serde(default)]
    nodes: Vec<NodeInfo>,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct NodeInfo {
    id: String,
    status: String,
    #[serde(default)]
    assignments: Vec<NodeAssignment>,
}

#[derive(Debug, Deserialize, Clone)]
#[serde(rename_all = "camelCase")]
struct NodeAssignment {
    model_id: String,
    version_id: String,
    #[serde(default)]
    weights_uri: Option<String>,
}

impl ControlPlaneClient {
    pub fn new(base_url: &str) -> Self {
        Self {
            base_url: base_url.trim_end_matches('/').to_string(),
            client: reqwest::Client::new(),
        }
    }

    pub async fn select_assignment(
        &self,
        model_identifier: &str,
        version_id: Option<&str>,
    ) -> anyhow::Result<Option<NodeSelection>> {
        let nodes = self.fetch_nodes().await?;
        for node in nodes {
            if !node.status.eq_ignore_ascii_case("READY") {
                continue;
            }
            for assignment in node.assignments {
                if !matches_model(&assignment.model_id, model_identifier) {
                    continue;
                }
                if let Some(requested_version) = version_id {
                    if assignment.version_id != requested_version {
                        continue;
                    }
                }
                return Ok(Some(NodeSelection {
                    node_id: node.id,
                    weights_uri: assignment.weights_uri,
                }));
            }
        }
        Ok(None)
    }

    async fn fetch_nodes(&self) -> anyhow::Result<Vec<NodeInfo>> {
        let url = format!("{}/api/nodes", self.base_url);
        let resp = self.client.get(url).send().await?.error_for_status()?;
        let payload: NodesResponse = resp.json().await.context("decode control-plane nodes")?;
        Ok(payload.nodes)
    }
}

fn matches_model(assignment_model: &str, requested: &str) -> bool {
    if assignment_model.eq_ignore_ascii_case(requested) {
        return true;
    }
    // Allow matching by short suffix after last slash for convenience.
    let assignment_suffix = assignment_model.split('/').last();
    let requested_suffix = requested.split('/').last();
    assignment_suffix == requested_suffix
}
