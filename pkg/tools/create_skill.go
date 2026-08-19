package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/SAP/astonish/pkg/safepath"
	"github.com/SAP/astonish/pkg/skills"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// CreateSkillArgs defines the arguments for create_skill.
type CreateSkillArgs struct {
	Name    string `json:"name" jsonschema:"New skill name using only ASCII letters, digits, hyphens, and underscores (e.g. Deploy_K8s)"`
	Content string `json:"content,omitempty" jsonschema:"Optional full SKILL.md content to write (including YAML frontmatter). When omitted a scaffold template is generated automatically."`
}

// CreateSkillResult describes the newly-created filesystem skill.
type CreateSkillResult struct {
	Name      string `json:"name"`
	Directory string `json:"directory,omitempty"`
	File      string `json:"file,omitempty"`
	Content   string `json:"content,omitempty"`
	Error     string `json:"error,omitempty"`
}

// createSkillMu serializes the portable read-directory/case-insensitive-name
// check with leaf creation. os.Mkdir remains the atomic guard for exact-name
// races, including creators outside this process.
var createSkillMu sync.Mutex

// CreateSkill creates a skill under the injected root. The root is captured by
// the constructor rather than accepted from tool arguments.
func CreateSkill(root string) func(tool.Context, CreateSkillArgs) (CreateSkillResult, error) {
	return func(_ tool.Context, args CreateSkillArgs) (result CreateSkillResult, err error) {
		name := strings.TrimSpace(args.Name)
		result.Name = name
		if !validSkillName(name) {
			result.Error = "invalid skill name: use ASCII letters, digits, hyphens, or underscores"
			return result, nil
		}
		if root == "" {
			result.Error = "skill root is not configured"
			return result, nil
		}
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			result.Error = "skill root is invalid"
			return result, nil
		}
		if err := os.MkdirAll(rootAbs, 0755); err != nil {
			result.Error = fmt.Sprintf("failed to prepare skill root: %v", err)
			return result, nil
		}

		skillDir := filepath.Join(rootAbs, name)
		if err := safepath.ContainedWithin(skillDir, rootAbs); err != nil {
			result.Error = "invalid skill path"
			return result, nil
		}

		createSkillMu.Lock()
		defer createSkillMu.Unlock()

		entries, err := os.ReadDir(rootAbs)
		if err != nil {
			result.Error = fmt.Sprintf("failed to inspect skill root: %v", err)
			return result, nil
		}
		for _, entry := range entries {
			if strings.EqualFold(entry.Name(), name) {
				result.Error = fmt.Sprintf("skill %q already exists", name)
				return result, nil
			}
		}
		if err := os.Mkdir(skillDir, 0755); err != nil {
			if os.IsExist(err) {
				result.Error = fmt.Sprintf("skill %q already exists", name)
			} else {
				result.Error = fmt.Sprintf("failed to create skill %q: %v", name, err)
			}
			return result, nil
		}
		cleanup := true
		defer func() {
			if cleanup {
				_ = os.RemoveAll(skillDir)
			}
		}()

		template := skills.NewSkillTemplate(name)
		// If the caller supplied explicit content, use it; otherwise fall back
		// to the scaffold template so the file is never empty.
		// If the supplied content lacks YAML frontmatter (--- delimiters), prepend
		// a minimal frontmatter block so the skill is always loadable by ParseSkillFile.
		fileContent := template
		if strings.TrimSpace(args.Content) != "" {
			fileContent = ensureSkillFrontmatter(args.Content, name)
		}
		skillFile := filepath.Join(skillDir, "SKILL.md")
		f, err := os.OpenFile(skillFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			result.Error = fmt.Sprintf("failed to create skill file: %v", err)
			return result, nil
		}
		if _, err = f.WriteString(fileContent); err != nil {
			_ = f.Close()
			result.Error = fmt.Sprintf("failed to write skill file: %v", err)
			return result, nil
		}
		if err = f.Close(); err != nil {
			result.Error = fmt.Sprintf("failed to close skill file: %v", err)
			return result, nil
		}
		cleanup = false
		result.Directory = skillDir
		result.File = skillFile
		result.Content = fileContent
		return result, nil
	}
}

// ensureSkillFrontmatter returns content with a valid YAML frontmatter block.
// If content already starts with a --- delimited block that contains both
// "name:" and "description:" fields, it is returned unchanged. Otherwise a
// minimal frontmatter block is prepended so ParseSkillFile can load the skill.
func ensureSkillFrontmatter(content, name string) string {
	trimmed := strings.TrimSpace(content)
	// Detect whether a frontmatter block is already present and valid:
	// it must start with "---", have a closing "---", and contain name/description.
	if strings.HasPrefix(trimmed, "---") {
		// Find closing ---
		rest := trimmed[3:]
		end := strings.Index(rest, "\n---")
		if end >= 0 {
			front := rest[:end]
			if strings.Contains(front, "name:") && strings.Contains(front, "description:") {
				return content // already valid
			}
		}
	}
	// No valid frontmatter — prepend a minimal one. Use a placeholder description
	// that the user can edit later. The skill name comes from the directory name arg.
	header := fmt.Sprintf("---\nname: %s\ndescription: \"TODO: describe what this skill does\"\nrequire_bins: []\n---\n\n", name)
	return header + trimmed
}

func validSkillName(name string) bool {
	if len(name) == 0 {
		return false
	}
	for _, c := range []byte(name) {
		if !((c >= 'A' && c <= 'Z') ||
			(c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// NewCreateSkillTool creates a create_skill tool bound to root.
func NewCreateSkillTool(root string) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "create_skill",
		Description: "Create a new local skill scaffold in the configured skills directory. After trimming, the name must use only ASCII letters, digits, hyphens, and underscores. Existing skills are never overwritten. Optionally pass 'content' with the full SKILL.md body to write real content instead of the boilerplate template. IMPORTANT: the content MUST begin with a YAML frontmatter block delimited by '---' lines containing at least 'name:' and 'description:' fields — without it the skill cannot be loaded. If you omit frontmatter it will be auto-injected with a placeholder description, so always include a meaningful description in the frontmatter.",
	}, CreateSkill(root))
}
