package controlplane

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
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
ALTER TABLE control_plane_nodes ADD COLUMN IF NOT EXISTS ssh_password TEXT;
ALTER TABLE control_plane_nodes ADD COLUMN IF NOT EXISTS ssh_private_key TEXT;
ALTER TABLE control_plane_nodes ADD COLUMN IF NOT EXISTS hf_token TEXT;
ALTER TABLE control_plane_nodes ADD COLUMN IF NOT EXISTS dashscope_api_key TEXT;
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
	now := time.Now().UTC()
	if node.ID == "" {
		node.ID = uuid.NewString()
	}
	if node.CreatedAt.IsZero() {
		node.CreatedAt = now
	}
	node.UpdatedAt = now
	if node.Status == "" {
		node.Status = NodeStatusPending
	}
	if node.SSHPort == 0 {
		node.SSHPort = 22
	}
	if strings.TrimSpace(node.TorchDType) == "" {
		node.TorchDType = "float16"
	}
	if strings.TrimSpace(node.Device) == "" {
		node.Device = "cuda"
	}
	copyNode := *node
	if err := s.saveNode(&copyNode); err != nil {
		return nil, err
	}
	s.AppendEvent(copyNode.ID, NodeStatusPending, "Node registered")
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
	var (
		raw             []byte
		sshPassword     sql.NullString
		sshPrivateKey   sql.NullString
		hfToken         sql.NullString
		dashscopeAPIKey sql.NullString
	)
	err := s.db.QueryRow(`SELECT data, ssh_password, ssh_private_key, hf_token, dashscope_api_key FROM control_plane_nodes WHERE id=$1`, id).
		Scan(&raw, &sshPassword, &sshPrivateKey, &hfToken, &dashscopeAPIKey)
	if err != nil {
		return nil, false
	}
	var node Node
	if err := json.Unmarshal(raw, &node); err != nil {
		return nil, false
	}
	if sshPassword.Valid {
		node.SSHPassword = sshPassword.String
	}
	if sshPrivateKey.Valid {
		node.SSHPrivateKey = sshPrivateKey.String
	}
	if hfToken.Valid {
		node.HFToken = hfToken.String
	}
	if dashscopeAPIKey.Valid {
		node.DashScopeAPIKey = dashscopeAPIKey.String
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
		modelPresent := false
		for _, model := range n.Models {
			if strings.EqualFold(model, assignment.ModelID) {
				modelPresent = true
				break
			}
		}
		if !modelPresent {
			n.Models = append(n.Models, assignment.ModelID)
		}
		return nil
	})
}

func (s *PostgresStore) FindAssignmentForModel(modelID string) (*Node, *ModelAssignment) {
	rows, err := s.db.Query(`SELECT data FROM control_plane_nodes WHERE data->>'status' = 'READY' ORDER BY updated_at DESC`)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()

	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var node Node
		if err := json.Unmarshal(raw, &node); err != nil {
			continue
		}
		for _, assignment := range node.Assignments {
			if strings.EqualFold(assignment.ModelID, modelID) {
				nCopy := node
				aCopy := assignment
				return &nCopy, &aCopy
			}
		}
	}
	return nil, nil
}

func (s *PostgresStore) saveNode(node *Node) error {
	bytes, err := json.Marshal(node)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO control_plane_nodes (
	    id,
	    data,
	    ssh_password,
	    ssh_private_key,
	    hf_token,
	    dashscope_api_key,
	    created_at,
	    updated_at
)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (id) DO UPDATE SET
	data = EXCLUDED.data,
	ssh_password = EXCLUDED.ssh_password,
	ssh_private_key = EXCLUDED.ssh_private_key,
	hf_token = EXCLUDED.hf_token,
	dashscope_api_key = EXCLUDED.dashscope_api_key,
	updated_at = EXCLUDED.updated_at`,
		node.ID,
		bytes,
		node.SSHPassword,
		node.SSHPrivateKey,
		node.HFToken,
		node.DashScopeAPIKey,
		node.CreatedAt,
		node.UpdatedAt,
	)
	return err
}

var _ Repository = (*PostgresStore)(nil)
