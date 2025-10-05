package falcompat

// QueueStatus enumerates the statuses shared with fal.ai.
type QueueStatus string

const (
    // StatusInQueue indicates the job is waiting in queue.
    StatusInQueue QueueStatus = "IN_QUEUE"
    // StatusInProgress indicates the job is executing.
    StatusInProgress QueueStatus = "IN_PROGRESS"
    // StatusCompleted indicates the job completed successfully.
    StatusCompleted QueueStatus = "COMPLETED"
    // StatusError indicates the job failed.
    StatusError QueueStatus = "ERROR"
)

// SubmissionEnvelope is the canonical response for queue submissions.
type SubmissionEnvelope struct {
    RequestID   string `json:"request_id"`
    ResponseURL string `json:"response_url"`
    StatusURL   string `json:"status_url"`
    CancelURL   string `json:"cancel_url"`
}

// StatusResponse represents the polling response contract.
type StatusResponse struct {
    Status        QueueStatus `json:"status"`
    QueuePosition *int        `json:"queue_position,omitempty"`
    ResponseURL   string      `json:"response_url"`
    Logs          []string    `json:"logs,omitempty"`
}

// ErrorDetail mirrors the validation error envelope fal.ai returns.
type ErrorDetail struct {
    Type string   `json:"type"`
    Loc  []string `json:"loc"`
    Msg  string   `json:"msg"`
    Ctx  any      `json:"ctx,omitempty"`
}

// ValidationError wraps Pydantic-style error lists.
type ValidationError struct {
    Detail []ErrorDetail `json:"detail"`
}
