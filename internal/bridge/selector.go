package bridge

import (
	"fmt"
	"sort"
	"strings"

	"AgentRelay/internal/protocol"
)

// SortDevices returns a stable order for registry views.
func SortDevices(devices []protocol.Device) []protocol.Device {
	out := append([]protocol.Device(nil), devices...)
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := strings.ToLower(strings.TrimSpace(out[i].Name)), strings.ToLower(strings.TrimSpace(out[j].Name))
		if li != lj {
			return li < lj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ResolveDeviceSelector accepts a persistent short handle, an alias, or an
// internal ID. IDs are retained only for controller-internal routing and are
// never required in agent-facing output.
func ResolveDeviceSelector(devices []protocol.Device, selector string) (string, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", fmt.Errorf("device_id is required (use a device alias or handle)")
	}
	sorted := SortDevices(devices)
	for i := range sorted {
		if sorted[i].ID == selector {
			return sorted[i].ID, nil
		}
	}
	for i := range sorted {
		if strings.EqualFold(strings.TrimSpace(sorted[i].Handle), selector) {
			return sorted[i].ID, nil
		}
	}
	var match *protocol.Device
	for i := range sorted {
		if strings.EqualFold(strings.TrimSpace(sorted[i].Name), selector) {
			if match != nil {
				return "", fmt.Errorf("device alias %q is ambiguous; rename one of the devices in the registry", selector)
			}
			match = &sorted[i]
		}
	}
	if match != nil {
		return match.ID, nil
	}
	return "", fmt.Errorf("unknown device %q; use a device alias or handle", selector)
}
