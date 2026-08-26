package store

import "context"

// Skill represents an operational knowledge skill.
type Skill struct {
	Name             string      `json:"name" yaml:"name"`
	Description      string      `json:"description" yaml:"description"`
	OS               []string    `json:"os,omitempty" yaml:"os,omitempty"`
	RequireBins      []string    `json:"require_bins,omitempty" yaml:"require_bins,omitempty"`
	RequireEnv       []string    `json:"require_env,omitempty" yaml:"require_env,omitempty"`
	Metadata         interface{} `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	Content          string      `json:"-" yaml:"-"`
	FilePath         string      `json:"-" yaml:"-"`
	Source           string      `json:"-" yaml:"-"`
	Directory        string      `json:"-" yaml:"-"`
	CreatedBy        string      `json:"-" yaml:"-"` // User ID (UUID) — required for PG inserts
	ValidationStatus string      `json:"validation_status,omitempty" yaml:"-"`
	ValidationMeta   string      `json:"-" yaml:"-"` // Raw JSON stored in DB
}

// SkillFile represents one auxiliary file belonging to a skill
// (e.g. scripts/deploy.sh, references/api.md, templates/report.md).
// These are stored separately from the main SKILL.md content.
type SkillFile struct {
	ID           string `json:"id"`
	SkillID      string `json:"skill_id"`
	Path         string `json:"path"`     // e.g. "scripts" or ""
	Filename     string `json:"filename"` // e.g. "deploy.sh"
	Content      string `json:"content"`
	IsExecutable bool   `json:"is_executable"`
	SizeBytes    int64  `json:"size_bytes"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// SkillStore manages skill definitions.
//
// In platform mode, skills can be org-level or team-level (loaded via SkillStore).
type SkillStore interface {
	// LoadAll loads all available skills from all sources.
	LoadAll(ctx context.Context) ([]Skill, error)

	// Get retrieves a skill by name.
	Get(ctx context.Context, name string) (*Skill, error)

	// Save persists a custom skill.
	Save(ctx context.Context, skill *Skill) error

	// Delete removes a custom skill.
	Delete(ctx context.Context, name string) error

	// List returns all skill names and their sources.
	List(ctx context.Context) ([]Skill, error)

	// UpdateValidationStatus updates the validation_status and validation_meta for a skill.
	UpdateValidationStatus(ctx context.Context, name, status, meta string) error

	// --- Multi-file support (Phase 1 of ClawHub multi-file skills) ---

	// ListFiles returns all auxiliary files belonging to a skill.
	ListFiles(ctx context.Context, skillName string) ([]SkillFile, error)

	// GetFile retrieves one specific auxiliary file for a skill.
	GetFile(ctx context.Context, skillName, path, filename string) (*SkillFile, error)

	// SaveFile creates or updates an auxiliary file for a skill.
	SaveFile(ctx context.Context, skillName string, file *SkillFile) error

	// DeleteFile removes one auxiliary file from a skill.
	DeleteFile(ctx context.Context, skillName, path, filename string) error
}
