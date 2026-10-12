package protocol

import (
	"encoding/json"
	"time"
)

// Connection is controller configuration, never part of the agent's identity.
type Connection struct {
	ID           string            `json:"id"`
	DeviceID     string            `json:"device_id"`
	Kind         string            `json:"kind"`
	Revision     string            `json:"revision"`
	OS           string            `json:"os,omitempty"`
	Architecture string            `json:"architecture,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	LastSeen     time.Time         `json:"last_seen"`
	Workspace    WorkspacePolicy   `json:"workspace"`
	SSH          *SSHConfig        `json:"ssh,omitempty"`
	Options      json.RawMessage   `json:"options,omitempty"`
	Capabilities Capabilities      `json:"capabilities"`
	Online       bool              `json:"online"`
	LastError    string            `json:"last_error,omitempty"`
}

// SSH uses the controller's OpenSSH key/agent authentication and known_hosts.
type SSHConfig struct {
	Platform       string `json:"platform,omitempty"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	User           string `json:"user"`
	IdentityFile   string `json:"identity_file,omitempty"`
	KnownHostsFile string `json:"known_hosts_file,omitempty"`
}

type DeviceCreate struct {
	Name       string         `json:"name"`
	Connection ConnectionSpec `json:"connection"`
}

// ConnectionSpec configures a device's sole transport and workspace policy.
type ConnectionSpec struct {
	Kind      string          `json:"kind"`
	SSH       *SSHConfig      `json:"ssh,omitempty"`
	Options   json.RawMessage `json:"options,omitempty"`
	Workspace WorkspacePolicy `json:"workspace"`
}

const (
	WorkspaceLocalSync = "local_sync"
	WorkspaceRemote    = "remote"
)

type WorkspacePolicy struct {
	Mode     string            `json:"mode"`
	Root     string            `json:"root"`
	Mappings map[string]string `json:"mappings,omitempty"`
}

// ConnectionReplacement is an explicit user action. Worker requires a pending
// pairing request; other adapters use a fresh connection specification.
type ConnectionReplacement struct {
	Connection *ConnectionSpec `json:"connection,omitempty"`
	PairingID  string          `json:"pairing_id,omitempty"`
}

// Activity is private transport state used to guard connection replacement.
type Activity struct {
	Requests int64 `json:"requests"`
	Tasks    int   `json:"tasks"`
	Sessions int   `json:"sessions"`
	Syncs    int   `json:"syncs"`
}

func (a Activity) Busy() bool {
	return a.Requests != 0 || a.Tasks != 0 || a.Sessions != 0 || a.Syncs != 0
}
