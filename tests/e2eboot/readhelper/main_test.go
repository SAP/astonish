package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadOnlySnapshotAndProtocol(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, ".tmp", "astonish-chat-first-test")
	if err = os.MkdirAll(filepath.Join(root, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "data", "fixture.db")
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA journal_mode=WAL", "CREATE TABLE bots(id INTEGER, data BLOB)", "CREATE TABLE scheduled_jobs(version INTEGER)", "INSERT INTO bots VALUES(1,x'00ff')", "INSERT INTO scheduled_jobs VALUES(1)"} {
		if _, err = writer.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	in, send, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	receive, out, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = in, out
	done := make(chan error, 1)
	go func() { done <- serve(root) }()
	defer func() {
		send.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
		in.Close()
		out.Close()
		receive.Close()
		os.Stdin, os.Stdout = oldIn, oldOut
	}()
	enc, dec := json.NewEncoder(send), json.NewDecoder(receive)
	call := func(r request) response {
		t.Helper()
		if e := enc.Encode(r); e != nil {
			t.Fatal(e)
		}
		var result response
		if e := dec.Decode(&result); e != nil {
			t.Fatal(e)
		}
		return result
	}
	ok := func(r request) response {
		t.Helper()
		result := call(r)
		if result.Error != "" {
			t.Fatal(result.Error)
		}
		return result
	}
	paths := ok(request{Op: "paths"})
	if len(paths.Paths) != 1 || paths.Paths[0] != path {
		t.Fatal(paths)
	}
	ok(request{Op: "open", Path: path})
	ok(request{Op: "query", SQL: "BEGIN"})
	first := ok(request{Op: "query", SQL: "SELECT id, data FROM bots"})
	if first.Rows[0][0] != float64(1) || first.Rows[0][1].(map[string]any)["sqlite_blob_base64"] != "AP8=" {
		t.Fatal(first)
	}
	// A commit between projected tables must not tear the observer snapshot.
	tx, err := writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE bots SET id=2"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE scheduled_jobs SET version=2"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	jobs := ok(request{Op: "query", SQL: "SELECT version FROM scheduled_jobs"})
	if jobs.Rows[0][0] != float64(1) {
		t.Fatal("torn snapshot", jobs)
	}
	for _, q := range []string{"UPDATE bots SET id=7", "PRAGMA wal_checkpoint(TRUNCATE)", "SELECT id FROM bots; DELETE FROM bots", "ATTACH DATABASE ':memory:' AS other"} {
		if call(request{Op: "query", SQL: q}).Error == "" {
			t.Fatal("accepted non-observer SQL", q)
		}
	}
	ok(request{Op: "close"})
	ok(request{Op: "open", Path: path})
	ok(request{Op: "query", SQL: "BEGIN"})
	current := ok(request{Op: "query", SQL: "SELECT id FROM bots"})
	if current.Rows[0][0] != float64(2) {
		t.Fatal("fresh transaction stale", current)
	}
	ok(request{Op: "close"})
	if call(request{Op: "open", Path: filepath.Join(root, "data", "absent.db")}).Error == "" {
		t.Fatal("created missing database")
	}
	if _, err = os.Stat(filepath.Join(root, "data", "absent.db")); !os.IsNotExist(err) {
		t.Fatal("missing DB created")
	}
	link := filepath.Join(root, "data", "link.db")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if call(request{Op: "open", Path: link}).Error == "" {
		t.Fatal("accepted symlink")
	}
}
