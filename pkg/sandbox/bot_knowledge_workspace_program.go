package sandbox

// BotKnowledgeWorkspaceProgram is inserted after acquiring the workspace flock.
// One directory exchange selects every proposal and its immutable publication
// receipt together. Readers share the same lock and never see partial batches.
const BotKnowledgeWorkspaceProgram = `
def knowledgepath(p):
 a=parts(p)
 if len(p)>240 or len(a)<2 or a[0] not in ('knowledge','preferences','procedures','failures') or not p.endswith('.md') or any(x.startswith('.') for x in a): raise ValueError('knowledge path')
 return p
def knowledgecheckdirs(p,budget=None):
 if budget is None: budget=[8192]
 d=directory(p)
 try:
  for n in os.listdir(d):
   budget[0]-=1
   if budget[0]<0: raise ValueError('knowledge directory quota')
   s=os.stat(n,dir_fd=d,follow_symlinks=False)
   if stat.S_ISDIR(s.st_mode):
    if n.endswith('.md'): raise ValueError('Markdown directory substitution')
    knowledgecheckdirs(p+'/'+n,budget)
 finally: os.close(d)
def knowledgefiles():
 try: knowledgecheckdirs('.knowledge');rows=inventory('.knowledge')
 except FileNotFoundError: return {}
 files={};total=0
 for p,s in rows:
  p=p[len('.knowledge/'):]
  if p.startswith('.publications/'): continue
  knowledgepath(p);b=read('.knowledge/'+p,1048576);b.decode('utf-8');total+=len(b)
  if len(files)>=64 or total>1048576: raise ValueError('knowledge snapshot quota')
  files[p]=b
 return files
def knowledgeoperation():
 files=knowledgefiles()
 if op=='knowledge-snapshot':
  return [{'path':p,'digest':hashlib.sha256(b).hexdigest(),'summary':b.decode('utf-8')} for p,b in sorted(files.items())]
 r=q['record'];props=r['decision']['proposals'];pub=r['id']
 if len(parts(pub))!=1 or pub.startswith('.'): raise ValueError('knowledge publication')
 identity={'binding':q['binding'],'id':pub,'source':r['input']['source'],'decision_digest':r['decision_digest'],'proposals':props}
 expected=[{'path':p['path'],'digest':hashlib.sha256(p['content'].encode()).hexdigest()} for p in props]
 for p in props: knowledgepath(p['path'])
 try: prior=json.loads(read('.knowledge/.publications/'+pub,1048576))
 except FileNotFoundError: prior=None
 if prior is not None:
  if prior!=identity: raise ValueError('edited knowledge retry')
  for p in props:
   if files.get(p['path'])!=p['content'].encode(): raise ValueError('knowledge readback mismatch')
  ready()
  return expected
 if op=='knowledge-verify': raise ValueError('knowledge receipt missing')
 for p in props:
  old=files.get(p['path']);digest=hashlib.sha256(old).hexdigest() if old is not None else ''
  if digest!=p['expected_digest']: raise ValueError('knowledge CAS')
  files[p['path']]=p['content'].encode()
 if len(files)>64 or sum(len(b) for b in files.values())>1048576: raise ValueError('knowledge quota')
 stage='.knowledge-stage-'+uuid.uuid4().hex
 oldrows=[]
 try: oldrows=inventory('.knowledge')
 except FileNotFoundError: pass
 receipts={p[len('.knowledge/'):]:read(p,1048576) for p,_ in oldrows if p.startswith('.knowledge/.publications/')}
 receipts['.publications/'+pub]=json.dumps(identity).encode()
 staged=dict(files);staged.update(receipts)
 quota(sum(len(b) for b in staged.values()),len(staged),paths=[stage+'/'+p for p in staged])
 os.mkdir(stage,0o700,dir_fd=root)
 try:
  for p,b in staged.items(): write(stage+'/'+p,b)
  for p,b in staged.items():
   if read(stage+'/'+p,1048576)!=b: raise ValueError('knowledge staging readback')
  ready()
  try: os.stat('.knowledge',dir_fd=root,follow_symlinks=False);exists=True
  except FileNotFoundError: exists=False
  if exists:
   import ctypes
   libc=ctypes.CDLL(None,use_errno=True)
   if libc.renameat2(root,stage.encode(),root,b'.knowledge',2)!=0: raise OSError(ctypes.get_errno(),'knowledge exchange')
  else: os.rename(stage,'.knowledge',src_dir_fd=root,dst_dir_fd=root)
  os.fsync(root)
  for p in props:
   if read('.knowledge/'+p['path'],1048576)!=p['content'].encode(): raise ValueError('knowledge committed readback')
 finally:
  import shutil
  shutil.rmtree('/proc/self/fd/'+str(root)+'/'+stage,ignore_errors=True)
 return expected
`
