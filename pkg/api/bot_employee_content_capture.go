package api

import (
	"context"
	"fmt"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

// Index only persisted current-run private tool responses. The runtime envelope
// validator requires the worker's start, dispatch and matching invocation before
// releasing response bytes; employee private_text is never an evidence channel.
func botEmployeeCaptureLegacyPrivateContent(ctx context.Context, req bot.RunRequest, resources *BotChatResources, w *store.BotWorkState) error {
	if err := botEmployeeBindPrivateEvidence(ctx, req, resources, w); err != nil {
		return err
	}
	w.Candidate.Digest = w.Candidate.ContentDigest()
	return nil
}

func botEmployeeBindPrivateEvidence(ctx context.Context, req bot.RunRequest, resources *BotChatResources, w *store.BotWorkState) error {
	if resources == nil || resources.EvidenceReader == nil {
		return nil
	}
	if w == nil || w.Candidate == nil || w.Contract.BotID != req.Bot.ID || w.Contract.RunID != req.Run.ID {
		return store.ErrBotForbidden
	}
	if w.Candidate.Bodies == nil {
		w.Candidate.Bodies = map[string]string{}
	}
	dispatches := make(map[string]*botPrivateEvidenceDispatch)
	var cursor int64
	for page := 0; page < 100; page++ {
		rows, err := resources.EvidenceReader.ReadBotTranscript(ctx, req.Run.SessionID, cursor, 100)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Event == nil {
				continue
			}
			if _, private := row.Event.Actions.StateDelta[botEmployeeWorkerTraceKey]; !private {
				continue
			}
			content := botPrivateEvidenceContent(row, "", dispatches)
			if content == nil {
				continue
			}
			for _, part := range content.Parts {
				response := part.FunctionResponse
				raw, err := botCapturedResponseJSON(response.Response)
				if err != nil {
					return err
				}
				reference := store.BotWorkEvidenceReference{Ref: "capture:" + response.ID, RunID: req.Run.ID, EventID: row.Event.ID, CallID: response.ID, Digest: botAgentDigest(raw)}
				if w.AgentWork != nil {
					if err := bindBotCapturedEvidence(w, reference); err != nil {
						return err
					}
				}
				if len(raw) > store.BotWorkMaxCandidateBytes {
					return store.ErrBotInvalid
				}
				w.Candidate.Bodies["tool-response:"+response.ID] = string(raw)
			}
		}
		if len(rows) < 100 {
			return nil
		}
		next := rows[len(rows)-1].Cursor
		if next <= cursor {
			return store.ErrBotInvalid
		}
		cursor = next
	}
	return fmt.Errorf("%w: private review evidence scan limit reached", store.ErrBotUnsupported)
}
