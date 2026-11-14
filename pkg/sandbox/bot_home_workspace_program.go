package sandbox

// BotHomeWorkspaceProgram operates only on a backend-private root descriptor.
// The ordinary session Home is never mounted here. Both backends use identical
// locking, quota accounting, path confinement and staged commit semantics.
const BotHomeWorkspaceProgram = `
def parts(p):
 a=p.split('/')
 if not p or len(p)>4096 or any(x in ('','.', '..') for x in a) or '\\' in p or '\x00' in p or len(a)>64: raise ValueError('path')
 return a
def directory(p,create=False):
 fd=os.dup(root)
 try:
  for x in parts(p):
   if create:
    try: os.mkdir(x,0o700,dir_fd=fd);os.fsync(fd)
    except FileExistsError: pass
   n=os.open(x,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=fd);os.close(fd);fd=n
  return fd
 except: os.close(fd);raise
def parent(p,create=False):
 a=parts(p);return (directory('/'.join(a[:-1]),create) if len(a)>1 else os.dup(root)),a[-1]
def read(p,limit):
 d,n=parent(p)
 try:
  f=os.open(n,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK,dir_fd=d)
  try:
   s=os.fstat(f)
   if not stat.S_ISREG(s.st_mode) or s.st_nlink!=1 or s.st_size>limit: raise ValueError('regular-file/size')
   b=bytearray()
   while len(b)<=limit:
    chunk=os.read(f,min(65536,limit+1-len(b)))
    if not chunk: break
    b+=chunk
   if len(b)!=s.st_size or len(b)>limit: raise ValueError('incomplete read')
   return bytes(b)
  finally: os.close(f)
 finally: os.close(d)
def ready():
 print('WORKSPACE_READY',flush=True)
 if sys.stdin.readline().strip()!='commit': raise ValueError('commit rejected')
def write(p,b,guard=False):
 d,n=parent(p,True);temp='.tmp-'+uuid.uuid4().hex
 try:
  try:
   s=os.stat(n,dir_fd=d,follow_symlinks=False)
   if not stat.S_ISREG(s.st_mode) or s.st_nlink!=1: raise ValueError('regular-file')
  except FileNotFoundError: pass
  f=os.open(temp,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600,dir_fd=d)
  try:
   view=memoryview(b)
   while view: view=view[os.write(f,view):]
   os.fsync(f)
  finally: os.close(f)
  if guard: ready()
  os.rename(temp,n,src_dir_fd=d,dst_dir_fd=d);os.fsync(d)
 finally:
  try: os.unlink(temp,dir_fd=d)
  except FileNotFoundError: pass
  os.close(d)
def inventory(p,budget=None):
 if budget is None: budget=[8192,4096]
 d=directory(p);out=[]
 try:
  for n in sorted(os.listdir(d)):
   budget[0]-=1
   if budget[0]<0: raise ValueError('directory quota')
   s=os.stat(n,dir_fd=d,follow_symlinks=False);f=p+'/'+n
   if stat.S_ISDIR(s.st_mode): out+=inventory(f,budget)
   elif stat.S_ISREG(s.st_mode) and s.st_nlink==1:
    budget[1]-=1
    if budget[1]<0: raise ValueError('file quota')
    out.append((f,s.st_size))
   else: raise ValueError('link or special file')
 finally: os.close(d)
 return out
def allrows(budget=None):
 rows=[]
 if budget is None: budget=[8192,4096]
 for n in os.listdir(root):
  budget[0]-=1
  if budget[0]<0: raise ValueError('directory quota')
  s=os.stat(n,dir_fd=root,follow_symlinks=False)
  if stat.S_ISDIR(s.st_mode): rows+=inventory(n,budget)
  elif stat.S_ISREG(s.st_mode) and s.st_nlink==1:
   budget[1]-=1;rows.append((n,s.st_size))
  else: raise ValueError('link or special file')
 if budget[1]<0: raise ValueError('file quota')
 return rows
def quota(add,count=0,replace=None,paths=None):
 budget=[8192,4096];rows=allrows(budget)
 prospective=set()
 for p in paths or []:
  a=parts(p)
  for i in range(1,len(a)):
   prefix='/'.join(a[:i])
   if prefix in prospective: continue
   try: d=directory(prefix);os.close(d)
   except FileNotFoundError: prospective.add(prefix)
 if len(prospective)+count>budget[0]: raise ValueError('directory quota')
 if replace is not None: rows=[(p,s) for p,s in rows if p!=replace and not p.startswith(replace+'/')]
 if sum(s for _,s in rows)+add>536870912 or len(rows)+count>4096: raise ValueError('workspace quota')
def validhead(h):
 a=parts(h['task_path']);pub=h['publication_id']
 if len(a)!=2 or a[0]!='tasks' or a[1].startswith('.') or len(parts(pub))!=1 or pub.startswith('.') or h['receipt_ref']!=pub or h['binding_key']!=q['binding']: raise ValueError('head identity')
 entries=h['entries']
 if not 1<=len(entries)<=64 or sum(e['size_bytes'] for e in entries)>16777216: raise ValueError('manifest quota')
 last=''
 for e in entries:
  parts(e['path'])
  if e['path']<=last or any(x.startswith('.') for x in parts(e['path'])) or not 0<=e['size_bytes']<=1048576 or len(e['digest'])!=64: raise ValueError('manifest entry')
  last=e['path'];e.setdefault('executable',False)
 if hashlib.sha256(json.dumps(entries,sort_keys=True,separators=(',',':'),ensure_ascii=False).encode()).hexdigest()!=h['digest']: raise ValueError('manifest digest')
 return h
def head(p):
 h=validhead(json.loads(read(p+'/.head',1048576)))
 if h['task_path']!=p: raise ValueError('head path')
 return h
def snapshot(h):
 validhead(h);files=[]
 prefix=h['task_path']+'/.revisions/'+h['publication_id']+'/'
 actual=inventory(prefix[:-1])
 if sorted(p[len(prefix):] for p,_ in actual)!=[e['path'] for e in h['entries']]: raise ValueError('undeclared file')
 for e in h['entries']:
  b=read(prefix+e['path'],1048576)
  if len(b)!=e['size_bytes'] or hashlib.sha256(b).hexdigest()!=e['digest']: raise ValueError('digest mismatch')
  files.append({'path':e['path'],'data':base64.b64encode(b).decode(),'executable':e['executable']})
 return files
def physical(p):
 a=parts(p)
 if len(a)>=5 and a[0]=='tasks' and a[2]=='.revisions' and not any(x.startswith('.') for x in a[:2]+a[3:]):
  h=validhead(json.loads(read('.receipts/'+a[3],1048576)));snapshot(h)
  if h['task_path']!='/'.join(a[:2]) or '/'.join(a[4:]) not in [e['path'] for e in h['entries']]: raise ValueError('revision read binding')
  return p
 if any(x.startswith('.') for x in a): raise ValueError('private path')
 if len(a)>=3 and a[0]=='tasks' and a[2] not in ('state','outputs','drafts'):
  h=head('/'.join(a[:2]));snapshot(h)
  if '/'.join(a[2:]) not in [e['path'] for e in h['entries']]: raise ValueError('undeclared read')
  return '/'.join(a[:2])+'/.revisions/'+h['publication_id']+'/'+ '/'.join(a[2:])
 return p
lock=os.open('.workspace-lock',os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW|os.O_NONBLOCK,0o600,dir_fd=root)
s=os.fstat(lock)
if not stat.S_ISREG(s.st_mode) or s.st_nlink!=1: raise ValueError('lock')
fcntl.flock(lock,fcntl.LOCK_EX)
op=q['op'];p=q.get('path','');result=None
if op=='publish':
 r=q['request'];p=r['TaskPath'];pub=r['PublicationID'];files=r['Files']
 entries=[]
 for f in files:
  b=base64.b64decode(f['data'],validate=True)
  entries.append({'path':f['path'],'size_bytes':len(b),'digest':hashlib.sha256(b).hexdigest(),'executable':f.get('executable',False)})
 entries.sort(key=lambda e:e['path'])
 digest=hashlib.sha256(json.dumps(entries,sort_keys=True,separators=(',',':'),ensure_ascii=False).encode()).hexdigest()
 result={'receipt_ref':pub,'publication_id':pub,'binding_key':q['binding'],'task_path':p,'digest':digest,'entries':entries}
 validhead(result)
 try: old=head(p)
 except FileNotFoundError: old=None
 if old and old['publication_id']==pub:
  snapshot(old)
  if old!=result: raise ValueError('edited retry')
  print(json.dumps(old));sys.exit(0)
 if (old['digest'] if old else '')!=r['ExpectedDigest']: raise ValueError('head CAS')
 stage='stage-'+uuid.uuid4().hex
 quota(sum(e['size_bytes'] for e in entries)+2097152,len(files)+2,paths=[p+'/.revisions/'+stage+'/'+f['path'] for f in files]+['.receipts/'+pub,p+'/.head'])
 d=directory(p+'/.revisions',True)
 try:
  try: os.stat(pub,dir_fd=d,follow_symlinks=False);exists=True
  except FileNotFoundError: exists=False
  if not exists:
   os.mkdir(stage,0o700,dir_fd=d);os.fsync(d)
   for f in files: write(p+'/.revisions/'+stage+'/'+f['path'],base64.b64decode(f['data'],validate=True))
   staged=dict(result);staged['publication_id']=stage;staged['receipt_ref']=stage;snapshot(staged)
   ready()
   os.rename(stage,pub,src_dir_fd=d,dst_dir_fd=d);os.fsync(d)
 finally: os.close(d)
 snapshot(result)
 ready()
 write('.receipts/'+pub,json.dumps(result).encode())
 write(p+'/.head',json.dumps(result).encode())
elif op=='resolve':
 if len(parts(p))!=1 or p.startswith('.'): raise ValueError('receipt')
 result=validhead(json.loads(read('.receipts/'+p,1048576)))
 if result['receipt_ref']!=p: raise ValueError('receipt identity')
 snapshot(result)
elif op=='read': result=base64.b64encode(read(physical(p),q['limit'])).decode()
elif op=='write':
 a=parts(p)
 if len(a)<4 or a[0]!='tasks' or a[2] not in ('state','outputs','drafts') or any(x.startswith('.') for x in a): raise ValueError('mutable path')
 b=base64.b64decode(q['data'],validate=True)
 if len(b)>1048576: raise ValueError('asset quota')
 quota(len(b),1,paths=[p]);rows=[]
 try: rows=inventory('/'.join(a[:3]))
 except FileNotFoundError: pass
 if sum(s for f,s in rows if f!=p)+len(b)>134217728: raise ValueError('state quota')
 write(p,b,True)
elif op=='script':
 a=parts(p)
 if len(a)!=2 or a[0]!='tasks' or a[1].startswith('.'): raise ValueError('task path')
 h=validhead(json.loads(read('.receipts/'+q['publication'],1048576)))
 if h['task_path']!=p: raise ValueError('script binding')
 files=snapshot(h)
 script=next(f for f in files if f['path']==q['script'])
 if not script['executable'] or hashlib.sha256(base64.b64decode(script['data'])).hexdigest()!=q['digest']: raise ValueError('script digest')
 rows=[]
 try: rows=inventory(p+'/state')
 except FileNotFoundError: pass
 if sum(s for _,s in rows)>134217728: raise ValueError('state quota')
 state=[{'path':f[len(p+'/state/'):],'data':base64.b64encode(read(f,1048576)).decode()} for f,_ in rows]
 print('WORKSPACE_SCRIPT '+json.dumps({'script':script,'files':state}),flush=True)
 update=json.loads(sys.stdin.readline())
 if update.get('exit_code',1)!=0: raise ValueError('script failed; state unchanged')
 new=update['files'];seen=set();total=0
 for f in new:
  a=parts(f['path'])
  if any(x.startswith('.') for x in a) or f['path'] in seen: raise ValueError('state path')
  seen.add(f['path']);b=base64.b64decode(f['data'],validate=True);total+=len(b)
  if len(b)>1048576: raise ValueError('state asset quota')
 if total>134217728: raise ValueError('state quota')
 stage='state-stage-'+uuid.uuid4().hex
 quota(total,len(new),paths=[p+'/'+stage+'/'+f['path'] for f in new]+[p+'/'+stage+'/placeholder'])
 # Atomic selection of a complete state tree: previous generations stay in
 # protected recovery storage until removed under the same lock.
 d=directory(p,True);stage='state-stage-'+uuid.uuid4().hex
 os.mkdir(stage,0o700,dir_fd=d)
 try:
  for f in new: write(p+'/'+stage+'/'+f['path'],base64.b64decode(f['data'],validate=True))
  if sum(s for _,s in inventory(p+'/'+stage))!=total: raise ValueError('state inventory')
  ready()
  # Linux renameat2 exchanges directories atomically, avoiding missing state.
  import ctypes,shutil
  try: os.stat('state',dir_fd=d,follow_symlinks=False);exists=True
  except FileNotFoundError: exists=False
  if exists:
   libc=ctypes.CDLL(None,use_errno=True)
   if libc.renameat2(d,stage.encode(),d,b'state',2)!=0: raise OSError(ctypes.get_errno(),'state exchange')
  else: os.rename(stage,'state',src_dir_fd=d,dst_dir_fd=d)
  os.fsync(d)
 finally:
  # All descendants came from confined writes; no workload runs on this mount.
  import shutil
  shutil.rmtree('/proc/self/fd/'+str(d)+'/'+stage,ignore_errors=True);os.close(d)
 result={'exit_code':0,'stdout':update.get('stdout',''),'stderr':update.get('stderr','')}
elif op=='restore':
 s=q['snapshot'];files=s['Files'];source=s['Receipt']['binding_key']
 if any(n!='.workspace-lock' for n in os.listdir(root)): raise ValueError('restore requires fresh empty workspace')
 decoded=[];seen=set();total=0
 for f in files:
  a=parts(f['path']);b=base64.b64decode(f['data'],validate=True)
  if f['path']=='.workspace-lock' or f['path'] in seen or len(b)>134217728: raise ValueError('restore file')
  seen.add(f['path']);total+=len(b)
  if a[0]=='.receipts' or a[-1]=='.head':
   h=json.loads(b)
   if h['binding_key']!=source: raise ValueError('foreign archive receipt')
   h['binding_key']=q['binding'];validhead(h);b=json.dumps(h).encode()
  decoded.append((f['path'],b))
 if total>16777216 or len(decoded)>4096: raise ValueError('restore quota')
 content=dict(decoded)
 for p,b in decoded:
  if p.startswith('.receipts/') or p.endswith('/.head'):
   h=validhead(json.loads(b));prefix=h['task_path']+'/.revisions/'+h['publication_id']+'/'
   if sorted(x[len(prefix):] for x in content if x.startswith(prefix))!=[e['path'] for e in h['entries']]: raise ValueError('restore revision inventory')
   for e in h['entries']:
    value=content[prefix+e['path']]
    if len(value)!=e['size_bytes'] or hashlib.sha256(value).hexdigest()!=e['digest']: raise ValueError('restore revision digest')
 ready()
 # Explicit offline recovery: interrupted copies remain held, not executable.
 for p,b in decoded: write(p,b)
 result={'restored':len(decoded)}
elif op=='snapshot':
 if p=='workspace':
  rows=sorted((f,s) for f,s in allrows() if f!='.workspace-lock')
  if sum(s for _,s in rows)>q['limit']: raise ValueError('snapshot limit')
  files=[];entries=[]
  for f,s in rows:
   b=read(f,134217728)
   files.append({'path':f,'data':base64.b64encode(b).decode(),'executable':False})
   entries.append({'path':f,'size_bytes':len(b),'digest':hashlib.sha256(b).hexdigest(),'executable':False})
  digest=hashlib.sha256(json.dumps(entries,sort_keys=True,separators=(',',':')).encode()).hexdigest()
  result={'Files':files,'Receipt':{'binding_key':q['binding'],'task_path':'workspace','digest':digest,'entries':entries}}
 else:
  h=head(p);files=snapshot(h)
  if sum(e['size_bytes'] for e in h['entries'])>q['limit']: raise ValueError('snapshot limit')
  result={'Files':files,'Receipt':h}
elif op=='list':
 h=head(p);snapshot(h);rows=[e for e in h['entries'] if e['path']>q['after']];n=q['limit'];result={'entries':rows[:n],'next':rows[n-1]['path'] if len(rows)>n else ''}
else: raise ValueError('operation')
print(json.dumps(result),flush=True)
`
