package protocol

// Connection is controller configuration, never part of the agent's identity.
type Connection struct {
	ID           string       `json:"id"`
	DeviceID     string       `json:"device_id"`
	Kind         string       `json:"kind"`
	SSH          *SSHConfig   `json:"ssh,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
	Online       bool         `json:"online"`
	LastError    string       `json:"last_error,omitempty"`
}

// SSH uses the controller's OpenSSH key/agent authentication and known_hosts.
type SSHConfig struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	User           string `json:"user"`
	IdentityFile   string `json:"identity_file,omitempty"`
	KnownHostsFile string `json:"known_hosts_file,omitempty"`
}

type DeviceCreate struct {
	Name      string     `json:"name"`
	Workspace string     `json:"workspace"`
	SSH       *SSHConfig `json:"ssh"`
}
