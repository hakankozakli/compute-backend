package controlplane

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(dsn string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres connection: %w", err)
	}
	db.SetMaxIdleConns(5)
	db.SetMaxOpenConns(10)
	db.SetConnMaxLifetime(time.Hour)

	s := &PostgresStore{db: db}
	if err := s.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return s, nil
}

func (s *PostgresStore) ensureSchema() error {
	schema := `
CREATE TABLE IF NOT EXISTS control_plane_nodes (
    id TEXT PRIMARY KEY,
    data JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS control_plane_events (
    id TEXT PRIMARY KEY,
    node_id TEXT NOT NULL REFERENCES control_plane_nodes(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    message TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);
`
	_, err := s.db.Exec(schema)
	return err
}

func (s *PostgresStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *PostgresStore) CreateNode(node *Node) (*Node, error) {
	if node.ID == "" {
		return nil, fmt.Errorf("node must have ID")
	}
	copyNode := *node
	if err := s.saveNode(&copyNode); err != nil {
		return nil, err
	}
	return &copyNode, nil
}

func (s *PostgresStore) ListNodes() []SanitizedNode {
	rows, err := s.db.Query(`SELECT data FROM control_plane_nodes ORDER BY created_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var nodes []SanitizedNode
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var node Node
		if err := json.Unmarshal(raw, &node); err != nil {
			continue
		}
		nodes = append(nodes, node.Sanitized())
	}
	return nodes
}

func (s *PostgresStore) GetNode(id string) (*Node, bool) {
	var raw []byte
	err := s.db.QueryRow(`SELECT data FROM control_plane_nodes WHERE id=$1`, id).Scan(&raw)
	if err != nil {
		return nil, false
	}
	var node Node
	if err := json.Unmarshal(raw, &node); err != nil {
		return nil, false
	}
	return &node, true
}

func (s *PostgresStore) UpdateNode(id string, fn func(n *Node) error) (*Node, error) {
	node, ok := s.GetNode(id)
	if !ok {
		return nil, fmt.Errorf("node %s not found", id)
	}
	if err := fn(node); err != nil {
		return nil, err
	}
	node.UpdatedAt = time.Now().UTC()
	if err := s.saveNode(node); err != nil {
		return nil, err
	}
	return node, nil
}

func (s *PostgresStore) UpdateStatus(id string, status NodeStatus, message string) (*Node, error) {
	node, err := s.UpdateNode(id, func(n *Node) error {
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
	s.AppendEvent(id, status, message)
	return node, nil
}

func (s *PostgresStore) DeleteNode(id string) error {
	_, err := s.db.Exec(`DELETE FROM control_plane_nodes WHERE id=$1`, id)
	return err
}

func (s *PostgresStore) GetEvents(id string) []NodeEvent {
	rows, err := s.db.Query(`SELECT id, status, message, created_at FROM control_plane_events WHERE node_id=$1 ORDER BY created_at ASC`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var events []NodeEvent
	for rows.Next() {
		var ev NodeEvent
		if err := rows.Scan(&ev.ID, &ev.Status, &ev.Message, &ev.CreatedAt); err != nil {
			continue
		}
		ev.NodeID = id
		events = append(events, ev)
	}
	return events
}

func (s *PostgresStore) AppendEvent(id string, status NodeStatus, message string) {
	eventID := uuid.NewString()
	_, err := s.db.Exec(`INSERT INTO control_plane_events (id, node_id, status, message, created_at) VALUES ($1,$2,$3,$4,$5)`, eventID, id, status, message, time.Now().UTC())
	if err != nil {
		fmt.Printf("append event error: %v\n", err)
	}
}

func (s *PostgresStore) Credentials(id string) (username, password string, ok bool) {
	node, found := s.GetNode(id)
	if !found {
		return "", "", false
	}
	return node.SSHUsername, node.SSHPassword, true
}

func (s *PostgresStore) UpdateSecrets(id, password, hfToken, dashscope string) error {
	_, err := s.UpdateNode(id, func(n *Node) error {
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

func (s *PostgresStore) UpsertAssignment(id string, assignment ModelAssignment) (*Node, error) {
	return s.UpdateNode(id, func(n *Node) error {
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

func (s *PostgresStore) saveNode(node *Node) error {
	bytes, err := json.Marshal(node)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO control_plane_nodes (id, data, created_at, updated_at)
VALUES ($1,$2,$3,$4)
ON CONFLICT (id) DO UPDATE SET data = EXCLUDED.data, updated_at = EXCLUDED.updated_at`, node.ID, bytes, node.CreatedAt, node.UpdatedAt)
	return err
}

var _ Repository = (*PostgresStore)(nil)
