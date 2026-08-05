package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// SlashCommandHTTPHandler returns an http.Handler that receives Slack slash commands.
// It verifies the request signature and returns an ephemeral response for the invoking user.
//
// This handler should be mounted at POST /slack/commands on the daemon's HTTP server.
func (s *SlackChannel) SlashCommandHTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if !s.verifyHTTPRequest(w, r) {
			return
		}

		cmd, err := slack.SlashCommandParse(r)
		if err != nil {
			s.logger.Printf("[slack] Failed to parse slash command: %v", err)
			http.Error(w, "failed to parse command", http.StatusBadRequest)
			return
		}
		threadTS := slashCommandThreadTSFromForm(r)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"response_type": "ephemeral",
			"text":          s.handleSlashCommandWithThread(r.Context(), cmd, threadTS),
		})
	})
}

func slashCommandThreadTSFromForm(r *http.Request) string {
	if r == nil {
		return ""
	}
	return slackThreadTimestamp(
		r.PostForm.Get("thread_ts"),
		firstNonEmpty(r.PostForm.Get("message_ts"), r.PostForm.Get("ts")),
	)
}

func (s *SlackChannel) verifyHTTPRequest(w http.ResponseWriter, r *http.Request) bool {
	body, ok := s.readAndVerifyHTTPRequest(w, r)
	if !ok {
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return true
}

func (s *SlackChannel) readAndVerifyHTTPRequest(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return nil, false
	}

	if s.config.SigningSecret == "" {
		http.Error(w, "verification failed", http.StatusUnauthorized)
		return nil, false
	}

	sv, svErr := slack.NewSecretsVerifier(r.Header, s.config.SigningSecret)
	if svErr != nil {
		http.Error(w, "verification failed", http.StatusUnauthorized)
		return nil, false
	}
	if _, svErr = sv.Write(body); svErr != nil {
		http.Error(w, "verification failed", http.StatusUnauthorized)
		return nil, false
	}
	if svErr = sv.Ensure(); svErr != nil {
		s.logger.Printf("[slack] Request signature verification failed: %v", svErr)
		http.Error(w, "verification failed", http.StatusUnauthorized)
		return nil, false
	}

	return body, true
}

// EventsHTTPHandler returns an http.Handler that receives Slack Events API
// webhooks. It verifies the request signature, handles URL verification
// challenges, and dispatches events to the adapter's event processing logic.
//
// This handler should be mounted at POST /slack/events on the daemon's HTTP server.
func (s *SlackChannel) EventsHTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, ok := s.readAndVerifyHTTPRequest(w, r)
		if !ok {
			return
		}

		// Parse the outer event
		eventsAPIEvent, err := slackevents.ParseEvent(json.RawMessage(body), slackevents.OptionNoVerifyToken())
		if err != nil {
			s.logger.Printf("[slack] Failed to parse event: %v", err)
			http.Error(w, "failed to parse event", http.StatusBadRequest)
			return
		}

		// Handle URL verification challenge (Slack sends this during app setup)
		if eventsAPIEvent.Type == slackevents.URLVerification {
			var challenge slackevents.ChallengeResponse
			if err := json.Unmarshal(body, &challenge); err != nil {
				http.Error(w, "challenge parse error", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(challenge.Challenge))
			return
		}

		s.logger.Printf("[slack] HTTP Events API request received: outer_type=%s inner_type=%s team=%s", eventsAPIEvent.Type, eventsAPIEvent.InnerEvent.Type, eventsAPIEvent.TeamID)

		// Respond immediately (Slack requires 200 within 3 seconds)
		w.WriteHeader(http.StatusOK)

		// Process the event asynchronously
		go s.handleEventsAPIEvent(context.Background(), eventsAPIEvent, eventsAPIEvent.TeamID)
	})
}
