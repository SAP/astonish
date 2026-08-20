package tools

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/safepath"
	"github.com/SAP/astonish/pkg/skills"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

const maxSkillFileBytes = 256 * 1024

// SkillLookupMode controls which skill sources the lookup is allowed to use.
type SkillLookupMode string

const (
	SkillLookupModeLocal    SkillLookupMode = "local"
	SkillLookupModeCode     SkillLookupMode = "code"
	SkillLookupModePlatform SkillLookupMode = "platform"
)

// SkillLookupArgs defines the arguments for the skill_lookup tool.
type SkillLookupArgs struct {
	Name     string `json:"name" jsonschema:"Skill name from the Available Skills list (e.g. github, docker, git)"`
	File     string `json:"file,omitempty" jsonschema:"Optional relative path to a specific file within the skill (e.g. 'scripts/deploy.sh' or 'references/api.md'). If omitted, returns the main SKILL.md plus a files manifest."`
	Path     string `json:"path,omitempty" jsonschema:"Alternative to 'file': directory part (e.g. 'scripts')"`
	Filename string `json:"filename,omitempty" jsonschema:"Alternative to 'file': filename part (e.g. 'deploy.sh')"`
}

// SkillLookupResult is returned from skill_lookup.
type SkillLookupResult struct {
	Name                string              `json:"name"`
	Description         string              `json:"description"`
	Content             string              `json:"content"`
	File                string              `json:"file,omitempty"`
	Directory           string              `json:"directory,omitempty"`
	Files               []string            `json:"files,omitempty"`
	FilesManifest       map[string][]string `json:"files_manifest,omitempty"`
	MissingRequirements []string            `json:"missing_requirements,omitempty"`
	Error               string              `json:"error,omitempty"`
}

// SkillLookup returns full skill content by name. Platform mode consults tenant
// stores in team > org > platform order before filesystem and builtin skills.
// Local and Code modes never consult context-injected stores.
//
// The variadic mode preserves source compatibility until all launcher call sites
// are migrated. An omitted mode retains the historical platform behavior.
func SkillLookup(allSkills []skills.Skill, modes ...SkillLookupMode) func(ctx tool.Context, args SkillLookupArgs) (SkillLookupResult, error) {
	mode := lookupMode(modes)
	builtins := skills.BuiltinSkills()
	staticIndex := make(map[string]*skills.Skill, len(builtins)+len(allSkills))
	for i := range builtins {
		staticIndex[strings.ToLower(builtins[i].Name)] = &builtins[i]
	}
	for i := range allSkills {
		staticIndex[strings.ToLower(allSkills[i].Name)] = &allSkills[i]
	}

	return func(ctx tool.Context, args SkillLookupArgs) (SkillLookupResult, error) {
		if strings.TrimSpace(args.Name) == "" {
			return SkillLookupResult{Error: "name is required"}, nil
		}
		name := strings.ToLower(strings.TrimSpace(args.Name))

		if mode == SkillLookupModePlatform && ctx != nil {
			if ss := store.SkillStoresFromContext(ctx); ss != nil {
				for _, skillStore := range []store.SkillStore{ss.Team, ss.Org, ss.Platform} {
					if skillStore == nil {
						continue
					}
					if skill, err := skillStore.Get(ctx, name); err == nil && skill != nil {
						return handlePlatformSkillLookup(ctx, skillStore, skill, args), nil
					}
				}
			}
		}

		skill, ok := staticIndex[name]
		if !ok {
			names := collectAllSkillNames(staticIndex, ctx, mode)
			if len(names) == 0 {
				return SkillLookupResult{Error: fmt.Sprintf("skill %q not found. No skills are configured.", args.Name)}, nil
			}
			return SkillLookupResult{Error: fmt.Sprintf("skill %q not found. Available: %s", args.Name, strings.Join(names, ", "))}, nil
		}
		return handleFilesystemSkillLookup(skill, args), nil
	}
}

func lookupMode(modes []SkillLookupMode) SkillLookupMode {
	if len(modes) == 0 {
		return SkillLookupModePlatform
	}
	return modes[0]
}

