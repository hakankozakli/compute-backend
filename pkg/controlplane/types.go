package controlplane

import "time"

// NodeStatus represents the lifecycle state of a compute node.
type NodeStatus string

const (
	NodeStatusPending      NodeStatus = "PENDING"
	NodeStatusProvisioning NodeStatus = "PROVISIONING"
	NodeStatusReady        NodeStatus = "READY"
	NodeStatusError        NodeStatus = "ERROR"
)

// Node describes a managed GPU host.
type Node struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	GPUType           string            `json:"gpuType"`
	IPAddress         string            `json:"ipAddress"`
	SSHUsername       string            `json:"sshUsername"`
	SSHPassword       string            `json:"-"`
	SSHPrivateKey     string            `json:"-"`
	SSHPort           int               `json:"sshPort"`
	Models            []string          `json:"models"`
	Assignments       []ModelAssignment `json:"assignments"`
	HFToken           string            `json:"-"`
	DashScopeAPIKey   string            `json:"-"`
	TorchDType        string            `json:"torchDtype"`
	Device            string            `json:"device"`
	EnableXformers    bool              `json:"enableXformers"`
	EnableTF32        bool              `json:"enableTF32"`
	TrustRemoteCode   bool              `json:"trustRemoteCode"`
	Status            NodeStatus        `json:"status"`
	LastError         string            `json:"lastError"`
	CreatedAt         time.Time         `json:"createdAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`
	LastProvisionedAt *time.Time        `json:"lastProvisionedAt,omitempty"`
}

// NodeEvent captures provisioning progress for a node.
type NodeEvent struct {
	ID        string     `json:"id"`
	NodeID    string     `json:"nodeId"`
	Status    NodeStatus `json:"status"`
	Message   string     `json:"message"`
	CreatedAt time.Time  `json:"createdAt"`
}

// SanitizedNode removes sensitive credentials from an API response.
type SanitizedNode struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	GPUType           string            `json:"gpuType"`
	IPAddress         string            `json:"ipAddress"`
	SSHUsername       string            `json:"sshUsername"`
	SSHPort           int               `json:"sshPort"`
	Models            []string          `json:"models"`
	Assignments       []ModelAssignment `json:"assignments"`
	TorchDType        string            `json:"torchDtype"`
	Device            string            `json:"device"`
	EnableXformers    bool              `json:"enableXformers"`
	EnableTF32        bool              `json:"enableTF32"`
	TrustRemoteCode   bool              `json:"trustRemoteCode"`
	Status            NodeStatus        `json:"status"`
	LastError         string            `json:"lastError"`
	CreatedAt         time.Time         `json:"createdAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`
	LastProvisionedAt *time.Time        `json:"lastProvisionedAt,omitempty"`
}

// Sanitized converts a node to its API-safe representation.
func (n *Node) Sanitized() SanitizedNode {
	return SanitizedNode{
		ID:                n.ID,
		Name:              n.Name,
		GPUType:           n.GPUType,
		IPAddress:         n.IPAddress,
		SSHUsername:       n.SSHUsername,
		SSHPort:           n.SSHPort,
		Models:            append([]string(nil), n.Models...),
		Assignments:       append([]ModelAssignment(nil), n.Assignments...),
		TorchDType:        n.TorchDType,
		Device:            n.Device,
		EnableXformers:    n.EnableXformers,
		EnableTF32:        n.EnableTF32,
		TrustRemoteCode:   n.TrustRemoteCode,
		Status:            n.Status,
		LastError:         n.LastError,
		CreatedAt:         n.CreatedAt,
		UpdatedAt:         n.UpdatedAt,
		LastProvisionedAt: n.LastProvisionedAt,
	}
}

// ModelAssignment captures which model/version a node should host.
type ModelAssignment struct {
	ModelID    string    `json:"modelId"`
	VersionID  string    `json:"versionId"`
	WeightsURI string    `json:"weightsUri,omitempty"`
	UpdatedAt  time.Time `json:"updatedAt"`
}
