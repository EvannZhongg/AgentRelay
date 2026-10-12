package controller

import (
	"AgentRelay/internal/protocol"
	"net/http"
	"sort"
	"strings"
)

func (s *Server) devicesRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		s.createDevice(w, r)
		return
	}
	if r.Method != "GET" {
		methodNotAllowed(w)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]protocol.Device, 0, len(s.store.Devices))
	for _, rec := range s.store.Devices {
		out = append(out, s.deviceViewLocked(rec.Device))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, 200, out)
}

type DeviceUpdate struct {
	ActiveConnection *string `json:"active_connection,omitempty"`
	Name             *string `json:"name,omitempty"`
	Authorized       *bool   `json:"authorized,omitempty"`
	DesktopAccess    *string `json:"desktop_access,omitempty"`
}

func validAlias(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\\r\n\t") {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
