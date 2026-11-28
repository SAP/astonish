package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/SAP/astonish/pkg/store"
)

// BotHomeFileErrorCode is the bounded status emitted by the confined file helper.
type BotHomeFileErrorCode string

const (
	BotHomeFileNotFound  BotHomeFileErrorCode = "not_found"
	BotHomeFileForbidden BotHomeFileErrorCode = "forbidden"
	BotHomeFileFailed    BotHomeFileErrorCode = "failed"
)

// BotHomeFileError never contains helper paths or arbitrary exception text.
// The process owner separately joins terminal/cleanup errors for diagnostics.
type BotHomeFileError struct {
	Code BotHomeFileErrorCode `json:"code"`
}

func (e *BotHomeFileError) Error() string { return "bot home file helper: " + string(e.Code) }
func (e *BotHomeFileError) Is(target error) bool {
	return e.Code == BotHomeFileNotFound && target == store.ErrBotNotFound || e.Code == BotHomeFileForbidden && target == store.ErrBotForbidden
}

// DecodeBotHomeFileLine preserves success metadata and rejects malformed or
// unknown error envelopes rather than decoding them as a zero-valued entry.
func DecodeBotHomeFileLine(line []byte, out any) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(line, &envelope); err != nil {
		return err
	}
	if envelope == nil {
		return errors.New("invalid file metadata object")
	}
	if raw, ok := envelope["error"]; ok {
		var e BotHomeFileError
		if len(envelope) != 1 {
			return errors.New("mixed file error envelope")
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		switch e.Code {
		case BotHomeFileNotFound, BotHomeFileForbidden, BotHomeFileFailed:
			return &e
		default:
			return fmt.Errorf("invalid file helper error code")
		}
	}
	return json.Unmarshal(line, out)
}

// BotHomeFileTerminalError identifies a completed helper response. Consumers
// close stdin and join normally for these errors instead of cancelling exec
// before it has flushed stderr and reported its exit status.
func BotHomeFileTerminalError(err error) bool {
	var e *BotHomeFileError
	return errors.As(err, &e)
}
