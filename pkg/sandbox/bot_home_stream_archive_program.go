package sandbox

import "strings"

// BotHomeArchiveProgram is retained as the legacy byte-compatibility fixture.
// Production archive transport uses RunBotHomeArchive, not this interpreter.
// Reuse exactly the file helper's descriptor-relative walk, mount identity,
// single-link checks and streaming digest functions, not a tar extractor.
var BotHomeArchiveProgram = strings.Replace(strings.Split(BotHomeFilesProgram, "op=q['op'];")[0], "inp.readline(65537)", "inp.readline(8389633)", 1) + `
def safe(root,p):
 a=parts(p)
 if any(x in ('.workspace-lock','.archive-state') for x in a): raise ValueError('archive control path')
 if root=='home' and a[0] in ('.ssh','.aws','.kube','.docker','.astonish','proc','sys','dev','run'): raise ValueError('archive runtime path')
 return a
roots={'home':home,'private':private}
# Existing private writer lock; ordinary writers and attachment admission are
# fenced by the caller-held durable barrier for the entire helper lifetime.
lock=os.open('.workspace-lock',os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW|os.O_NONBLOCK,0o600,dir_fd=private)
check(lock,private,True);fcntl.flock(lock,fcntl.LOCK_EX)
def inventory():
 rows=[];total=0;privatebytes=0;privatefiles=0;privateentries=0
 def walk(root,d,prefix):
  nonlocal total,privatebytes,privatefiles,privateentries
  with os.scandir(d) as it:
   for item in it:
    if root=='private' and not prefix and item.name=='.workspace-lock': continue
    if root=='private' and not prefix and item.name=='.archive-state':
     state=fileopen(private,'.archive-state')
     try:
      if os.read(state,64)!=b'restoring\nverified\n': raise ValueError('incomplete archive target')
     finally: os.close(state)
     continue
    p=prefix+item.name;safe(root,p)
    fd=os.open(item.name,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK,dir_fd=d)
    try:
     s=check(fd,roots[root]);isdir=stat.S_ISDIR(s.st_mode)
     if not isdir: check(fd,roots[root],True)
     row={'root':root,'path':p,'mode':stat.S_IMODE(s.st_mode)&0o777,'size':0 if isdir else s.st_size}
     if isdir: row['directory']=True
     else:
      row['sha256']=digest(fd)
      if identity(s)!=identity(os.fstat(fd)): raise ValueError('changed file')
     rows.append(row);total+=row['size']
     if root=='private':
      privateentries+=1;privatebytes+=row['size'];privatefiles+=int(not isdir)
     if len(rows)>16384 or total>8589934592 or privateentries>8192 or privatefiles>4096 or privatebytes>536870912: raise ValueError('archive quota')
     if isdir: walk(root,fd,p+'/')
    finally: os.close(fd)
 walk('home',home,'');walk('private',private,'')
 rows.sort(key=lambda e:e['root']+'/'+e['path'])
 return {'version':1,'binding':q['binding'],'entries':rows}
def approved():
 if inp.readline(16)!=b'commit\n': raise ValueError('archive commit denied')
if q['op']=='export':
 manifest=inventory();emit(manifest);approved()
 for e in manifest['entries']:
  if e.get('directory'): continue
  fd=fileopen(roots[e['root']],e['path'])
  try:
   before=check(fd,roots[e['root']],True);left=e['size'];h=hashlib.sha256()
   while left:
    b=os.read(fd,min(65536,left))
    if not b: raise ValueError('short archive read')
    sys.stdout.buffer.write(b);h.update(b);left-=len(b)
   if h.hexdigest()!=e['sha256'] or identity(before)!=identity(os.fstat(fd)): raise ValueError('changed archive read')
  finally: os.close(fd)
 sys.stdout.buffer.flush()
 if inventory()!=manifest: raise ValueError('archive inventory changed')
elif q['op']=='restore':
 # Fresh means physically empty, not merely an unregistered binding. A marker
 # survives interruption so even an empty archive cannot be adopted twice.
 if os.listdir(home) or set(os.listdir(private))-set(['.workspace-lock']): raise ValueError('restore target not fresh')
 state=os.open('.archive-state',os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600,dir_fd=private)
 os.write(state,b'restoring\n');os.fsync(state);os.fsync(private)
 emit({'ready':True})
 for e in q['manifest']['entries']:
  r=roots[e['root']];a=safe(e['root'],e['path']);d=directory(r,a[:-1])
  try:
   if e.get('directory'):
    os.mkdir(a[-1],0o700,dir_fd=d)
   else:
    fd=os.open(a[-1],os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600,dir_fd=d)
    try:
     left=e['size'];h=hashlib.sha256()
     while left:
      b=inp.read(min(65536,left))
      if not b: raise ValueError('short restore input')
      view=memoryview(b)
      while view: view=view[os.write(fd,view):]
      h.update(b);left-=len(b)
     if h.hexdigest()!=e['sha256']: raise ValueError('restore digest mismatch')
     os.fchmod(fd,e['mode']);os.fsync(fd)
    finally: os.close(fd)
    verify=fileopen(r,e['path'])
    try:
     if os.fstat(verify).st_size!=e['size'] or digest(verify)!=e['sha256']: raise ValueError('staged restore bytes mismatch')
    finally: os.close(verify)
   os.fsync(d)
  finally: os.close(d)
 emit({'commit':True});approved()
 # Preserve executable directories last, after child creation.
 for e in reversed(q['manifest']['entries']):
  if e.get('directory'):
   fd=directory(roots[e['root']],parts(e['path']))
   try: os.fchmod(fd,e['mode']);os.fsync(fd)
   finally: os.close(fd)
 os.write(state,b'verified\n');os.fsync(state);os.close(state)
 # Keep marker until success has been observed; exclude it only from subsequent
 # exports (a restored Home is valid backup input).
 emit({'restored':True})
else: raise ValueError('archive operation')
`
