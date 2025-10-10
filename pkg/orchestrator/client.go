package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client interacts with the internal orchestrator service over HTTP.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new orchestrator client with sane defaults.
func NewClient(baseURL string) *Client {
	trimmed := strings.TrimSuffix(baseURL, "/")
	return &Client{
		baseURL: trimmed,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// SubmitJobRequest is the payload sent to orchestrator for queue submissions.
type SubmitJobRequest struct {
	ModelID    string          `json:"model_id"`
	VersionID  *string         `json:"version_id,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	StoreIO    bool            `json:"store_io"`
	FalWebhook *string         `json:"fal_webhook,omitempty"`
}

// SubmitJobResponse contains the request ID issued by the orchestrator.
type SubmitJobResponse struct {
	RequestID string `json:"request_id"`
}

// SubmitJob enqueues a new job for a given model.
func (c *Client) SubmitJob(ctx context.Context, req SubmitJobRequest) (SubmitJobResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return SubmitJobResponse{}, fmt.Errorf("marshal submit request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v1/internal/jobs", c.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return SubmitJobResponse{}, fmt.Errorf("create orchestrator request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return SubmitJobResponse{}, fmt.Errorf("submit job: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return SubmitJobResponse{}, fmt.Errorf("submit job failed: %s", strings.TrimSpace(string(payload)))
	}

	var out SubmitJobResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return SubmitJobResponse{}, fmt.Errorf("decode orchestrator response: %w", err)
	}

	return out, nil
}

// JobDetails encapsulates job state returned by orchestrator.
type JobDetails struct {
	RequestID     string            `json:"request_id"`
	ModelID       string            `json:"model_id"`
	VersionID     *string           `json:"version_id,omitempty"`
	NodeID        *string           `json:"node_id,omitempty"`
	Status        string            `json:"status"`
	QueuePosition *int              `json:"queue_position,omitempty"`
	Logs          []string          `json:"logs,omitempty"`
	Result        json.RawMessage   `json:"result,omitempty"`
	Artifacts     []json.RawMessage `json:"artifacts,omitempty"`
}

// GetJob fetches job state for status or result retrieval.
func (c *Client) GetJob(ctx context.Context, requestID string) (JobDetails, error) {
	endpoint := fmt.Sprintf("%s/v1/internal/jobs/%s", c.baseURL, requestID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return JobDetails{}, fmt.Errorf("create get job request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return JobDetails{}, fmt.Errorf("get job: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return JobDetails{}, ErrNotFound
	}

	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return JobDetails{}, fmt.Errorf("get job failed: %s", strings.TrimSpace(string(payload)))
	}

	var details JobDetails
	if err := json.NewDecoder(resp.Body).Decode(&details); err != nil {
		return JobDetails{}, fmt.Errorf("decode job details: %w", err)
	}

	return details, nil
}

// StreamStatus opens an SSE stream for job status updates.
func (c *Client) StreamStatus(ctx context.Context, requestID string) (*http.Response, error) {
	endpoint := fmt.Sprintf("%s/v1/internal/jobs/%s/stream", c.baseURL, requestID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create stream request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("stream status: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		return nil, fmt.Errorf("stream status failed: %s", strings.TrimSpace(string(payload)))
	}

	return resp, nil
}

// CancelResponse represents orchestrator cancellation outcome.
type CancelResponse struct {
	Status string `json:"status"`
}

// CancelJob requests cancellation for a queued job.
func (c *Client) CancelJob(ctx context.Context, requestID string) (CancelResponse, error) {
	endpoint := fmt.Sprintf("%s/v1/internal/jobs/%s/cancel", c.baseURL, requestID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, nil)
	if err != nil {
		return CancelResponse{}, fmt.Errorf("create cancel request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return CancelResponse{}, fmt.Errorf("cancel job: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return CancelResponse{}, ErrNotFound
	}

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusBadRequest {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return CancelResponse{}, fmt.Errorf("cancel job failed: %s", strings.TrimSpace(string(payload)))
	}

	var out CancelResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return CancelResponse{}, fmt.Errorf("decode cancel response: %w", err)
	}

	return out, nil
}

// SyncRequest requests synchronous execution of a model.
type SyncRequest struct {
	ModelID string          `json:"model_id"`
	Payload json.RawMessage `json:"payload"`
}

// SyncResponse is returned by orchestrator for synchronous invocations.
type SyncResponse struct {
	Result json.RawMessage `json:"result"`
	Logs   []string        `json:"logs"`
}

// InvokeSync runs a synchronous model inference through orchestrator.
func (c *Client) InvokeSync(ctx context.Context, req SyncRequest) (SyncResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return SyncResponse{}, fmt.Errorf("marshal sync request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v1/internal/sync", c.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return SyncResponse{}, fmt.Errorf("create sync request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return SyncResponse{}, fmt.Errorf("invoke sync: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return SyncResponse{}, fmt.Errorf("sync failed: %s", strings.TrimSpace(string(payload)))
	}

	var out SyncResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return SyncResponse{}, fmt.Errorf("decode sync response: %w", err)
	}

	return out, nil
}

// ErrNotFound is returned when the orchestrator reports missing resources.
var ErrNotFound = fmt.Errorf("resource not found")

// ParseSSEEvent extracts JSON payloads from SSE streams.
func ParseSSEEvent(lines []string) (json.RawMessage, bool) {
	for _, line := range lines {
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" {
				continue
			}
			return json.RawMessage(payload), true
		}
	}
	return nil, false
}

// CopyHeaders copies selected headers from source to destination writer.
func CopyHeaders(src http.Header, dst http.ResponseWriter, keys ...string) {
	for _, key := range keys {
		if values := src.Values(key); len(values) > 0 {
			for _, v := range values {
				dst.Header().Add(key, v)
			}
		}
	}
}

// ReadEvents streams SSE events, invoking callback for each completed event.
func ReadEvents(body io.Reader, eventFn func(json.RawMessage) error) error {
	reader := bufio.NewReader(body)
	var lines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(lines) > 0 {
					if err := dispatchEvent(lines, eventFn); err != nil {
						return err
					}
				}
				return nil
			}
			return err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			if err := dispatchEvent(lines, eventFn); err != nil {
				return err
			}
			lines = lines[:0]
			continue
		}
		lines = append(lines, trimmed)
	}
}

func dispatchEvent(lines []string, eventFn func(json.RawMessage) error) error {
	if len(lines) == 0 {
		return nil
	}
	payload, ok := ParseSSEEvent(lines)
	if !ok {
		return nil
	}
	return eventFn(payload)
}
