package docker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/google/uuid"
)

func TestHomeFileTransactionSessionTransport(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		t.Run(fmt.Sprint(scoped), func(t *testing.T) {
			dir := t.TempDir()
			scope := sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()}
			b := &sandbox.BotHomeBinding{Scope: scope, Backend: sandbox.BackendKindDocker, Ref: botHomeVolume(scope)}
			runtime := filepath.Join(dir, "runtime")
			script := fmt.Sprintf(`#!/usr/bin/env python3
import sys,json,os
args=sys.argv[1:]
root=%q
scope=%q
home=%q
with open(root+'/trace','a') as f: f.write(args[0]+'\n')
if args[0]=='volume':
 print(json.dumps([{'Name':home,'Driver':'local','Options':{},'Labels':{'astonish.bot-home.scope':scope,'astonish.bot-home.version':'1'}}]))
elif args[0]=='container':
 name=next(x[7:] for x in args if x.startswith('--name='))
 with open(root+'/'+name,'w') as f: json.dump(args,f)
 print(name)
elif args[0]=='inspect':
 name=args[-1]
 if '.Mounts' in args[2]:
  print(json.dumps([{'Type':'volume','Name':%q,'Destination':'/mnt/astonish-bot-private','RW':True},{'Type':'volume','Name':home,'Destination':'/mnt/astonish-bot-home','RW':True}]))
 elif '.State' in args[2]: print('running')
 else:
  print(json.dumps({'Id':'physical-test-id','Config':{'Labels':{'astonish.session_id':'test-session'}},'State':{'Running':True},'Mounts':[{'Type':'volume','Name':%q,'Destination':'/mnt/astonish-bot-private','RW':True},{'Type':'volume','Name':home,'Destination':'/mnt/astonish-bot-home','RW':True}]}))
elif args[0]=='exec' or (args[0]=='start' and '--attach' in args):
 if any('test -x ' in a or 'mountpoint -q' in a for a in args): sys.exit(0)
 q=json.loads(sys.stdin.readline())
 print(json.dumps({'entries':[]}))
elif args[0]=='rm':
 os.unlink(root+'/'+args[-1])
`, dir, scope.Key(), b.Ref, workspaceVolume(b), workspaceVolume(b))
			if err := os.WriteFile(runtime, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			db := &DockerBackend{cfg: Config{ContainerRuntimePath: runtime, UppersDir: dir}}
			if err := db.BindBotHomeSession(b, "test-session"); err != nil {
				t.Fatal(err)
			}
			if err := db.writeSessionMetadata(newDockerSessionMetadata(sandbox.SessionSpec{SessionID: "test-session", BotHome: b, TemplateID: "tools", LayerChain: []string{"@base", "tools"}, Image: "test"})); err != nil {
				t.Fatal(err)
			}
			ctx := context.Context(t.Context())
			end := func() error { return nil }
			if scoped {
				ctx, end = sandbox.WithBotHomeFileOperation(ctx)
			}
			for i := 0; i < 3; i++ {
				if _, err := db.ListBotHomeFiles(ctx, b, ".", "", 10); err != nil {
					t.Fatal(err)
				}
			}
			if err := end(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "trace"))
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if got := strings.Count(string(raw), "container\n"); got != want {
				t.Fatalf("creates=%d want=%d trace=%s", got, want, raw)
			}
			if got := strings.Count(string(raw), "rm\n"); got != want {
				t.Fatalf("removes=%d want=%d", got, want)
			}
			if strings.Count(string(raw), "exec\n") < 3 {
				t.Fatal("not isolated exec per operation")
			}
			if strings.Count(string(raw), "volume\n") < 3 {
				t.Fatal("volume checks cached")
			}
		})
	}
}
