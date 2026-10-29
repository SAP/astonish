package common

import "fmt"

// FileToolPath reads canonical or historical file arguments without mutating
// persisted events. Ambiguous aliases never identify an artifact or target.
func FileToolPath(args map[string]any) string {
	path, err := fileToolPath(args)
	if err != nil {
		return ""
	}
	return path
}

func fileToolPath(args map[string]any) (string, error) {
	value, present := args["path"]
	legacy, hasLegacy := args["file_path"]
	if !present {
		value, present = legacy, hasLegacy
	}
	if !present {
		return "", fmt.Errorf("required argument %q is missing", "path")
	}
	if value == nil {
		return "", fmt.Errorf("required argument %q must not be null", "path")
	}
	path, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("required argument %q must be a string", "path")
	}
	if hasLegacy {
		old, ok := legacy.(string)
		if !ok || old != path {
			return "", fmt.Errorf("path and file_path must be identical strings when both are provided")
		}
	}
	return path, nil
}

// NormalizeFileToolArguments accepts the old file_path spelling at runtime,
// while all four public file schemas advertise only path. It never changes the
// caller's map; authorization can continue inspecting both original keys.
func NormalizeFileToolArguments(name string, args map[string]any) (map[string]any, error) {
	switch name {
	case "read_file", "write_file", "edit_file", "load_file":
	default:
		return args, nil
	}
	path, err := fileToolPath(args)
	if err != nil {
		return nil, fmt.Errorf("tool %q: %w", name, err)
	}
	normalized := make(map[string]any, len(args)+1)
	for key, value := range args {
		if key != "file_path" {
			normalized[key] = value
		}
	}
	normalized["path"] = path
	return normalized, nil
}
