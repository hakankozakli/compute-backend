package builder

import (
	"errors"
	"sync"
	"time"
)

type subscriber chan string

type buildRecord struct {
	build       Build
	subscribers []subscriber
	logs        []string
}

// MemStore keeps build records in memory and supports log subscriptions.
type MemStore struct {
	mu    sync.RWMutex
	items map[string]*buildRecord
}

func NewMemStore() *MemStore {
	return &MemStore{items: make(map[string]*buildRecord)}
}

func (s *MemStore) Create(build Build) Build {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := &buildRecord{build: build}
	s.items[build.ID] = rec
	return rec.build
}

func (s *MemStore) SetStatus(id string, status Status, finished bool, finishedAt time.Time, errMsg string) (Build, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.items[id]
	if !ok {
		return Build{}, errors.New("build not found")
	}
	rec.build.Status = status
	rec.build.UpdatedAt = time.Now().UTC()
	if finished {
		rec.build.FinishedAt = finishedAt
	}
	rec.build.Error = errMsg
	return rec.build, nil
}

func (s *MemStore) AppendLog(id string, line string) {
	s.mu.RLock()
	rec, ok := s.items[id]
	s.mu.RUnlock()
	if !ok {
		return
	}

	s.mu.Lock()
	rec.logs = append(rec.logs, line)
	s.mu.Unlock()

	s.Broadcast(id, line)
}

func (s *MemStore) Get(id string) (Build, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, ok := s.items[id]
	if !ok {
		return Build{}, errors.New("build not found")
	}
	return rec.build, nil
}

func (s *MemStore) List() []Build {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Build, 0, len(s.items))
	for _, rec := range s.items {
		result = append(result, rec.build)
	}
	return result
}

func (s *MemStore) Subscribe(id string) (<-chan string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.items[id]
	if !ok {
		return nil, errors.New("build not found")
	}

	ch := make(subscriber, 32)
	rec.subscribers = append(rec.subscribers, ch)
	for _, line := range rec.logs {
		ch <- line
	}
	return ch, nil
}

func (s *MemStore) Broadcast(id string, message string) {
	s.mu.RLock()
	rec, ok := s.items[id]
	s.mu.RUnlock()
	if !ok {
		return
	}

	for _, sub := range rec.subscribers {
		select {
		case sub <- message:
		default:
		}
	}
}

func (s *MemStore) CloseSubscribers(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.items[id]
	if !ok {
		return
	}
	for _, sub := range rec.subscribers {
		close(sub)
	}
	rec.subscribers = nil
}
