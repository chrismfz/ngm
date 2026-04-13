package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mynginx/internal/config"
	"mynginx/internal/util"
)

const provisionReadyMarkerPath = "/var/lib/ngm/provision.ready"

var ErrProvisionInitNotCompleted = errors.New("Provision init not completed for active nginx config. Run: ngm provision init")

type provisionReadyMarker struct {
	Timestamp     string `json:"timestamp"`
	NginxMainConf string `json:"nginx_main_conf"`
	ConfigHash    string `json:"config_hash"`
}

func WriteProvisionReadyMarker(paths config.Paths) error {
	hash, err := nginxConfigHash(paths.NginxMainConf)
	if err != nil {
		return fmt.Errorf("compute nginx config hash: %w", err)
	}

	m := provisionReadyMarker{
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		NginxMainConf: paths.NginxMainConf,
		ConfigHash:    hash,
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal provision marker: %w", err)
	}
	b = append(b, '\n')

	if err := util.WriteFileAtomic(provisionReadyMarkerPath, b, 0644); err != nil {
		return fmt.Errorf("write provision marker %s: %w", provisionReadyMarkerPath, err)
	}
	return nil
}

func EnsureProvisionReady(paths config.Paths) error {
	expectedHash, err := nginxConfigHash(paths.NginxMainConf)
	if err != nil {
		return ErrProvisionInitNotCompleted
	}

	b, err := os.ReadFile(provisionReadyMarkerPath)
	if err != nil {
		return ErrProvisionInitNotCompleted
	}

	var m provisionReadyMarker
	if err := json.Unmarshal(b, &m); err != nil {
		return ErrProvisionInitNotCompleted
	}

	if filepath.Clean(m.NginxMainConf) != filepath.Clean(paths.NginxMainConf) {
		return ErrProvisionInitNotCompleted
	}
	if m.ConfigHash != expectedHash {
		return ErrProvisionInitNotCompleted
	}
	return nil
}

func nginxConfigHash(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return util.Sha256Hex(b), nil
}
