package protocol

import "time"

const APIVersion = "v1"

type Device struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	OS           string            `json:"os"`
	OSVersion    string            `json:"os_version,omitempty"`
	Architecture string            `json:"architecture"`
	Authorized   bool              `json:"authorized"`
	Workspace    string            `json:"workspace"`
	Mappings     map[string]string `json:"workspace_mappings,omitempty"`
	Capabilities Capabilities      `json:"capabilities"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	LastSeen     time.Time         `json:"last_seen"`
	Online       bool              `json:"online"`
}

type Capabilities struct {
	Desktop    *DesktopCapability `json:"desktop,omitempty"`
	Exec       bool               `json:"exec"`
	Files      bool               `json:"files"`
	Tasks      bool               `json:"tasks"`
	Sessions   bool               `json:"sessions"`
	Shells     []string           `json:"shells,omitempty"`
	MaxPayload int64              `json:"max_payload_bytes,omitempty"`
}

type Health struct {
	Status     string `json:"status"`
	APIVersion string `json:"api_version"`
	UptimeMS   int64  `json:"uptime_ms"`
}

type ExecRequest struct {
	Command    string            `json:"command"`
	WorkingDir string            `json:"working_dir,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	TimeoutMS  int64             `json:"timeout_ms,omitempty"`
}

type ExecResponse struct {
	OutputTruncated bool   `json:"output_truncated,omitempty"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	ExitCode        int    `json:"exit_code"`
	DurationMS      int64  `json:"duration_ms"`
	TimedOut        bool   `json:"timed_out,omitempty"`
}

const (
	SyncModeUpdate = "update"
	SyncModeMirror = "mirror"
)

type SyncManifestEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode,omitempty"`
}

type SyncIgnorePattern struct {
	Pattern string   `json:"pattern"`
	Domain  []string `json:"domain,omitempty"`
}

type SyncStartRequest struct {
	SourcePath string `json:"source_path,omitempty"`
	Root       string `json:"root,omitempty"`
	Mode       string `json:"mode"`
}

type SyncStartResponse struct {
	ID             string    `json:"id"`
	Root           string    `json:"root"`
	ChunkSizeBytes int64     `json:"chunk_size_bytes"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type SyncManifestRequest struct {
	Entries []SyncManifestEntry `json:"entries"`
	Ignore  []SyncIgnorePattern `json:"ignore,omitempty"`
	Final   bool                `json:"final,omitempty"`
}

type SyncManifestResponse struct {
	Received int `json:"received"`
}

type SyncPlanResponse struct {
	Upload         []string `json:"upload"`
	Delete         []string `json:"delete,omitempty"`
	Unchanged      int      `json:"unchanged"`
	UnchangedBytes int64    `json:"unchanged_bytes"`
}

type SyncCommitRequest struct {
	SourcePath string `json:"source_path"`
	Root       string `json:"root"`
}

type SyncCommitResponse struct {
	Uploaded      int   `json:"uploaded"`
	Deleted       int   `json:"deleted"`
	UploadedBytes int64 `json:"uploaded_bytes"`
}

type SyncResponse struct {
	Root           string   `json:"root"`
	Scanned        int      `json:"scanned"`
	Ignored        int      `json:"ignored"`
	Uploaded       int      `json:"uploaded"`
	Unchanged      int      `json:"unchanged"`
	Deleted        int      `json:"deleted"`
	UploadedBytes  int64    `json:"uploaded_bytes"`
	UnchangedBytes int64    `json:"unchanged_bytes"`
	DurationMS     int64    `json:"duration_ms"`
	DryRun         bool     `json:"dry_run,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

type ReadResponse struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"content_base64"`
	Bytes         int64  `json:"bytes"`
}

type Task struct {
	ID         string        `json:"id"`
	Status     string        `json:"status"`
	Command    string        `json:"command"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt *time.Time    `json:"finished_at,omitempty"`
	Result     *ExecResponse `json:"result,omitempty"`
	Error      string        `json:"error,omitempty"`
}

type TaskRequest struct {
	ExecRequest
}

type SessionRequest struct {
	Command    string            `json:"command"`
	WorkingDir string            `json:"working_dir,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

type SessionInput struct {
	Input string `json:"input"`
}

type Session struct {
	ID              string     `json:"id"`
	Status          string     `json:"status"`
	Command         string     `json:"command"`
	StartedAt       time.Time  `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	Stdout          string     `json:"stdout"`
	Stderr          string     `json:"stderr"`
	ExitCode        *int       `json:"exit_code,omitempty"`
	Error           string     `json:"error,omitempty"`
	OutputTruncated bool       `json:"output_truncated,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type PairRequest struct {
	Device Device `json:"device"`
}

type PairingRequest struct {
	ID           string    `json:"id"`
	Device       Device    `json:"device"`
	Verification string    `json:"verification"`
	Fingerprint  string    `json:"fingerprint,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type PairingStatus struct {
	PairingRequest
	Status      string       `json:"status"`
	Credentials *Credentials `json:"credentials,omitempty"`
}

type Credentials struct {
	BridgeURL   string `json:"bridge_url"`
	Fingerprint string `json:"fingerprint"`
	DeviceID    string `json:"device_id"`
	Token       string `json:"token"`
	Workspace   string `json:"workspace"`
}
