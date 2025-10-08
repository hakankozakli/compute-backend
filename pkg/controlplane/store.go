package controlplane

import (
	encodingjson "encoding/json"
	errors "errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Store persists node metadata and provisioning events.
type Store struct {
	path   string
	mu     sync.RWMutex
	nodes  map[string]*Node
	events map[string][]NodeEvent
}

type persistContainer struct {
	Nodes  []*persistedNode `json:"nodes"`
	Events [][]NodeEvent    `json:"events"`
}

type persistedNode struct {
	*Node
	SSHPassword     string `json:"sshPassword"`
	SSHPrivateKey   string `json:"sshPrivateKey"`
	HFToken         string `json:"hfToken"`
	DashScopeAPIKey string `json:"dashScopeApiKey"`
}

func NewStore(path string) (*Store, error) {
	s := &Store{
		path:   path,
		nodes:  make(map[string]*Node),
		events: make(map[string][]NodeEvent),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	if s.path == "" {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var container persistContainer
	if err := encodingjson.Unmarshal(data, &container); err != nil {
		return fmt.Errorf("parse node store: %w", err)
	}
	for idx, pn := range container.Nodes {
		if pn.Node == nil {
			continue
		}
		n := *pn.Node
		n.Assignments = append([]ModelAssignment(nil), pn.Node.Assignments...)
		n.SSHPassword = pn.SSHPassword
		n.SSHPrivateKey = pn.SSHPrivateKey
		n.HFToken = pn.HFToken
		n.DashScopeAPIKey = pn.DashScopeAPIKey
		s.nodes[n.ID] = &n
		if idx < len(container.Events) {
			s.events[n.ID] = container.Events[idx]
		}
	}
	return nil
}

func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	container := persistContainer{}
	for _, node := range s.nodes {
		copyNode := *node
		copyNode.Assignments = append([]ModelAssignment(nil), node.Assignments...)
		container.Nodes = append(container.Nodes, &persistedNode{
			Node:            &copyNode,
			SSHPassword:     node.SSHPassword,
			SSHPrivateKey:   node.SSHPrivateKey,
			HFToken:         node.HFToken,
			DashScopeAPIKey: node.DashScopeAPIKey,
		})
		container.Events = append(container.Events, append([]NodeEvent(nil), s.events[node.ID]...))
	}
	payload, err := encodingjson.MarshalIndent(container, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) CreateNode(node *Node) (*Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	node.ID = uuid.NewString()
	node.Status = NodeStatusPending
	node.CreatedAt = now
	node.UpdatedAt = now

	if node.SSHPort == 0 {
		node.SSHPort = 22
	}
	if strings.TrimSpace(node.TorchDType) == "" {
		node.TorchDType = "float16"
	}
	if strings.TrimSpace(node.Device) == "" {
		node.Device = "cuda"
	}

	s.nodes[node.ID] = node
	s.events[node.ID] = append(s.events[node.ID], NodeEvent{
		ID:        uuid.NewString(),
		NodeID:    node.ID,
		Status:    NodeStatusPending,
		Message:   "Node registered",
		CreatedAt: now,
	})

	if err := s.save(); err != nil {
		return nil, err
	}
	return node, nil
}

func (s *Store) ListNodes() []SanitizedNode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]SanitizedNode, 0, len(s.nodes))
	for _, node := range s.nodes {
		result = append(result, node.Sanitized())
	}
	return result
}

func (s *Store) GetNode(id string) (*Node, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	node, ok := s.nodes[id]
	if !ok {
		return nil, false
	}
	copyNode := *node
	return &copyNode, true
}

func (s *Store) GetEvents(id string) []NodeEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]NodeEvent(nil), s.events[id]...)
}

func (s *Store) updateNode(id string, fn func(n *Node) error) (*Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[id]
	if !ok {
		return nil, fmt.Errorf("node %s not found", id)
	}
	if err := fn(node); err != nil {
		return nil, err
	}
	node.UpdatedAt = time.Now().UTC()
	if err := s.save(); err != nil {
		return nil, err
	}
	copyNode := *node
	return &copyNode, nil
}

func (s *Store) UpdateNode(id string, fn func(n *Node) error) (*Node, error) {
	return s.updateNode(id, fn)
}

func (s *Store) UpdateStatus(id string, status NodeStatus, message string) (*Node, error) {
	node, err := s.updateNode(id, func(n *Node) error {
		n.Status = status
		if status == NodeStatusError {
			n.LastError = message
		} else if status == NodeStatusReady {
			n.LastError = ""
			now := time.Now().UTC()
			n.LastProvisionedAt = &now
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.appendEvent(id, NodeEvent{
		ID:        uuid.NewString(),
		NodeID:    id,
		Status:    status,
		Message:   message,
		CreatedAt: time.Now().UTC(),
	})
	return node, nil
}

func (s *Store) appendEvent(id string, event NodeEvent) {
	s.mu.Lock()
	s.events[id] = append(s.events[id], event)
	_ = s.save()
	s.mu.Unlock()
}

func (s *Store) AppendEvent(id string, status NodeStatus, message string) {
	s.appendEvent(id, NodeEvent{
		ID:        uuid.NewString(),
		NodeID:    id,
		Status:    status,
		Message:   message,
		CreatedAt: time.Now().UTC(),
	})
}

func (s *Store) Credentials(id string) (username, password string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	node, ok := s.nodes[id]
	if !ok {
		return "", "", false
	}
	return node.SSHUsername, node.SSHPassword, true
}

func (s *Store) UpsertAssignment(id string, assignment ModelAssignment) (*Node, error) {
	return s.updateNode(id, func(n *Node) error {
		replaced := false
		for idx, existing := range n.Assignments {
			if existing.ModelID == assignment.ModelID {
				n.Assignments[idx] = assignment
				replaced = true
				break
			}
		}
		if !replaced {
			n.Assignments = append(n.Assignments, assignment)
		}
		return nil
	})
}

func (s *Store) UpdateSecrets(id, password, hfToken, dashscope string) error {
	_, err := s.updateNode(id, func(n *Node) error {
		if password != "" {
			n.SSHPassword = password
		}
		if hfToken != "" {
			n.HFToken = hfToken
		}
		if dashscope != "" {
			n.DashScopeAPIKey = dashscope
		}
		return nil
	})
	return err
}

func (s *Store) SetNode(node *Node) error {
	s.mu.Lock()
	s.nodes[node.ID] = node
	s.mu.Unlock()
	return s.save()
}

func (s *Store) DeleteNode(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[id]; !ok {
		return fmt.Errorf("node %s not found", id)
	}
	delete(s.nodes, id)
	delete(s.events, id)
	return s.save()
}

func NormalizePrivateKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	return key + "\n"
}
