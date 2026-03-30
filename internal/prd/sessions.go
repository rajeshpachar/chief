package prd

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// sessionsPath returns the sessions.json path for a given prd.md path.
func sessionsPath(prdPath string) string {
	return filepath.Join(filepath.Dir(prdPath), "sessions.json")
}

// LoadSessions reads the sessions.json file and returns a map of storyID -> sessionID.
func LoadSessions(prdPath string) (map[string]string, error) {
	data, err := os.ReadFile(sessionsPath(prdPath))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	var sessions map[string]string
	if err := json.Unmarshal(data, &sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

// SaveSession stores a Claude session ID for a story so it can be resumed later.
func SaveSession(prdPath, storyID, sessionID string) error {
	sessions, err := LoadSessions(prdPath)
	if err != nil {
		sessions = map[string]string{}
	}
	sessions[storyID] = sessionID
	data, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(sessionsPath(prdPath), data, 0o644)
}

// GetSession returns the Claude session ID for a story, or "" if not found.
func GetSession(prdPath, storyID string) (string, error) {
	sessions, err := LoadSessions(prdPath)
	if err != nil {
		return "", err
	}
	return sessions[storyID], nil
}
