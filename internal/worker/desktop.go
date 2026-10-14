package worker

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"DevFreight/internal/desktop"
	"DevFreight/internal/protocol"
)

func desktopJSON(r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}

func desktopError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, desktop.ErrUnavailable) {
		status = http.StatusNotImplemented
	}
	writeError(w, status, err)
}

func (s *Server) desktopInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	info, err := s.desktop.Info(r.Context())
	if err != nil {
		desktopError(w, err)
		return
	}
	writeJSON(w, 200, info)
}

func (s *Server) desktopCapture(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req protocol.CaptureRequest
	if err := desktopJSON(r, &req); err != nil {
		writeError(w, 400, err)
		return
	}
	out, err := s.desktop.Capture(r.Context(), req)
	if err != nil {
		desktopError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) desktopInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req protocol.DesktopInput
	if err := desktopJSON(r, &req); err != nil {
		writeError(w, 400, err)
		return
	}
	out, err := s.desktop.Input(r.Context(), req)
	if err != nil {
		desktopError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
