package api

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/SAP/astonish/pkg/channels"
)

// channelManager holds a reference to the active ChannelManager.
// Set by the daemon during startup via SetChannelManager.
var (
	channelManagerMu sync.RWMutex
	channelManager   *channels.ChannelManager
)

// ChannelConfigStatus records the status of a channel from configuration:
// enabled but possibly not started (e.g., missing bot token).
type ChannelConfigStatus struct {
	Enabled bool   // Channel is enabled in config
	Error   string // Why it didn't start (empty = it started fine or isn't enabled)
}

var (
	channelConfigMu       sync.RWMutex
	channelConfigStatuses map[string]ChannelConfigStatus
)

// SetChannelConfigStatuses records which channels are enabled in config and
// any errors that prevented them from starting. Called by the daemon after
// initChannels runs.
func SetChannelConfigStatuses(statuses map[string]ChannelConfigStatus) {
	channelConfigMu.Lock()
	defer channelConfigMu.Unlock()
	channelConfigStatuses = statuses
}

// getChannelConfigStatuses returns the registered config statuses.
func getChannelConfigStatuses() map[string]ChannelConfigStatus {
	channelConfigMu.RLock()
	defer channelConfigMu.RUnlock()
	return channelConfigStatuses
}

// channelReloadFn holds a callback that reloads channel configuration.
// Set by the daemon run loop via SetChannelReloadFunc.
var (
	channelReloadMu sync.RWMutex
	channelReloadFn func() error
)

// SetChannelReloadFunc registers a callback that the daemon provides to
// reload channels from the latest config. Called once during daemon startup.
func SetChannelReloadFunc(fn func() error) {
	channelReloadMu.Lock()
	defer channelReloadMu.Unlock()
	channelReloadFn = fn
}

// getChannelReloadFunc returns the registered reload callback, or nil.
func getChannelReloadFunc() func() error {
	channelReloadMu.RLock()
	defer channelReloadMu.RUnlock()
	return channelReloadFn
}

// SetChannelManager registers the active channel manager for API access.
// Called by the daemon run loop after channel initialization.
func SetChannelManager(cm *channels.ChannelManager) {
	channelManagerMu.Lock()
	defer channelManagerMu.Unlock()
	channelManager = cm
}

// GetChannelManager returns the active channel manager, or nil if not set.
func GetChannelManager() *channels.ChannelManager {
	channelManagerMu.RLock()
	defer channelManagerMu.RUnlock()
	return channelManager
}

// ChannelsStatusHandler returns the status of all registered channels.
// Also reports channels that are enabled in config but failed to start.
//
// GET /api/channels/status
//
// Response:
//
//	{
//	  "channels": {
//	    "telegram": { "connected": true, "account_id": "@bot", ... }
//	  }
//	}
func ChannelsStatusHandler(w http.ResponseWriter, r *http.Request) {
	cm := GetChannelManager()

	type channelStatusResponse struct {
		Connected    bool   `json:"connected"`
		Enabled      bool   `json:"enabled"`
		AccountID    string `json:"account_id,omitempty"`
		ConnectedAt  string `json:"connected_at,omitempty"`
		Error        string `json:"error,omitempty"`
		MessageCount int64  `json:"message_count"`
	}

	channelMap := make(map[string]channelStatusResponse)

	// First: add live channel statuses from the ChannelManager
	if cm != nil {
		statuses := cm.Status()
		for id, status := range statuses {
			csr := channelStatusResponse{
				Connected:    status.Connected,
				Enabled:      true,
				AccountID:    status.AccountID,
				Error:        status.Error,
				MessageCount: status.MessageCount,
			}
			if !status.ConnectedAt.IsZero() {
				csr.ConnectedAt = status.ConnectedAt.Format("2006-01-02T15:04:05Z")
			}
			channelMap[id] = csr
		}
	}

	// Second: add channels that are enabled in config but not running
	if cfgStatuses := getChannelConfigStatuses(); cfgStatuses != nil {
		for id, cs := range cfgStatuses {
			if !cs.Enabled {
				continue
			}
			// Only add if not already in the map (i.e., not running)
			if _, exists := channelMap[id]; !exists {
				channelMap[id] = channelStatusResponse{
					Connected: false,
					Enabled:   true,
					Error:     cs.Error,
				}
			}
		}
	}

	response := map[string]any{
		"channels": channelMap,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// ChannelsReloadHandler re-reads channel configuration and reinitializes
// channel adapters without restarting the daemon process.
//
// POST /api/channels/reload
//
// Response:
//
//	{ "status": "ok", "message": "Channels reloaded" }
func SlackEventsHandler(w http.ResponseWriter, r *http.Request) {
	cm := GetChannelManager()
	if cm == nil {
		http.Error(w, "slack channel not available", http.StatusServiceUnavailable)
		return
	}
	events, _, ok := cm.GetSlackHTTPHandlers()
	if !ok || events == nil {
		http.Error(w, "slack channel not available", http.StatusServiceUnavailable)
		return
	}
	events.ServeHTTP(w, r)
}

func SlackSlashCommandHandler(w http.ResponseWriter, r *http.Request) {
	cm := GetChannelManager()
	if cm == nil {
		http.Error(w, "slack channel not available", http.StatusServiceUnavailable)
		return
	}
	_, commands, ok := cm.GetSlackHTTPHandlers()
	if !ok || commands == nil {
		http.Error(w, "slack channel not available", http.StatusServiceUnavailable)
		return
	}
	commands.ServeHTTP(w, r)
}

func ChannelsReloadHandler(w http.ResponseWriter, r *http.Request) {
	// Only org admins can reload channel configuration.
	if isPlatformMode(r) {
		if RequireOrgAdmin(w, r) == nil {
			return
		}
	}

	reload := getChannelReloadFunc()
	if reload == nil {
		respondError(w, http.StatusServiceUnavailable, `{"error":"reload not available"}`)
		return
	}

	if err := reload(); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"message": "Channels reloaded",
	})
}
