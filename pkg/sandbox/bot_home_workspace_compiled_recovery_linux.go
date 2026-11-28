//go:build linux

package sandbox

import (
	"errors"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

// Revision stages use a private namespace because historical stage-UUID names
// are valid publication IDs. Never infer ownership of those historical names
// from missing receipts: a crash after revision selection is an uncertain effect.
func workspaceRevisionStage(path string) bool {
	parts := strings.Split(path, "/")
	if len(parts) != 4 || parts[0] != "tasks" || parts[2] != ".revisions" {
		return false
	}
	suffix, ok := strings.CutPrefix(parts[3], ".revision-stage-")
	id, err := uuid.Parse(suffix)
	return ok && err == nil && id.String() == suffix
}

// Requires exclusive root lifetime AND exclusive .workspace-lock. No receipt,
// PID, age, staged bytes or host approval is consulted as cleanup authority.
// Fully validate every candidate tree before unlinking; links/special files and
// cross-mount lookups fail closed. Existing revisions and state are never adopted.
func workspaceReclaimStages(root *os.File) error {
	nodes := 0
	var walk func(*os.File, string, bool) error
	walk = func(parent *os.File, path string, remove bool) error {
		scan, err := homeOpen(parent, ".", unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			return err
		}
		defer scan.Close()
		for {
			entries, err := scan.ReadDir(128)
			if err != nil && err != io.EOF {
				return err
			}
			for _, entry := range entries {
				nodes++
				if nodes > 8192 {
					return errors.New("workspace recovery directory quota")
				}
				name := entry.Name()
				child := name
				if path != "" {
					child = path + "/" + name
				}
				candidate := strings.HasPrefix(name, string(homeWorkspaceStagePrefix)) && homeInternalStage(name)
				revision := workspaceRevisionStage(child)
				f, e := homeOpen(parent, name, unix.O_RDONLY, 0)
				if e != nil {
					return e
				}
				s, e := homeStat(f, false)
				if e != nil {
					f.Close()
					return e
				}
				if s.Mode&unix.S_IFMT == unix.S_IFDIR {
					if candidate {
						f.Close()
						return errHomeForbidden
					}
					// Validate the whole reserved directory before deleting any child.
					if revision && !remove {
						_, _, e = (&compiledWorkspace{root: f}).inventory(".")
						if e != nil {
							f.Close()
							return e
						}
					}
					e = walk(f, child, remove || revision)
					if e == nil && (remove || revision) {
						now, openErr := homeOpen(parent, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
						if openErr != nil {
							e = openErr
						} else {
							e = workspaceSameDirectory(f, now)
							now.Close()
						}
						if e == nil {
							e = unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
						}
					}
				} else if s.Mode&unix.S_IFMT != unix.S_IFREG || s.Nlink != 1 || revision {
					e = errHomeForbidden
				} else if remove || candidate {
					now, openErr := homeOpen(parent, name, unix.O_RDONLY, 0)
					if openErr != nil {
						e = openErr
					} else {
						current, statErr := homeStat(now, true)
						now.Close()
						e = statErr
						if e == nil && !homeIdentity(s, current) {
							e = errHomeForbidden
						}
					}
					if e == nil {
						e = unix.Unlinkat(int(parent.Fd()), name, 0)
					}
				}
				f.Close()
				if e != nil {
					return e
				}
			}
			if err == io.EOF {
				break
			}
		}
		return parent.Sync()
	}
	return walk(root, "", false)
}
