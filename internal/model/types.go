package model

import "time"

// VPNState describes whether torrent actions are currently allowed.
type VPNState string

const (
	VPNStateSafe    VPNState = "SAFE"
	VPNStateUnsafe  VPNState = "UNSAFE"
	VPNStateUnknown VPNState = "UNKNOWN"
)

type Torrent struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Progress      float64 `json:"progress"`
	DownloadSpeed int64   `json:"download_speed"`
	ETASeconds    int64   `json:"eta_seconds"`
	State         string  `json:"state"`
	SavePath      string  `json:"save_path"`
	Completed     bool    `json:"completed"`
}

type JobStatus string

const (
	JobStatusQueued  JobStatus = "queued"
	JobStatusRunning JobStatus = "running"
	JobStatusPaused  JobStatus = "paused"
	JobStatusDone    JobStatus = "done"
	JobStatusFailed  JobStatus = "failed"
)

type ConversionJob struct {
	ID         int64     `json:"id"`
	TorrentID  string    `json:"torrent_id"`
	InputPath  string    `json:"input_path"`
	OutputPath string    `json:"output_path"`
	Preset     string    `json:"preset"`
	Status     JobStatus `json:"status"`
	Attempts   int       `json:"attempts"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type UploadJob struct {
	ID        int64     `json:"id"`
	FilePath  string    `json:"file_path"`
	ObjectKey string    `json:"object_key"`
	Status    JobStatus `json:"status"`
	Attempts  int       `json:"attempts"`
	FinalURL  string    `json:"final_url,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type LinkRecord struct {
	ID        int64     `json:"id"`
	FilePath  string    `json:"file_path"`
	ObjectKey string    `json:"object_key"`
	FinalURL  string    `json:"final_url"`
	CreatedAt time.Time `json:"created_at"`
}

type Event struct {
	ID        int64     `json:"id"`
	Level     string    `json:"level"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}