func handleFilesystemSkillLookup(skill *skills.Skill, args SkillLookupArgs) SkillLookupResult {
	result := SkillLookupResult{Name: skill.Name, Description: skill.Description, Content: skill.Content}
	if missing := skill.MissingRequirements(); len(missing) > 0 {
		result.MissingRequirements = missing
	}
	if skill.Directory == "" {
		if requestedFile(args) != "" {
			result.Content = ""
			result.Error = fmt.Sprintf("skill %q has no filesystem files", skill.Name)
		}
		return result
	}

	root, err := filepath.EvalSymlinks(skill.Directory)
	if err != nil {
		result.Error = fmt.Sprintf("failed to access files for skill %q", skill.Name)
		return result
	}
	root, err = filepath.Abs(root)
	if err != nil {
		result.Error = fmt.Sprintf("failed to access files for skill %q", skill.Name)
		return result
	}
	result.Directory = root

	if filePath := requestedFile(args); filePath != "" {
		cleaned, err := cleanSkillRelativePath(filePath)
		if err != nil {
			result.Content = ""
			result.Error = err.Error()
			return result
		}
		data, err := readContainedSkillFile(root, cleaned)
		result.Content = ""
		if err != nil {
			result.Error = fmt.Sprintf("failed to load file %q from skill %q: %v", cleaned, skill.Name, err)
			return result
		}
		result.File = cleaned
		result.Content = string(data)
		return result
	}

	files, manifest, err := filesystemSkillManifest(root)
	if err != nil {
		result.Error = fmt.Sprintf("failed to list files for skill %q: %v", skill.Name, err)
		return result
	}
	result.Files = files
	result.FilesManifest = manifest
	return result
}

func requestedFile(args SkillLookupArgs) string {
	filePath := strings.TrimSpace(args.File)
	if filePath == "" && strings.TrimSpace(args.Filename) != "" {
		filePath = strings.TrimSpace(args.Filename)
		if strings.TrimSpace(args.Path) != "" {
			filePath = strings.TrimSpace(args.Path) + "/" + filePath
		}
	}
	return filePath
}

func cleanSkillRelativePath(path string) (string, error) {
	if path == "" || strings.Contains(path, "\\") || filepath.IsAbs(path) {
		return "", fmt.Errorf("invalid file path: must be a clean relative path without '..' or leading '/'")
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if cleaned == "." || cleaned != path || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("invalid file path: must be a clean relative path without '..' or leading '/'")
	}
	return cleaned, nil
}

func readContainedSkillFile(root, relative string) ([]byte, error) {
	candidate := filepath.Join(root, filepath.FromSlash(relative))
	if err := safepath.ContainedWithin(candidate, root); err != nil {
		return nil, fmt.Errorf("path escapes skill directory")
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found")
		}
		return nil, fmt.Errorf("file is not accessible")
	}
	if err := safepath.ContainedWithin(resolved, root); err != nil {
		return nil, fmt.Errorf("path escapes skill directory through a symlink")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("path is not a regular file")
	}
	f, err := os.Open(resolved)
	if err != nil {
		return nil, fmt.Errorf("file is not accessible")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSkillFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("file is not readable")
	}
	if len(data) > maxSkillFileBytes {
		return nil, fmt.Errorf("file exceeds 256KiB limit")
	}
	return data, nil
}

func filesystemSkillManifest(root string) ([]string, map[string][]string, error) {
	var files []string
	manifest := make(map[string][]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("path escapes skill directory")
		}
		rel = filepath.ToSlash(rel)
		files = append(files, rel)
		dir, name := filepath.ToSlash(filepath.Dir(rel)), filepath.Base(rel)
		if dir == "." {
			dir = ""
		}
		manifest[dir] = append(manifest[dir], name)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(files)
	for dir := range manifest {
		sort.Strings(manifest[dir])
	}
	if len(manifest) == 0 {
		manifest = nil
	}
	return files, manifest, nil
}

