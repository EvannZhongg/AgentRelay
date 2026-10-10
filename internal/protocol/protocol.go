package protocol

import "time"

const APIVersion = "v1"

type Device struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	OS           string            `json:"os"`
	OSVersion    string            `json:"os_version,omitempty"`
	Architecture string            `json:"architecture"`
	WorkerURL    string            `json:"worker_url,omitempty"`
	Capabilities Capabilities      `json:"capabilities"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	LastSeen     time.Time         `json:"last_seen"`
}

type Capabilities struct {
	Exec       bool     `json:"exec"`
	Files      bool     `json:"files"`
	Tasks      bool     `json:"tasks"`
	Shells     []string `json:"shells,omitempty"`
	MaxPayload int64    `json:"max_payload_bytes,omitempty"`
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
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out,omitempty"`
}

type FileEntry struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"content_base64"`
}

type SyncRequest struct {
	Root  string      `json:"root,omitempty"`
	Files []FileEntry `json:"files"`
}

type SyncResponse struct {
	Written []string `json:"written"`
	Bytes   int64    `json:"bytes"`
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

type ErrorResponse struct {
	Error string `json:"error"`
}
