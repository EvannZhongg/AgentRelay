package protocol

import "time"

type DesktopCapability struct {
	Supported bool     `json:"supported"`
	Backend   string   `json:"backend"`
	Actions   []string `json:"actions,omitempty"`
}

// Bounds use global desktop coordinates: physical pixels on Windows, points on macOS.
type Bounds struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Display struct {
	ID          int     `json:"id"`
	Primary     bool    `json:"primary"`
	Bounds      Bounds  `json:"bounds"`
	PixelWidth  int     `json:"pixel_width"`
	PixelHeight int     `json:"pixel_height"`
	ScaleX      float64 `json:"scale_x"`
	ScaleY      float64 `json:"scale_y"`
}

type DesktopInfo struct {
	DesktopCapability
	CoordinateUnit string    `json:"coordinate_unit"`
	CaptureAllowed bool      `json:"capture_allowed"`
	InputAllowed   bool      `json:"input_allowed"`
	Displays       []Display `json:"displays"`
}

type CaptureRequest struct {
	DisplayID int `json:"display_id"`
	MaxWidth  int `json:"max_width,omitempty"`
	MaxHeight int `json:"max_height,omitempty"`
}

type CaptureResponse struct {
	Display        Display          `json:"display"`
	Displays       []Display        `json:"displays"`
	CoordinateUnit string           `json:"coordinate_unit"`
	CapturedAt     time.Time        `json:"captured_at"`
	MimeType       string           `json:"mime_type"`
	Width          int              `json:"width"`
	Height         int              `json:"height"`
	ImageBase64    string           `json:"image_base64,omitempty"`
	Artifact       *CaptureArtifact `json:"artifact,omitempty"`
}

type CaptureArtifact struct {
	Path     string `json:"path"`
	MimeType string `json:"mime_type"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Bytes    int    `json:"bytes"`
	SHA256   string `json:"sha256"`
}

type DesktopInput struct {
	Action string `json:"action"`
	X      *int   `json:"x,omitempty"`
	Y      *int   `json:"y,omitempty"`
	ToX    *int   `json:"to_x,omitempty"`
	ToY    *int   `json:"to_y,omitempty"`
	// With display_id, coordinates are relative to that display. Reference sizes
	// let a caller use preview-image pixels instead of native desktop coordinates.
	DisplayID       *int     `json:"display_id,omitempty"`
	ReferenceWidth  int      `json:"reference_width,omitempty"`
	ReferenceHeight int      `json:"reference_height,omitempty"`
	Button          string   `json:"button,omitempty"`
	Clicks          int      `json:"clicks,omitempty"`
	DeltaX          int      `json:"delta_x,omitempty"`
	DeltaY          int      `json:"delta_y,omitempty"`
	Key             string   `json:"key,omitempty"`
	Modifiers       []string `json:"modifiers,omitempty"`
	Text            string   `json:"text,omitempty"`
}

type DesktopInputResult struct {
	Action      string    `json:"action"`
	CompletedAt time.Time `json:"completed_at"`
}
