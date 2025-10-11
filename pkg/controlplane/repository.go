package controlplane

// Repository defines the storage operations required by the control plane server.
type Repository interface {
	CreateNode(node *Node) (*Node, error)
	ListNodes() []SanitizedNode
	GetNode(id string) (*Node, bool)
	UpdateNode(id string, fn func(n *Node) error) (*Node, error)
	UpdateStatus(id string, status NodeStatus, message string) (*Node, error)
	DeleteNode(id string) error
	GetEvents(id string) []NodeEvent
	AppendEvent(id string, status NodeStatus, message string)
	Credentials(id string) (username, password string, ok bool)
	UpdateSecrets(id, password, hfToken, dashscope string) error
	UpsertAssignment(id string, assignment ModelAssignment) (*Node, error)
	FindAssignmentForModel(modelID string) (*Node, *ModelAssignment)
}

var _ Repository = (*Store)(nil)
