package builder

import "time"

// Status represents the lifecycle state of a build.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// Build describes a runner build job tracked by the builder service.
type Build struct {
	ID          string    `json:"id"`
	Template    string    `json:"template"`
	RunnerName  string    `json:"runner_name"`
	ModelID     string    `json:"model_id"`
	Version     string    `json:"version"`
	Repository  string    `json:"repository"`
	RunnerImage string    `json:"runner_image"`
	WeightsURI  string    `json:"weights_uri,omitempty"`
	Status      Status    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// CreateRequest captures the payload needed to request a new build.
type CreateRequest struct {
	Template    string            `json:"template"`
	RunnerName  string            `json:"runner_name"`
	ModelID     string            `json:"model_id"`
	Version     string            `json:"version"`
	Repository  string            `json:"repository"`
	RunnerImage string            `json:"runner_image"`
	WeightsURI  string            `json:"weights_uri,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}