func storeSkillToResult(s *store.Skill) SkillLookupResult {
	parsed, err := skills.ParseSkillFile("store:"+s.Name, []byte(s.Content))
	if err != nil {
		return SkillLookupResult{Name: s.Name, Description: s.Description, Content: s.Content}
	}
	result := SkillLookupResult{Name: parsed.Name, Description: parsed.Description, Content: parsed.Content}
	if missing := parsed.MissingRequirements(); len(missing) > 0 {
		result.MissingRequirements = missing
	}
	return result
}

func collectAllSkillNames(staticIndex map[string]*skills.Skill, ctx tool.Context, mode SkillLookupMode) []string {
	nameSet := make(map[string]struct{}, len(staticIndex))
	for _, skill := range staticIndex {
		nameSet[skill.Name] = struct{}{}
	}
	if mode == SkillLookupModePlatform && ctx != nil {
		if ss := store.SkillStoresFromContext(ctx); ss != nil {
			for _, skillStore := range []store.SkillStore{ss.Platform, ss.Org, ss.Team} {
				if skillStore == nil {
					continue
				}
				if stored, err := skillStore.List(ctx); err == nil {
					for _, skill := range stored {
						nameSet[skill.Name] = struct{}{}
					}
				}
			}
		}
	}
	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// NewSkillLookupTool creates the skill_lookup tool. See SkillLookup for the
// temporary omitted-mode compatibility behavior.
func NewSkillLookupTool(allSkills []skills.Skill, modes ...SkillLookupMode) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "skill_lookup",
		Description: "Load full instructions for a CLI tool skill by name. " +
			"Use this to learn how to use a CLI tool or workflow before executing commands. " +
			"If a skill references environment variables for auth, resolve them from the credential store. " +
			"Check the Available Skills list in the system prompt for valid skill names.",
	}, SkillLookup(allSkills, modes...))
}

func handlePlatformSkillLookup(ctx tool.Context, skillStore store.SkillStore, skill *store.Skill, args SkillLookupArgs) SkillLookupResult {
	if !skills.IsUsableStatus(skill.ValidationStatus) {
		return SkillLookupResult{Name: skill.Name, Error: fmt.Sprintf("Skill %q is blocked (validation_status: %q). A team member must validate and acknowledge any critical security issues in Settings → Skills before this skill can be used.", skill.Name, skill.ValidationStatus)}
	}
	if filePath := requestedFile(args); filePath != "" {
		cleaned, err := cleanSkillRelativePath(filePath)
		if err != nil {
			return SkillLookupResult{Name: skill.Name, Error: err.Error()}
		}
		dir, name := filepath.ToSlash(filepath.Dir(cleaned)), filepath.Base(cleaned)
		if dir == "." {
			dir = ""
		}
		f, err := skillStore.GetFile(ctx, skill.Name, dir, name)
		if err != nil {
			return SkillLookupResult{Name: skill.Name, Error: fmt.Sprintf("failed to load file %q from skill %q (database error)", cleaned, skill.Name)}
		}
		if f == nil {
			return SkillLookupResult{Name: skill.Name, Error: fmt.Sprintf("file %q not found in skill %q", cleaned, skill.Name)}
		}
		if len(f.Content) > maxSkillFileBytes {
			return SkillLookupResult{Name: skill.Name, Error: fmt.Sprintf("file %q exceeds 256KiB limit", cleaned)}
		}
		return SkillLookupResult{Name: skill.Name, File: cleaned, Content: f.Content}
	}

	result := storeSkillToResult(skill)
	if files, err := skillStore.ListFiles(ctx, skill.Name); err == nil && len(files) > 0 {
		result.FilesManifest = make(map[string][]string)
		for _, f := range files {
			result.FilesManifest[f.Path] = append(result.FilesManifest[f.Path], f.Filename)
			full := f.Filename
			if f.Path != "" {
				full = f.Path + "/" + f.Filename
			}
			result.Files = append(result.Files, full)
		}
		sort.Strings(result.Files)
		for path := range result.FilesManifest {
			sort.Strings(result.FilesManifest[path])
		}
	}
	return result
}
