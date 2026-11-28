package docker

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/google/uuid"
)

// Scratch has a hard aggregate tmpfs capacity. Neither ordinary Home nor the
// private workspace is mounted in the workload container. All exits inventory
// scratch, but only a successful bounded result can publish a new state tree.
const workspaceScriptProgram = `import os,sys,json,base64,subprocess,resource,stat,signal,tempfile
q=json.load(sys.stdin)
def path(p):
 a=p.split('/')
 if not p or len(p)>4096 or len(a)>64 or '\\' in p or '\x00' in p or any(x in ('','.', '..') or x.startswith('.') for x in a): raise ValueError('path')
 return '/scratch/'+p
for f in q['files']:
 p=path(f['path']);os.makedirs(os.path.dirname(p),exist_ok=True)
 with open(p,'wb') as out: out.write(base64.b64decode(f['data'],validate=True))
# The workload has a different uid, cannot replace its input or signal the
# trusted inventory supervisor. No writable directory contains executable input.
for root,dirs,names in os.walk('/scratch'):
 os.chown(root,65534,65534)
 for n in names: os.chown(root+'/'+n,65534,65534)
os.chmod('/tmp',0o755)
script=base64.b64decode(q['script']['data'],validate=True)
with open('/scratch-input.py','wb') as f: f.write(script)
os.chmod('/scratch-input.py',0o444)
def limits():
 resource.setrlimit(resource.RLIMIT_FSIZE,(1048576,1048576));resource.setrlimit(resource.RLIMIT_CPU,(5,5));resource.setrlimit(resource.RLIMIT_NOFILE,(64,64))
# Disk-backed bounded capture avoids subprocess.PIPE's unbounded memory buffer.
with tempfile.TemporaryFile() as out, tempfile.TemporaryFile() as err:
 try:
  r=subprocess.run([sys.executable,'-I','/scratch-input.py'],cwd='/scratch',env={'PATH':'/usr/local/bin:/usr/bin:/bin','HOME':'/nonexistent'},timeout=10,stdout=out,stderr=err,user=65534,group=65534,extra_groups=[],start_new_session=True,preexec_fn=limits)
  code=r.returncode
 except subprocess.TimeoutExpired: code=124
 # Kill all residual workload processes, including children that created their
 # own process group, before reading files. PID namespace is container-private.
 for attempt in range(32):
  others=[int(n) for n in os.listdir('/proc') if n.isdigit() and int(n)!=os.getpid()]
  live=[]
  for pid in others:
   try:
    with open('/proc/'+str(pid)+'/stat') as status:
     if status.read().rsplit(')',1)[1].split()[0]=='Z': continue
    live.append(pid);os.kill(pid,signal.SIGKILL)
   except ProcessLookupError: pass
   except FileNotFoundError: pass
  if not live: break
  import time
  time.sleep(.01)
 else: raise ValueError('workload did not quiesce')
 out.seek(0);err.seek(0);stdout=out.read(65537);stderr=err.read(4097)
# Reject all special objects and over-quota results, including failure results.
files=[];size=0;count=0;dircount=0
for root,dirs,names in os.walk('/scratch',followlinks=False):
 for n in dirs:
  dircount+=1;path(os.path.relpath(root+'/'+n,'/scratch'))
  if dircount>8192 or not stat.S_ISDIR(os.lstat(root+'/'+n).st_mode): raise ValueError('state directory')
 for n in names:
  p=root+'/'+n;s=os.lstat(p);count+=1;size+=s.st_size
  if not stat.S_ISREG(s.st_mode) or s.st_nlink!=1 or s.st_size>1048576 or count>4096 or size>134217728: raise ValueError('state quota or link')
  rel=os.path.relpath(p,'/scratch');path(rel)
  with open(p,'rb') as f: data=f.read(1048577)
  if len(data)!=s.st_size: raise ValueError('state changed')
  files.append({'path':rel,'data':base64.b64encode(data).decode()})
if len(stdout)>65536 or len(stderr)>4096: raise ValueError('output quota')
print(json.dumps({'exit_code':code,'stdout':stdout.decode(errors='replace'),'stderr':stderr.decode(errors='replace'),'files':files}),flush=True)
`

func (db *DockerBackend) runWorkspaceScratch(ctx context.Context, h *sandbox.BotHomeBinding, input []byte) (data []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	name := "astonish-workspace-script-" + uuid.NewString()
	defer func() {
		c, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		raw, e := runDocker(c, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{json .Config.Labels}}", name)
		if e != nil {
			if !isDockerNotFoundError(e) {
				err = errors.Join(err, e)
			}
			return
		}
		var labels map[string]string
		if json.Unmarshal(raw, &labels) != nil || labels[botHomeReaderLabel] != name || labels[botHomeScopeLabel] != h.Scope.Key() {
			err = errors.Join(err, errors.New("script cleanup identity mismatch"))
			return
		}
		_, e = runDocker(c, db.cfg.ContainerRuntimePath, "rm", "-fv", name)
		err = errors.Join(err, e)
	}()
	cmd := exec.CommandContext(ctx, db.cfg.ContainerRuntimePath, "container", "run", "--interactive", "--name="+name, "--label="+botHomeReaderLabel+"="+name, "--label="+botHomeScopeLabel+"="+h.Scope.Key(), "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--cap-add=CHOWN", "--cap-add=SETUID", "--cap-add=SETGID", "--cap-add=KILL", "--security-opt=no-new-privileges", "--memory=256m", "--cpus=0.5", "--pids-limit=16", "--log-driver=none", "--tmpfs=/scratch:rw,nosuid,nodev,noexec,size=134217728", "--tmpfs=/tmp:rw,nosuid,nodev,noexec,size=2097152", "--entrypoint=python3", BotWorkspaceHelperImage, "-I", "-c", strings.Replace(workspaceScriptProgram, "/scratch-input.py", "/tmp/scratch-input.py", -1))
	cmd.Stdin = bytes.NewReader(input)
	out := &artifactBuffer{remaining: 192 << 20}
	stderr := &artifactBuffer{remaining: 4096}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if e := cmd.Run(); e != nil {
		return nil, fmt.Errorf("scratch script rejected: %w: %s", e, stderr.String())
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out.Bytes(), nil
}

func (db *DockerBackend) RunBotHomeWorkspaceScript(ctx context.Context, h *sandbox.BotHomeBinding, q sandbox.BotHomeWorkspaceScriptRequest) (*sandbox.BotHomeWorkspaceScriptResult, error) {
	if sandbox.ValidateBotHomeArtifactPath(q.TaskPath) != nil || !strings.HasPrefix(q.TaskPath, "tasks/") || strings.Count(q.TaskPath, "/") != 1 || sandbox.ValidateBotHomeArtifactPath(q.ScriptPath) != nil || !strings.HasPrefix(q.ScriptPath, "scripts/") || !strings.HasSuffix(q.ScriptPath, ".py") {
		return nil, errors.New("invalid script path")
	}
	if _, e := uuid.Parse(q.PublicationID); e != nil {
		return nil, e
	}
	if raw, e := hex.DecodeString(q.Digest); e != nil || len(raw) != 32 {
		return nil, errors.New("invalid script digest")
	}
	var r sandbox.BotHomeWorkspaceScriptResult
	operation := func() error {
		return db.workspace(ctx, h, map[string]any{"op": "script", "path": q.TaskPath, "publication": q.PublicationID, "script": q.ScriptPath, "digest": q.Digest}, &r)
	}
	var err error
	if q.BeforeCommit != nil {
		err = q.BeforeCommit(operation)
	} else {
		err = operation()
	}
	return &r, err
}
