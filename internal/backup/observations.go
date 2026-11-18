package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/moedove/ixp-manager/internal/sflow"
	"github.com/moedove/ixp-manager/internal/telemetry"
	"github.com/moedove/ixp-manager/internal/traffic"
)

func observationFiles(dataDir string) ([]string, error) {
	files := []string{}
	if _, err := os.Lstat(filepath.Join(dataDir, "telemetry")); err == nil {
		names, err := telemetry.ListFiles(filepath.Join(dataDir, "telemetry"))
		if err != nil {
			return nil, fmt.Errorf("traffic backup preflight failed: %w", err)
		}
		for _, name := range names {
			files = append(files, "telemetry/"+name)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	flowDir := filepath.Join(dataDir, "sflow")
	if err := sflow.ValidateFiles(flowDir); err != nil && !errors.Is(err, sflow.ErrTornWALTail) {
		return nil, fmt.Errorf("sampled traffic backup preflight failed: %w", err)
	}
	names, err := sflow.ListFiles(flowDir)
	if err != nil && !errors.Is(err, sflow.ErrTornWALTail) {
		return nil, err
	}
	for _, name := range names {
		files = append(files, "sflow/"+name)
	}
	legacy, err := traffic.LegacyListFiles(filepath.Join(dataDir, "legacy-traffic"))
	if err != nil {
		return nil, fmt.Errorf("legacy traffic preflight failed: %w", err)
	}
	for _, name := range legacy {
		files = append(files, "legacy-traffic/"+name)
	}
	return files, nil
}
