// Controlled fixture only: owns its fresh database, never accepts an existing DB.
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func main() {
	root, bot, peer := os.Args[1], os.Args[2], os.Args[3]
	p := filepath.Join(root, "data", "fixture.db")
	must(os.MkdirAll(filepath.Dir(p), 0700))
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		panic("fixture already exists")
	}
	db, err := sql.Open("sqlite", p)
	must(err)
	db.SetMaxOpenConns(1)
	defer db.Close()
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0", "CREATE TABLE bots(id TEXT,home_session_id TEXT,version INTEGER,credentials BLOB)", "CREATE TABLE bot_runs(id TEXT,bot_id TEXT,data TEXT)", "CREATE TABLE scheduled_jobs(id TEXT,mode TEXT,payload TEXT,status TEXT)"} {
		_, err = db.Exec(q)
		must(err)
	}
	_, err = db.Exec("INSERT INTO bots VALUES (?,?,?,?), (?,?,?,?)", bot, "owned-home", 0, []byte("OWNED-SECRET"), peer, "peer-home", 99, []byte("PEER-SECRET"))
	must(err)
	payload, _ := json.Marshal(map[string]string{"bot_id": peer})
	_, err = db.Exec("INSERT INTO scheduled_jobs VALUES (?,?,?,?)", "peer-job", "bot_trigger", string(payload), "99")
	must(err)
	version := 0
	for {
		requested := 1
		if data, e := os.ReadFile("/tmp/namespace-command"); e == nil {
			requested, e = strconv.Atoi(string(data))
			must(e)
		}
		if requested != version {
			tx, e := db.Begin()
			must(e)
			_, e = tx.Exec("UPDATE bots SET version=? WHERE id=?", requested, bot)
			must(e)
			_, e = tx.Exec("DELETE FROM scheduled_jobs WHERE id=?", "owned-job")
			must(e)
			payload, _ = json.Marshal(map[string]string{"bot_id": bot})
			_, e = tx.Exec("INSERT INTO scheduled_jobs VALUES (?,?,?,?)", "owned-job", "bot_trigger", string(payload), strconv.Itoa(requested))
			must(e)
			must(tx.Commit())
			must(db.QueryRow("SELECT version FROM bots WHERE id=?", bot).Scan(&version))
			must(os.WriteFile("/tmp/namespace-ack", []byte(fmt.Sprint(version)), 0600))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
