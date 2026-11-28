// Read-only SQL transport for the employee acceptance observer. No HTTP or writes.
package main

import (
	"bufio"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type request struct {
	Op   string `json:"op"`
	Path string `json:"path"`
	SQL  string `json:"sql"`
	Args []any  `json:"args"`
}
type response struct {
	Paths   []string `json:"paths,omitempty"`
	Columns []string `json:"columns,omitempty"`
	Rows    [][]any  `json:"rows,omitempty"`
	Error   string   `json:"error,omitempty"`
}

func ownedPath(root, path string) (string, error) {
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	data := filepath.Join(root, "data")
	if !strings.HasPrefix(p, data+string(os.PathSeparator)) || p != filepath.Clean(path) {
		return "", fmt.Errorf("database escaped owned root or symlink")
	}
	return p, nil
}
func serve(root string) error {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) || !strings.HasPrefix(filepath.Base(root), "astonish-chat-first-") || filepath.Base(filepath.Dir(root)) != ".tmp" {
		return fmt.Errorf("invalid owned root")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return fmt.Errorf("invalid owned root symlink")
	}
	var db *sql.DB
	defer func() {
		if db != nil {
			db.Close()
		}
	}()
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var r request
		result := response{}
		err := json.Unmarshal(scanner.Bytes(), &r)
		if err == nil {
			switch r.Op {
			case "paths":
				err = filepath.WalkDir(filepath.Join(root, "data"), func(p string, d os.DirEntry, e error) error {
					if e != nil {
						return e
					}
					if !d.IsDir() && strings.HasSuffix(p, ".db") {
						if _, e = ownedPath(root, p); e != nil {
							return e
						}
						result.Paths = append(result.Paths, p)
					}
					return nil
				})
			case "open":
				if db != nil {
					db.Close()
					db = nil
				}
				var p string
				p, err = ownedPath(root, r.Path)
				if err == nil {
					u := url.URL{Scheme: "file", Path: p}
					db, err = sql.Open("sqlite", u.String()+"?mode=ro")
					if err == nil {
						db.SetMaxOpenConns(1)
						_, err = db.Exec("PRAGMA query_only=ON")
					}
				}
			case "query":
				if db == nil {
					err = fmt.Errorf("no database")
					break
				}
				// These are the observer's only statements. URI mode=ro and query_only
				// independently enforce read-only access even for a malformed request.
				q := strings.TrimSpace(r.SQL)
				if q == "BEGIN" || q == "ROLLBACK" || q == "PRAGMA query_only=ON" {
					_, err = db.Exec(q)
					break
				}
				if !(strings.HasPrefix(q, "SELECT ") || strings.HasPrefix(q, "PRAGMA table_info(")) || strings.Contains(q, ";") {
					err = fmt.Errorf("non-observer SQL rejected")
					break
				}
				var rows *sql.Rows
				rows, err = db.Query(q, r.Args...)
				if err == nil {
					result.Columns, err = rows.Columns()
					for err == nil && rows.Next() {
						values := make([]any, len(result.Columns))
						ptrs := make([]any, len(values))
						for i := range values {
							ptrs[i] = &values[i]
						}
						err = rows.Scan(ptrs...)
						if err != nil {
							break
						}
						for i, v := range values {
							if b, ok := v.([]byte); ok {
								values[i] = map[string]any{"sqlite_blob_base64": base64.StdEncoding.EncodeToString(b)}
							}
						}
						result.Rows = append(result.Rows, values)
					}
					if err == nil {
						err = rows.Err()
					}
					rows.Close()
				}
			case "close":
				if db != nil {
					err = db.Close()
					db = nil
				}
			default:
				err = fmt.Errorf("unknown observer operation")
			}
		}
		if err != nil {
			result = response{Error: err.Error()}
		}
		if err = encoder.Encode(result); err != nil {
			return err
		}
	}
	return scanner.Err()
}
func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "owned root required")
		os.Exit(2)
	}
	if err := serve(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
