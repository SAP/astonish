package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/SAP/astonish/pkg/store"
)

// MigrateBaseTemplateLayersOnStartup checks if the @base template's layer directory
// exists on disk. If the database has a configured layer but the layer data is missing
// (e.g., from an Incus-to-Docker migration), it clears the layer reference in the DB
// and logs the condition. Sessions will continue to work using the @none fallback,
// and the UI will prompt the admin to rebuild the layer.
//
// It also updates the distro field in the base_config if it's still set to the old
// Incus default (ubuntu-noble) to match the Docker sandbox-base image (debian-bookworm).
func MigrateBaseTemplateLayersOnStartup(ctx context.Context, tplStore store.SandboxTemplateStore, layersDir string) error {
	// Fetch the @base template config from the database
	baseConfig, err := tplStore.GetBaseConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to get base template config: %w", err)
	}

	// If no configuration exists, nothing to migrate
	if baseConfig == nil {
		return nil
	}

	// Update distro if it's still the old Incus ubuntu-noble default
	if baseConfig.ConfigJSON != nil {
		var configMap map[string]interface{}
		if err := json.Unmarshal(baseConfig.ConfigJSON, &configMap); err == nil {
			// Check if distro is ubuntu-noble (old Incus value)
			if distro, ok := configMap["distro"].(string); ok && distro == "ubuntu-noble" {
				slog.Info("updating base template distro from ubuntu-noble to debian-bookworm for Docker backend",
					"component", "layer-migration")
				// Update to debian-bookworm (matching the Docker sandbox-base image)
				configMap["distro"] = "debian-bookworm"
				if updatedJSON, err := json.Marshal(configMap); err == nil {
					if err := tplStore.SetBaseConfig(ctx, baseConfig.LayerID, updatedJSON, baseConfig.ConfiguredBy); err != nil {
						slog.Warn("failed to update base template distro",
							"error", err)
						// Don't fail the migration, just log the warning
					} else {
						slog.Info("base template distro updated successfully")
					}
				}
			}
		}
	}

	// If no layer ID is set, nothing to migrate
	if baseConfig.LayerID == "" {
		return nil
	}

	// Check if the layer directory exists on disk
	layerRootfs := filepath.Join(layersDir, baseConfig.LayerID, "rootfs")
	_, err = os.Stat(layerRootfs)
	if err == nil {
		// Layer exists on disk, no migration needed
		return nil
	}
	if !os.IsNotExist(err) {
		// Unexpected error (e.g., permission denied) — log but don't fail startup
		slog.Warn("error checking base template layer on disk",
			"layer_id", baseConfig.LayerID,
			"path", layerRootfs,
			"error", err)
		return nil
	}

	// Layer directory does not exist on disk but the DB references it —
	// this indicates the layer was built under a different backend (e.g., Incus)
	// and the data is no longer accessible. Clear the layer reference so:
	// 1. Sessions fall back to @none (running the base image directly)
	// 2. The UI shows "Not configured" and prompts the admin to rebuild
	slog.Info("base template layer not found on disk; clearing database reference",
		"layer_id", baseConfig.LayerID,
		"path", layerRootfs)

	// Set the layer ID to empty, preserving the configuration
	if err := tplStore.SetBaseConfig(ctx, "", baseConfig.ConfigJSON, baseConfig.ConfiguredBy); err != nil {
		return fmt.Errorf("failed to clear base template layer reference: %w", err)
	}

	slog.Info("base template migration complete; admin should rebuild layer from Platform Admin → Sandbox Templates")

	return nil
}
