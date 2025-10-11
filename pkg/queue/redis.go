package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type JobStatus string

const (
	StatusPending    JobStatus = "pending"
	StatusProcessing JobStatus = "processing"
	StatusCompleted  JobStatus = "completed"
	StatusFailed     JobStatus = "failed"
)

type Job struct {
	ID          string                 `json:"id"`
	Model       string                 `json:"model"`
	Status      JobStatus              `json:"status"`
	Params      map[string]interface{} `json:"params"`
	WorkerID    string                 `json:"worker_id,omitempty"`
	NodeID      string                 `json:"node_id,omitempty"`
	CreatedAt   int64                  `json:"created_at"`
	StartedAt   int64                  `json:"started_at,omitempty"`
	CompletedAt int64                  `json:"completed_at,omitempty"`
	Result      interface{}            `json:"result,omitempty"`
	Error       string                 `json:"error,omitempty"`
}

type Queue struct {
	redis *redis.Client
}

func NewQueue(redisURL string) (*Queue, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid redis URL: %w", err)
	}

	client := redis.NewClient(opt)
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	return &Queue{redis: client}, nil
}

func (q *Queue) EnqueueJob(ctx context.Context, job *Job) error {
	job.CreatedAt = time.Now().Unix()
	job.Status = StatusPending

	// Save job data
	jobKey := fmt.Sprintf("job:%s", job.ID)
	jobData, err := json.Marshal(job)
	if err != nil {
		return err
	}

	if err := q.redis.Set(ctx, jobKey, jobData, 24*time.Hour).Err(); err != nil {
		return err
	}

	// Add to queues
	if strings.TrimSpace(job.NodeID) != "" {
		nodeQueue := fmt.Sprintf("queue:%s:%s", job.NodeID, job.Model)
		if err := q.redis.RPush(ctx, nodeQueue, job.ID).Err(); err != nil {
			return err
		}
	}
	queueKey := fmt.Sprintf("queue:%s", job.Model)
	return q.redis.RPush(ctx, queueKey, job.ID).Err()
}

func (q *Queue) DequeueJob(ctx context.Context, model string, workerID string) (*Job, error) {
	queueKey := fmt.Sprintf("queue:%s", model)

	// Blocking pop with timeout
	result, err := q.redis.BLPop(ctx, 5*time.Second, queueKey).Result()
	if err == redis.Nil {
		return nil, nil // No jobs available
	}
	if err != nil {
		return nil, err
	}

	jobID := result[1]
	jobKey := fmt.Sprintf("job:%s", jobID)

	// Get job data
	jobData, err := q.redis.Get(ctx, jobKey).Bytes()
	if err != nil {
		return nil, err
	}

	var job Job
	if err := json.Unmarshal(jobData, &job); err != nil {
		return nil, err
	}

	// Update job status
	job.Status = StatusProcessing
	job.WorkerID = workerID
	job.StartedAt = time.Now().Unix()

	updatedData, _ := json.Marshal(job)
	q.redis.Set(ctx, jobKey, updatedData, 24*time.Hour)

	return &job, nil
}

func (q *Queue) CompleteJob(ctx context.Context, jobID string, result interface{}) error {
	jobKey := fmt.Sprintf("job:%s", jobID)

	jobData, err := q.redis.Get(ctx, jobKey).Bytes()
	if err != nil {
		return err
	}

	var job Job
	if err := json.Unmarshal(jobData, &job); err != nil {
		return err
	}

	job.Status = StatusCompleted
	job.CompletedAt = time.Now().Unix()
	job.Result = result

	updatedData, _ := json.Marshal(job)
	return q.redis.Set(ctx, jobKey, updatedData, 24*time.Hour).Err()
}

func (q *Queue) FailJob(ctx context.Context, jobID string, errorMsg string) error {
	jobKey := fmt.Sprintf("job:%s", jobID)

	jobData, err := q.redis.Get(ctx, jobKey).Bytes()
	if err != nil {
		return err
	}

	var job Job
	if err := json.Unmarshal(jobData, &job); err != nil {
		return err
	}

	job.Status = StatusFailed
	job.CompletedAt = time.Now().Unix()
	job.Error = errorMsg

	updatedData, _ := json.Marshal(job)
	return q.redis.Set(ctx, jobKey, updatedData, 24*time.Hour).Err()
}

func (q *Queue) GetJob(ctx context.Context, jobID string) (*Job, error) {
	jobKey := fmt.Sprintf("job:%s", jobID)

	jobData, err := q.redis.Get(ctx, jobKey).Bytes()
	if err == redis.Nil {
		return nil, fmt.Errorf("job not found")
	}
	if err != nil {
		return nil, err
	}

	var job Job
	if err := json.Unmarshal(jobData, &job); err != nil {
		return nil, err
	}

	return &job, nil
}

func (q *Queue) GetQueueLength(ctx context.Context, model string) (int64, error) {
	queueKey := fmt.Sprintf("queue:%s", model)
	return q.redis.LLen(ctx, queueKey).Result()
}

func (q *Queue) Close() error {
	return q.redis.Close()
}
