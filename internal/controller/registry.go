package controller

import (
	"AgentRelay/internal/protocol"
	"errors"
	"fmt"
	"strings"
)

// Validate state before writing anything. Identity/configuration are never
// synthesized to repair existing data, and orphaned selections are rejected.
func (s *Server) validateRegistry() error {
	handles := map[string]bool{}
	for id, rec := range s.store.Devices {
		d := rec.Device
		if id != d.ID || d.Handle == "" || handles[strings.ToLower(d.Handle)] || !validAlias(d.Name) || !s.aliasAvailableLocked(d.Name, id) {
			return errors.New("invalid device identity, handle or alias")
		}
		handles[strings.ToLower(d.Handle)] = true
		if d.ActiveConnection != "" {
			c, ok := s.store.Connections[d.ActiveConnection]
			if !ok || c.Connection.DeviceID != id {
				return errors.New("invalid device connection selection")
			}
		}
	}
	for id, rec := range s.store.Connections {
		c := rec.Connection
		if _, ok := s.store.Devices[c.DeviceID]; !ok || c.ID != id || c.Revision == "" {
			return errors.New("invalid connection identity or revision")
		}
		if c.Kind == "worker" && rec.TokenHash == "" {
			return errors.New("Worker credential hash is missing")
		}
		if err := s.validateSpec(protocol.ConnectionSpec{Kind: c.Kind, SSH: c.SSH, Options: c.Options, Workspace: c.Workspace}); err != nil {
			return fmt.Errorf("invalid saved connection: %w", err)
		}
	}
	if len(s.store.Devices) != 0 && s.store.Identity.Certificate == "" {
		return errors.New("Controller TLS identity is missing")
	}
	return nil
}
