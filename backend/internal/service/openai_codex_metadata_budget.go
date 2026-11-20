package service

import (
	"encoding/json"
	"sort"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexTurnMetadataBudget = 8096

type codexRemoteCandidate struct {
	path, name, url           string
	entryBytes, firstMapBytes int
}

// Only complete remote entries are optional. Reserve every other metadata field
// before fitting them, including extensions retained by legacy projection.
func boundCodexTurnMetadata(full string) (string, error) {
	if len(full) <= codexTurnMetadataBudget {
		return full, nil
	}
	rawWorkspaces := gjson.Get(full, "workspaces")
	if !rawWorkspaces.IsObject() {
		return full, nil
	}
	var workspaces map[string]map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rawWorkspaces.Raw), &workspaces); err != nil {
		return full, nil
	}
	var candidates []codexRemoteCandidate
	selected := make(map[string]map[string]string)
	for path, workspace := range workspaces {
		var remotes map[string]string
		if json.Unmarshal(workspace["associated_remote_urls"], &remotes) != nil || len(remotes) == 0 {
			continue
		}
		delete(workspace, "associated_remote_urls")
		firstMapBytes := len(`"associated_remote_urls":{}`)
		if len(workspace) > 0 {
			firstMapBytes++
		}
		for name, remoteURL := range remotes {
			entry, err := marshalJSONWithoutHTMLEscape([2]string{name, remoteURL})
			if err != nil {
				return "", err
			}
			candidates = append(candidates, codexRemoteCandidate{path, name, remoteURL, len(escapeNonASCIIJSON(entry)) - 2, firstMapBytes})
		}
	}
	if len(candidates) == 0 {
		return full, nil
	}
	baseWorkspaces, err := marshalJSONWithoutHTMLEscape(workspaces)
	if err != nil {
		return "", err
	}
	projectedBytes := len(full) - len(rawWorkspaces.Raw) + len(escapeNonASCIIJSON(baseWorkspaces))
	if projectedBytes > codexTurnMetadataBudget {
		return full, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if (left.name == "origin") != (right.name == "origin") {
			return left.name == "origin"
		}
		if left.entryBytes != right.entryBytes {
			return left.entryBytes < right.entryBytes
		}
		if left.path != right.path {
			return left.path < right.path
		}
		return left.name < right.name
	})
	for _, candidate := range candidates {
		overhead := candidate.firstMapBytes
		if len(selected[candidate.path]) > 0 {
			overhead = 1
		}
		if candidate.entryBytes+overhead > codexTurnMetadataBudget-projectedBytes {
			continue
		}
		projectedBytes += candidate.entryBytes + overhead
		if selected[candidate.path] == nil {
			selected[candidate.path] = make(map[string]string)
		}
		selected[candidate.path][candidate.name] = candidate.url
	}
	for path, remotes := range selected {
		encoded, err := marshalJSONWithoutHTMLEscape(remotes)
		if err != nil {
			return "", err
		}
		workspaces[path]["associated_remote_urls"] = encoded
	}
	encoded, err := marshalJSONWithoutHTMLEscape(workspaces)
	if err != nil {
		return "", err
	}
	return sjson.SetRaw(full, "workspaces", escapeNonASCIIJSON(encoded))
}
