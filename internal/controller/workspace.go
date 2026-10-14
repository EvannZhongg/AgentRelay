package controller

import (
	"DevFreight/internal/protocol"
	"DevFreight/internal/workspace"
	"encoding/json"
	"errors"
)

func resolveWorkingDir(policy protocol.WorkspacePolicy, data []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("request body must be a JSON object")
	}
	var dir, kind string
	if value, ok := fields["working_dir"]; ok {
		if err := json.Unmarshal(value, &dir); err != nil {
			return nil, err
		}
	}
	if value, ok := fields["working_dir_kind"]; ok {
		if err := json.Unmarshal(value, &kind); err != nil {
			return nil, err
		}
	}
	resolved, err := workspace.Resolve(policy, dir, kind)
	if err != nil {
		return nil, err
	}
	fields["working_dir"], _ = json.Marshal(resolved)
	delete(fields, "working_dir_kind")
	return json.Marshal(fields)
}
