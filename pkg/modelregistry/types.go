package modelregistry

import (
	"time"
)

// Status constants for models and versions.
const (
	StatusDraft    = "DRAFT"
	StatusReady    = "READY"
	StatusInactive = "INACTIVE"
)

// Model represents a logical model family exposed to clients.
type Model struct {
	ID               string            `json:"id"`
	ExternalID       string            `json:"external_id"`
	DisplayName      string            `json:"display_name"`
	Family           string            `json:"family"`
	Description      string            `json:"description"`
	Metadata         map[string]any    `json:"metadata"`
	Status           string            `json:"status"`
	DefaultVersionID *string           `json:"default_version_id,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	LatestVersion    *ModelVersionInfo `json:"latest_version,omitempty"`
}

// ModelVersion captures deployable artifacts of a model.
type ModelVersion struct {
	ID           string         `json:"id"`
	ModelID      string         `json:"model_id"`
	Version      string         `json:"version"`
	RunnerImage  string         `json:"runner_image"`
	WeightsURI   *string        `json:"weights_uri,omitempty"`
	MinGPUMemGB  *int           `json:"min_gpu_mem_gb,omitempty"`
	MinVRAMGB    *int           `json:"min_vram_gb,omitempty"`
	MaxBatchSize *int           `json:"max_batch_size,omitempty"`
	Parameters   map[string]any `json:"parameters"`
	Capabilities []string       `json:"capabilities"`
	LatencyTier  *string        `json:"latency_tier,omitempty"`
	ArtifactURI  *string        `json:"artifact_uri,omitempty"`
	Status       string         `json:"status"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// ModelVersionInfo is a slim view attached to Model list responses.
type ModelVersionInfo struct {
	ID          string    `json:"id"`
	Version     string    `json:"version"`
	Status      string    `json:"status"`
	RunnerImage string    `json:"runner_image"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CreateModelInput bundles fields for registering a new model family.
type CreateModelInput struct {
	ExternalID  string         `json:"external_id"`
	DisplayName string         `json:"display_name"`
	Family      string         `json:"family"`
	Description string         `json:"description"`
	Metadata    map[string]any `json:"metadata"`
}

// CreateVersionInput represents a new deployable version.
type CreateVersionInput struct {
	Version      string         `json:"version"`
	RunnerImage  string         `json:"runner_image"`
	WeightsURI   *string        `json:"weights_uri,omitempty"`
	MinGPUMemGB  *int           `json:"min_gpu_mem_gb,omitempty"`
	MinVRAMGB    *int           `json:"min_vram_gb,omitempty"`
	MaxBatchSize *int           `json:"max_batch_size,omitempty"`
	Parameters   map[string]any `json:"parameters"`
	Capabilities []string       `json:"capabilities"`
	LatencyTier  *string        `json:"latency_tier,omitempty"`
	ArtifactURI  *string        `json:"artifact_uri,omitempty"`
	Status       string         `json:"status"`
}

// PromoteVersionInput configures promotion of a version to default.
type PromoteVersionInput struct {
	DefaultVersionID string `json:"default_version_id"`
}

// QueryOptions controls filtering when listing models.
type QueryOptions struct {
	Status []string
	Family []string
	Limit  int
	Offset int
}

// Store defines persistence operations for models.
type Store interface {
	EnsureSchema() error
	CreateModel(input CreateModelInput) (Model, error)
	CreateVersion(modelID string, input CreateVersionInput) (ModelVersion, error)
	ListModels(opts QueryOptions) ([]Model, error)
	GetModel(id string) (Model, error)
	PromoteVersion(modelID, versionID string) error
}
