package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

// BotHomeScope is server-derived authorization scope, not user-selected storage.
// Personal mode leaves OrgID and TeamID empty; platform mode supplies both.
type BotHomeScope struct {
	OrgID  string `json:"org_id,omitempty"`
	TeamID string `json:"team_id,omitempty"`
	UserID string `json:"user_id"`
	BotID  string `json:"bot_id"`
}

func (s BotHomeScope) Validate() error {
	if s.UserID == "" || s.BotID == "" || (s.OrgID == "") != (s.TeamID == "") {
		return errors.New("sandbox: incomplete bot home scope")
	}
	for _, v := range []string{s.OrgID, s.TeamID, s.UserID, s.BotID} {
		if strings.TrimSpace(v) != v || strings.ContainsAny(v, "\x00\r\n") {
			return errors.New("sandbox: invalid bot home scope")
		}
	}
	return nil
}

// Key is a collision-resistant, path-safe identity with unambiguous field boundaries.
func (s BotHomeScope) Key() string {
	raw, _ := json.Marshal(s)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

// BotHomeBinding is an opaque backend-issued reference. It is not a host path,
// mount destination, or authorization grant. Callers must authorize Scope first.
type BotHomeBinding struct {
	Scope   BotHomeScope `json:"scope"`
	Backend BackendKind  `json:"backend"`
	Ref     string       `json:"ref"`
}

func (b *BotHomeBinding) Validate() error {
	if b == nil {
		return nil
	}
	if err := b.Scope.Validate(); err != nil {
		return err
	}
	if b.Backend == "" || b.Ref == "" {
		return errors.New("sandbox: incomplete bot home binding")
	}
	return nil
}
func (b *BotHomeBinding) Clone() *BotHomeBinding {
	if b == nil {
		return nil
	}
	c := *b
	return &c
}
func (b *BotHomeBinding) Equal(other *BotHomeBinding) bool {
	if b == nil || other == nil {
		return b == other
	}
	return *b == *other
}

// BotHomeBackend is optional. Ensure is idempotent and never adopts storage with
// conflicting ownership. DestroySession must not delete the independent home.
// Purging a home is deliberately not part of run/session cleanup.
type BotHomeBackend interface {
	EnsureBotHome(context.Context, BotHomeScope) (*BotHomeBinding, error)
}

// BotHomeArtifactBackend is optional and independent of provisioning. It reads
// only normalized home-relative regular files, never host paths or run storage.
// Callers must authorize the exact binding first; implementations must bound reads.
type BotHomeArtifactBackend interface {
	ReadBotHomeArtifact(context.Context, *BotHomeBinding, string, int64) ([]byte, error)
}

// BotHomeDeleteBackend is an internal storage capability, not authorization.
// Trusted retention must authorize before calling it. Attached homes are retained.
type BotHomeDeleteBackend interface {
	DeleteBotHome(context.Context, *BotHomeBinding) error
}

const MaxBotHomeArtifactBytes int64 = 16 << 20

// ValidateBotHomeArtifactPath rejects aliases as well as traversal, so metadata
// has one canonical, platform-independent relative path.
func ValidateBotHomeArtifactPath(p string) error {
	if p == "" || p == "." || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") || strings.ContainsAny(p, "\\\x00\r\n") {
		return errors.New("sandbox: invalid bot home artifact path")
	}
	return nil
}

var ErrBotHomeUnsupported = errors.New("sandbox: durable bot homes unsupported")

// BotHomePool binds an immutable home before the first prepare starts.
type BotHomePool interface {
	SetBotHome(string, *BotHomeBinding) error
}

func BindBotHome(pool ToolNodePool, sessionID string, binding *BotHomeBinding) error {
	if binding == nil {
		return nil
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	p, ok := pool.(BotHomePool)
	if !ok {
		return ErrBotHomeUnsupported
	}
	return p.SetBotHome(sessionID, binding)
}
