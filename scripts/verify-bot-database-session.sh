#!/usr/bin/env bash
# Strict acceptance/evidence collector. Never restarts, migrates or runs a provider.
# Private evidence may contain conversation data; do not publish the directory.
# Usage (E must be outside repo; all files immutable):
#   bash scripts/verify-bot-database-session.sh fixture --evidence-dir "$E"
#   ... capture-process --evidence-dir "$E" --name race --kind go-test \
#       --requires TestExactName,TestExactName/personal -- go test ./pkg/... -race -count=1 -json -run '^TestExactName$'
#   ... capture-process --evidence-dir "$E" --name frontend_tests --kind frontend-test \
#       --requires 'exact Vitest fullName' -- npm --silent --prefix web test -- --reporter=json
#   ... capture-process --evidence-dir "$E" --name go_build --kind build -- make build
#   ... capture-activity --evidence-dir "$E" --label before|after|reload \
#       --confirm personal:4d6e9fea-4bbf-4465-a3cc-76c8b4b6ad27 [--curl-config /private/credentials.conf]
#   ... check --evidence-dir "$E" --confirm personal:4d6e9fea-4bbf-4465-a3cc-76c8b4b6ad27
# live checks the immutable baseline, then records fresh GETs under invocations/UUID.
# Fresh profiles must be collected independently into --profile-dir/REQUEST_ID.json;
# use check --invocation-dir PATH to recheck a capture after profile collection.
# Report-only documentation hashes are recorded separately from tested source code.
# Parent-owned manifest.json schema 1: url,scope,bot; processes map containing
# fixture,fixture_cli,race,frontend_tests,frontend_typecheck,frontend_build,go_build,
# lint,terminal,browser, each {report:'NAME.process.json',kind,required_tests:[exact names]}.
# Remaining manifest fields are filenames: backup,inventory,migration,dry_run,
# workspace_before,workspace_after,identities_before,identities_after,reload,journey.
# backup: {verified:true,compatible_format:3,bot,scope,archive,sha256,verified_ns}.
# inventory: {bot,active_runs:[],captured_ns}. migration: CLI report plus
# started_ns,finished_ns; bot,scope,complete:true,busy:false,dry_run:false.
# dry_run: {bot,dry_run:true,busy:false,...}. workspace files: nonempty identical
# path->SHA256 maps of actual standing workspace bytes, excluding retained runtime files.
# identities: identical nonempty logical transcript/approval/run IDs and cursors,
# objective,answers,revision/config; derive via existing repository/HTTP, not filesystem repair.
# reload: {bot,ready:true,ready_ns,source:{revision,code_sha256,reports_sha256,report_paths}}
# from approved restart; old tree_sha256-only evidence must be recaptured, not relabeled.
# Each after/reload-N.profile.json: {request_id,middleware_total_ns,stages:{
# repository_get_bot:ns,sql_activity_query:ns,handler_total:ns,...}} from request-correlated probes.
# journey: {provider:'sap-ai-core',scope:'personal',bot:DISPOSABLE_ID,session,events:FILE,
# answer:['U.S. markets','major indexes'],runtime_io_covered:true,runtime_opens:0,
# runtime_puts:0,timings_ns:{dispatch,home_preparation,factory,model_start,model_end,first_committed_ack}}.
# events JSON array: exactly one each question_rendered,answer_accepted,ack_sse,
# ack_rendered,tool_observed,final_rendered,reload_rendered, with kind,bot,session,
# timestamp_ns (same wall-clock basis); accepted has http_status:202; ack_sse has
# genuine_model_response:true,event_id matching ack_rendered; final has proper_final:true,
# event_id; reload has final_event_id,ack_event_id matching first render.
# Parent must derive those observations from real browser/SSE, never model mock or synthetic echo.
# Successful fixture is NOT final live acceptance. Missing/skipped evidence fails closed.
set -euo pipefail
umask 077
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
export ASTONISH_VERIFY_ROOT="$ROOT"
exec python3 - "$@" <<'PY'
import argparse, datetime, hashlib, json, os, pathlib, re, subprocess, sys, time, uuid
ROOT=pathlib.Path(os.environ['ASTONISH_VERIFY_ROOT'])
BOT='4d6e9fea-4bbf-4465-a3cc-76c8b4b6ad27'
URL='http://127.0.0.1:9393'
def fail(message): raise ValueError(message)
def need(condition,message):
    if not condition: fail(message)
def load(p):
    with open(p) as f: return json.load(f)
def save(p,obj):
    p=pathlib.Path(p); p.parent.mkdir(parents=True,exist_ok=True)
    with open(p,'x') as f: json.dump(obj,f,indent=2); f.write('\n')
def digest(p): return hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest()
def git(*args): return subprocess.check_output(['git',*args],cwd=ROOT,stderr=subprocess.DEVNULL)
# Explicit report-only allowlist; no blanket exclusion of Markdown or docs/code.
REPORT_PATHS={'activity-endpoint-profile.md','bot-database-session-evaluation.md','docs/architecture/bot-release-checklist.md'}
def source():
    # Fingerprint dirty/untracked code and report documents independently.
    code=hashlib.sha256(); reports=hashlib.sha256()
    paths=set(git('ls-files','-z').split(b'\0')) | set(git('ls-files','--others','--exclude-standard','-z').split(b'\0'))
    for raw in sorted(paths):
        if not raw: continue
        name=os.fsdecode(raw); p=ROOT/name
        h=reports if name in REPORT_PATHS else code
        h.update(raw+b'\0')
        if p.is_symlink(): h.update(os.readlink(p).encode())
        elif p.is_file(): h.update(p.read_bytes())
    return {'revision':git('rev-parse','HEAD').decode().strip(),'code_sha256':code.hexdigest(),
            'reports_sha256':reports.hexdigest(),'report_paths':sorted(REPORT_PATHS)}
def same_code(record,current=None):
    current=source() if current is None else current
    # Old combined tree hashes cannot prove which code was tested: recapture explicitly.
    return bool(record.get('code_sha256')) and all(record.get(k)==current.get(k) for k in ('revision','code_sha256','report_paths'))
def identity(a):
    need(a.url==URL and a.scope=='personal' and a.bot==BOT,'Only the exact reported URL/personal Bot is accepted')
    need(a.confirm==a.scope+':'+a.bot,'Explicit scope:Bot confirmation required')
def tests(path,required):
    events=[json.loads(line) for line in pathlib.Path(path).read_text().splitlines() if line.strip()]
    need(events and required,'Missing test events or exact required test names')
    need(not any(e.get('Action') in ('skip','fail') for e in events),'Test failure or skip is not acceptance')
    for name in required:
        need(any(e.get('Test')==name and e.get('Action')=='run' for e in events),'Missing exact test run: '+name)
        need(any(e.get('Test')==name and e.get('Action')=='pass' for e in events),'Missing exact test pass: '+name)
    need(any(not e.get('Test') and e.get('Action')=='pass' for e in events),'Missing package pass')
def frontend_tests(path,required):
    report=load(path)
    need(required and report.get('success') is True and report.get('numTotalTests',0)>0,'Frontend JSON tests missing or failed')
    need(report.get('numPendingTests',0)==0 and report.get('numFailedTests',0)==0,'Frontend failed/skipped tests are not acceptance')
    assertions=[a for suite in report['testResults'] for a in suite['assertionResults']]
    need(assertions and all(a['status']=='passed' for a in assertions),'Frontend skipped/missing assertions')
    for name in required: need(any(a.get('fullName')==name for a in assertions),'Missing exact frontend test: '+name)
def capture_process(directory,name,kind,command,required):
    need(re.fullmatch(r'[A-Za-z0-9_-]+',name) is not None,'Unsafe process name')
    need(command and pathlib.Path(command[0]).name not in ('sh','bash','zsh','fish','tee'), 'Run the actual process, never a shell/tee pipeline')
    if kind=='go-test': need(pathlib.Path(command[0]).name=='go' and 'test' in command and '-json' in command,'Go test must directly emit JSON')
    need(not any(x in ('|','||','&&',';') for x in command),'Pipelines are not process evidence')
    directory=pathlib.Path(directory); directory.mkdir(parents=True,exist_ok=True)
    log=directory/(name+'.stdout'); err=directory/(name+'.stderr'); stamp=source(); start=time.time_ns()
    with open(log,'xb') as out, open(err,'xb') as errors:
        result=subprocess.run(command,cwd=ROOT,stdout=out,stderr=errors)
    report={'schema':1,'kind':kind,'name':name,'source':stamp,'started_ns':start,'finished_ns':time.time_ns(),'exit_code':result.returncode,'stdout':log.name,'stderr':err.name,'stdout_sha256':digest(log),'required_tests':required}
    save(directory/(name+'.process.json'),report)
    need(result.returncode==0,'Process failed; private evidence retained: '+name)
    if kind=='go-test': tests(log,required)
    if kind=='frontend-test': frontend_tests(log,required)
    print('Captured successful process: '+name)
def activity(a,label):
    identity(a); need(a.samples==3 and a.activity_max_seconds<=1 and a.activity_max_seconds>0,'Exactly three samples and <=1 second required')
    directory=pathlib.Path(a.evidence_dir); directory.mkdir(parents=True,exist_ok=True)
    endpoint=a.url+'/api/studio/bots/'+a.bot+'/activity?scope='+a.scope+'&after=0&limit=100'
    for index in range(1,4):
        prefix=directory/(label+'-'+str(index)); body=prefix.with_suffix('.body.json'); headers=prefix.with_suffix('.headers'); meta=prefix.with_suffix('.capture.json')
        need(not any(p.exists() for p in (body,headers,meta)),'Refusing to overwrite activity evidence')
        cmd=['curl','--silent','--show-error','--max-time','30','--request','GET','--dump-header',str(headers),'--output',str(body),'--write-out','%{http_code} %{time_total}']
        if a.curl_config:
            config=pathlib.Path(a.curl_config)
            need(config.is_file() and config.stat().st_mode & 0o077==0,'curl credential config must be private (0600)')
            # Existing client credentials may be supplied, but config cannot change destination/method/output.
            need(all(not re.match(r'\s*(?:--)?(?:url|request|output|dump-header|write-out|location|next|data\S*|upload-file|config)\b',line) for line in config.read_text().splitlines()),'Credential config must not override request behavior')
            cmd+=['--config',str(config)]
        cmd+=[endpoint]; started=time.time_ns(); tick=time.perf_counter_ns()
        result=subprocess.run(cmd,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,cwd=ROOT)
        total=time.perf_counter_ns()-tick
        need(result.returncode==0,'Activity transport failed (credentials are never logged)')
        status,curl_total=result.stdout.decode().split()
        save(meta,{'schema':1,'url':endpoint,'scope':a.scope,'bot':a.bot,'label':label,'sample':index,'http_status':int(status),'started_ns':started,'client_total_ns':total,'curl_total_seconds':float(curl_total),'body_sha256':digest(body),'source':source()})
        need(status=='200','Activity must return HTTP 200'); load(body)
    print('Captured three '+label+' activity responses (not yet acceptance)')
def process_check(directory,entry):
    p=directory/entry['report']; report=load(p)
    need(report['schema']==1 and report['exit_code']==0,'Missing successful process exit evidence')
    need(same_code(report['source']),'Process source does not match current revision/dirty tree')
    log=directory/report['stdout']; need(digest(log)==report['stdout_sha256'],'Changed process log')
    need(report['kind']==entry['kind'],'Process kind mismatch')
    if entry['kind'] in ('go-test','frontend-test'):
        need(set(entry['required_tests'])==set(report['required_tests']),'Exact test requirements differ')
        (tests if entry['kind']=='go-test' else frontend_tests)(log,entry['required_tests'])
def check(a,announce=True):
    identity(a); directory=pathlib.Path(a.evidence_dir); m=load(directory/'manifest.json')
    need(m['schema']==1 and m['url']==URL and m['scope']=='personal' and m['bot']==BOT,'Manifest target mismatch')
    # Required process roles cannot be omitted, downgraded to skips or replaced by tee's status.
    roles={'fixture','fixture_cli','race','frontend_tests','frontend_typecheck','frontend_build','go_build','lint','terminal','browser'}
    need(roles<=set(m['processes']),'Missing required process/build role')
    for role,entry in m['processes'].items(): process_check(directory,entry)
    need(all(m['processes'][r]['kind']=='go-test' for r in ('fixture','fixture_cli','race','terminal','browser')),'Exact Go test evidence required for fixture/race/CLI/terminal/browser')
    need(m['processes']['frontend_tests']['kind']=='frontend-test','Structured frontend no-skip evidence required')
    critical={'TestBotDatabaseRuntimeFormatMatrix','TestBotDatabaseSessionChatParity','TestBotDatabaseBackupRestore','TestBotDatabaseArtifactClosure'}
    for role in ('fixture','race'): need(critical<=set(m['processes'][role]['required_tests']),'Missing critical fixture/race exact test names')
    cli={'TestBotRuntimeStorageMigrationCLISubprocess'} | {f'TestBotRuntimeStorageMigrationCLISubprocess/{s}/{mode}' for s in ('personal','team') for mode in ('convert','dry-run','busy','revoked','stale','wrong-confirm')}
    need(cli<=set(m['processes']['fixture_cli']['required_tests']),'Missing personal/team CLI matrix')
    b=load(directory/m['backup']); need(b['verified'] is True and b['compatible_format']==3 and b['bot']==BOT and b['scope']=='personal','Compatible verified backup required')
    need(digest(directory/b['archive'])==b['sha256'],'Backup archive digest mismatch')
    inventory=load(directory/m['inventory']); need(inventory['bot']==BOT and inventory['active_runs']==[],'Pre-migration quiescent run inventory required')
    migration=load(directory/m['migration']); need(migration['bot']==BOT and migration['scope']=='personal' and migration['complete'] is True and migration['busy'] is False and migration['dry_run'] is False,'Successful exact Bot migration report required')
    dry=load(directory/m['dry_run']); need(dry['bot']==BOT and dry['dry_run'] is True and dry['busy'] is False,'Dry-run evidence required')
    need(b['verified_ns']<=migration['started_ns'] and inventory['captured_ns']<=migration['started_ns'],'Backup/inventory must precede migration')
    for pair in ('workspace','identities'):
        before=load(directory/m[pair+'_before']); after=load(directory/m[pair+'_after'])
        need(before and before==after,'Unchanged nonempty '+pair+' before/after evidence required')
    reload=load(directory/m['reload']); need(reload['bot']==BOT and reload['ready'] is True and same_code(reload['source']),'Daemon reload/readiness/source evidence required')
    logical=None; request_ids=set()
    batches=[(directory,label) for label in ('before','after','reload')]
    if getattr(a,'invocation_dir',None):
        invocation=pathlib.Path(a.invocation_dir).resolve(); record=load(invocation/'invocation.json')
        need(record['baseline']==str(directory.resolve()) and record['manifest_sha256']==digest(directory/'manifest.json'),'Invocation baseline mismatch')
        need(same_code(record['source']),'Invocation source mismatch')
        batches.append((invocation,'after'))
    for sample_dir,label in batches:
        for index in range(1,4):
            prefix=sample_dir/(label+'-'+str(index)); meta=load(prefix.with_suffix('.capture.json')); body=prefix.with_suffix('.body.json'); value=load(body)
            need(meta['scope']=='personal' and meta['bot']==BOT and meta['label']==label and meta['sample']==index,'Wrong activity scope/identity/sample')
            need(meta['http_status']==200 and meta['url']==URL+'/api/studio/bots/'+BOT+'/activity?scope=personal&after=0&limit=100','Wrong activity target/status')
            need(meta['body_sha256']==digest(body),'Activity body digest changed')
            if logical is None: logical=value
            need(value==logical,'Logical activity/order changed')
            if label=='before': continue
            need(same_code(meta['source']),'Activity measured against wrong source')
            need(0<meta['client_total_ns']<=1_000_000_000 and 0<meta['curl_total_seconds']<=1,'Full activity response exceeds one second')
            need(meta['started_ns']>=migration['finished_ns'],'Activity predates migration')
            if label=='reload': need(meta['started_ns']>=reload['ready_ns'],'Fresh reload samples predate readiness')
            if sample_dir!=directory:
                need(meta['started_ns']>=max(record['started_ns'],reload['ready_ns']),'Invocation samples are not fresh after reload')
            h={}
            for line in prefix.with_suffix('.headers').read_text().splitlines():
                if ':' in line:
                    key,val=line.split(':',1); h[key.lower()]=val.strip()
            need(h.get('x-astonish-bot-runtime-io-covered')=='true','Zero counters without profiler coverage are not proof')
            for key in ('opens','puts','failures'): need(h.get('x-astonish-bot-runtime-'+key)=='0','Runtime file I/O/failure observed or counter missing')
            rid=h.get('x-astonish-bot-activity-request-id'); need(rid and rid not in request_ids,'Missing/reused profile request ID'); request_ids.add(rid)
            need(int(h.get('x-astonish-bot-activity-handler-total-ns','0'))>0,'Missing handler total trailer')
            profile=load(sample_dir/(label+'-'+str(index)+'.profile.json'))
            need(profile['request_id']==rid and profile['middleware_total_ns']>0,'Request-correlated middleware timing required')
            stages=profile['stages']; need(all(s in stages for s in ('repository_get_bot','sql_activity_query','handler_total')) and all(isinstance(v,int) and v>=0 for v in stages.values()),'Missing per-stage nanosecond timings')
    journey=load(directory/m['journey']); events=load(directory/journey['events'])
    need(journey['provider']=='sap-ai-core' and journey['scope']=='personal' and journey['bot']!=BOT,'Disposable personal SAP AI Core browser journey required')
    need(journey['answer']==['U.S. markets','major indexes'],'Required real browser clarification choice missing')
    need(journey['runtime_io_covered'] is True and journey['runtime_opens']==0 and journey['runtime_puts']==0,'Browser runtime file I/O must be covered and zero')
    names=['question_rendered','answer_accepted','ack_sse','ack_rendered','tool_observed','final_rendered','reload_rendered']
    selected=[]
    for name in names:
        found=[e for e in events if e['kind']==name]; need(len(found)==1,'Expected exactly one browser event: '+name); selected.append(found[0])
    need(all(e['bot']==journey['bot'] and e['session']==journey['session'] and isinstance(e['timestamp_ns'],int) for e in selected),'Browser event identity/timestamp mismatch')
    need(all(selected[i]['timestamp_ns']<=selected[i+1]['timestamp_ns'] for i in range(len(selected)-1)),'Browser journey timestamps out of order')
    need(selected[1]['http_status']==202 and selected[2]['event_id']==selected[3]['event_id'] and selected[2]['genuine_model_response'] is True,'Accepted answer and genuine correlated rendered acknowledgement required')
    need(selected[5]['event_id']==selected[6]['final_event_id'] and selected[6]['ack_event_id']==selected[3]['event_id'],'Reload must retain same final/ack identities')
    need(selected[5]['proper_final'] is True,'Proper final response required')
    need(all(isinstance(journey['timings_ns'][k],int) and journey['timings_ns'][k]>=0 for k in ('dispatch','home_preparation','factory','model_start','model_end','first_committed_ack')),'Missing separately recorded execution timing spans')
    delta=selected[3]['timestamp_ns']-selected[1]['timestamp_ns']
    if announce: print('PASS: unchanged exact activity; after+fresh-reload three calls <=1s; covered zero opens/puts. Accepted-to-rendered-ack seconds: %.6f (no overlapping spans subtracted)'%(delta/1e9))
def collect_profiles(invocation,profile_dir):
    # Copy independently captured, request-correlated measurements once; never synthesize timings.
    if not profile_dir: return
    invocation=pathlib.Path(invocation)
    for index in range(1,4):
        prefix=invocation/('after-'+str(index)); target=prefix.with_suffix('.profile.json')
        if target.exists(): continue
        headers={}
        for line in prefix.with_suffix('.headers').read_text().splitlines():
            if ':' in line:
                key,value=line.split(':',1); headers[key.lower()]=value.strip()
        rid=headers.get('x-astonish-bot-activity-request-id','')
        need(re.fullmatch(r'[A-Za-z0-9_-]+',rid) is not None,'Missing/unsafe profile request ID')
        profile=load(pathlib.Path(profile_dir)/(rid+'.json'))
        need(profile['request_id']==rid,'Profile request ID mismatch')
        save(target,profile)
def new_invocation(directory):
    directory=pathlib.Path(directory).resolve()
    invocation=directory/'invocations'/str(uuid.uuid4()); invocation.mkdir(parents=True,exist_ok=False)
    save(invocation/'invocation.json',{'baseline':str(directory),'manifest_sha256':digest(directory/'manifest.json'),'source':source(),'started_ns':time.time_ns()})
    return invocation
def selftest():
    # Synthetic checker-unit inputs live only in a temporary directory, never in live evidence.
    import copy, tempfile, unittest
    from unittest.mock import patch
    with tempfile.TemporaryDirectory(prefix='astonish-harness-selftest-') as temp:
        d=pathlib.Path(temp); stamp={'revision':'unit-only','code_sha256':'unit-only','reports_sha256':'one','report_paths':sorted(REPORT_PATHS)}
        a=argparse.Namespace(url=URL,scope='personal',bot=BOT,confirm='personal:'+BOT,evidence_dir=str(d),invocation_dir=None,profile_dir=None)
        critical=['TestBotDatabaseRuntimeFormatMatrix','TestBotDatabaseSessionChatParity','TestBotDatabaseBackupRestore','TestBotDatabaseArtifactClosure']
        cli=['TestBotRuntimeStorageMigrationCLISubprocess']+[f'TestBotRuntimeStorageMigrationCLISubprocess/{s}/{m}' for s in ('personal','team') for m in ('convert','dry-run','busy','revoked','stale','wrong-confirm')]
        m={'schema':1,'url':URL,'scope':'personal','bot':BOT,'processes':{}}
        for role in ('fixture','fixture_cli','race','frontend_tests','frontend_typecheck','frontend_build','go_build','lint','terminal','browser'):
            kind='go-test' if role in ('fixture','fixture_cli','race','terminal','browser') else ('frontend-test' if role=='frontend_tests' else 'build')
            required=critical if role in ('fixture','race') else (cli if role=='fixture_cli' else ['UnitOnly'])
            log=d/(role+'.stdout')
            if kind=='go-test':
                events=[{'Action':action,'Test':name} for name in required for action in ('run','pass')]+[{'Action':'pass'}]
                log.write_text('\n'.join(json.dumps(e) for e in events))
            elif kind=='frontend-test':
                save(log,{'success':True,'numTotalTests':1,'testResults':[{'assertionResults':[{'fullName':'UnitOnly','status':'passed'}]}]})
            else: log.write_text('unit-only')
            save(d/(role+'.process.json'),{'schema':1,'exit_code':0,'kind':kind,'source':stamp,'stdout':log.name,'stdout_sha256':digest(log),'required_tests':required})
            m['processes'][role]={'report':role+'.process.json','kind':kind,'required_tests':required}
        (d/'unit-only.archive').write_bytes(b'unit checker data, NOT a backup')
        records={
            'backup':{'verified':True,'compatible_format':3,'bot':BOT,'scope':'personal','archive':'unit-only.archive','sha256':digest(d/'unit-only.archive'),'verified_ns':1},
            'inventory':{'bot':BOT,'active_runs':[],'captured_ns':1},
            'migration':{'bot':BOT,'scope':'personal','complete':True,'busy':False,'dry_run':False,'started_ns':2,'finished_ns':3},
            'dry_run':{'bot':BOT,'dry_run':True,'busy':False},
            'reload':{'bot':BOT,'ready':True,'ready_ns':4,'source':stamp},
            'workspace_before':{'unit-only':'unchanged'},'workspace_after':{'unit-only':'unchanged'},
            'identities_before':{'unit-only':'unchanged'},'identities_after':{'unit-only':'unchanged'},
            'journey':{'provider':'sap-ai-core','scope':'personal','bot':'unit-only','session':'unit-only','events':'events.json','answer':['U.S. markets','major indexes'],'runtime_io_covered':True,'runtime_opens':0,'runtime_puts':0,'timings_ns':dict.fromkeys(('dispatch','home_preparation','factory','model_start','model_end','first_committed_ack'),1)}}
        for name,obj in records.items(): m[name]=name+'.json'; save(d/m[name],obj)
        events=[{'kind':kind,'bot':'unit-only','session':'unit-only','timestamp_ns':i} for i,kind in enumerate(('question_rendered','answer_accepted','ack_sse','ack_rendered','tool_observed','final_rendered','reload_rendered'),10)]
        events[1]['http_status']=202; events[2].update(event_id='ack',genuine_model_response=True); events[3]['event_id']='ack'
        events[5].update(event_id='final',proper_final=True); events[6].update(final_event_id='final',ack_event_id='ack'); save(d/'events.json',events)
        save(d/'manifest.json',m)
        def samples(target,label):
            for i in range(1,4):
                prefix=target/(label+'-'+str(i)); save(prefix.with_suffix('.body.json'),{'unit-only':['ordered','activity']})
                save(prefix.with_suffix('.capture.json'),{'http_status':200,'url':URL+'/api/studio/bots/'+BOT+'/activity?scope=personal&after=0&limit=100','scope':'personal','bot':BOT,'label':label,'sample':i,'body_sha256':digest(prefix.with_suffix('.body.json')),'source':stamp,'client_total_ns':1,'curl_total_seconds':0.001,'started_ns':time.time_ns()})
                rid=target.name+'-'+label+'-'+str(i)
                prefix.with_suffix('.headers').write_text('X-Astonish-Bot-Runtime-IO-Covered: true\n'+''.join('X-Astonish-Bot-Runtime-'+k+': 0\n' for k in ('Opens','Puts','Failures'))+'X-Astonish-Bot-Activity-Request-ID: '+rid+'\nX-Astonish-Bot-Activity-Handler-Total-Ns: 1\n')
                save(prefix.with_suffix('.profile.json'),{'request_id':rid,'middleware_total_ns':1,'stages':dict.fromkeys(('repository_get_bot','sql_activity_query','handler_total'),1)})
        for label in ('before','after','reload'): samples(d,label)
        original_load=load
        class CheckerTests(unittest.TestCase):
            def verify(self,mutate=None,args=None):
                def altered(path):
                    obj=original_load(path)
                    if mutate: mutate(pathlib.Path(path).name,obj)
                    return obj
                with patch.dict(globals(),source=lambda:stamp,load=altered): check(args or a,announce=False)
            def test_accepts_202(self): self.verify()
            def test_rejects_200_continuation(self):
                def mutate(name,obj):
                    if name=='events.json': obj[1]['http_status']=200
                with self.assertRaises(ValueError): self.verify(mutate)
            def test_rejects_wrong_scope(self):
                bad=copy.copy(a); bad.scope='team'
                with self.assertRaises(ValueError): self.verify(args=bad)
            def test_rejects_activity_status_and_scope(self):
                for key,value in (('http_status',202),('scope','team')):
                    def mutate(name,obj):
                        if name=='after-1.capture.json': obj[key]=value
                    with self.assertRaises(ValueError): self.verify(mutate)
            def test_rejects_missing_role(self):
                def mutate(name,obj):
                    if name=='manifest.json': del obj['processes']['browser']
                with self.assertRaises(ValueError): self.verify(mutate)
            def test_rejects_failed_exit(self):
                def mutate(name,obj):
                    if name=='go_build.process.json': obj['exit_code']=1
                with self.assertRaises(ValueError): self.verify(mutate)
            def test_rejects_missing_skip_and_fail_tests(self):
                for action in ('skip','fail','absent'):
                    log=d/('negative-'+action+'.jsonl'); log.write_text(json.dumps({'Action':action,'Test':'Exact'})+'\n'+json.dumps({'Action':'pass'}))
                    with self.assertRaises(ValueError): tests(log,['Exact'])
            def test_rejects_frontend_skip(self):
                log=d/'negative-frontend.json'; save(log,{'success':True,'numTotalTests':1,'numPendingTests':1})
                with self.assertRaises(ValueError): frontend_tests(log,['Exact'])
            def test_rejects_uncovered_io_slow_or_changed_activity(self):
                for key,value in (('client_total_ns',1_000_000_001),('body_sha256','changed')):
                    def mutate(name,obj):
                        if name=='after-1.capture.json': obj[key]=value
                    with self.assertRaises(ValueError): self.verify(mutate)
                path=d/'after-1.headers'; text=path.read_text()
                for bad in (text.replace('true','false'),text.replace('Opens: 0','Opens: 1'),text.replace('Puts: 0','Puts: 1')):
                    try:
                        path.write_text(bad)
                        with self.assertRaises(ValueError): self.verify()
                    finally: path.write_text(text)
            def test_rejects_missing_profile(self):
                path=d/'after-1.profile.json'; held=path.with_suffix('.held'); path.rename(held)
                try:
                    with self.assertRaises(FileNotFoundError): self.verify()
                finally: held.rename(path)
            def test_rejects_backup_and_journey_gaps(self):
                for filename,key,value in (('backup.json','verified',False),('journey.json','provider','mock'),('journey.json','runtime_io_covered',False),('workspace_after.json','unit-only','changed')):
                    def mutate(name,obj):
                        if name==filename: obj[key]=value
                    with self.assertRaises(ValueError): self.verify(mutate)
                for index,key,value in ((2,'genuine_model_response',False),(5,'proper_final',False),(6,'ack_event_id','wrong')):
                    def mutate(name,obj):
                        if name=='events.json': obj[index][key]=value
                    with self.assertRaises(ValueError): self.verify(mutate)
            def test_report_only_changes_not_code_changes(self):
                changed=dict(stamp,reports_sha256='two'); self.assertTrue(same_code(stamp,changed))
                self.assertFalse(same_code(stamp,dict(stamp,code_sha256='changed')))
                self.assertFalse(same_code({'revision':'unit-only','tree_sha256':'old'},stamp))
            def test_repeat_invocations_preserve_baseline(self):
                before={p.name:digest(p) for p in d.iterdir() if p.is_file()}
                with patch.dict(globals(),source=lambda:stamp):
                    one=new_invocation(d); two=new_invocation(d)
                self.assertNotEqual(one,two)
                for target in (one,two):
                    samples(target,'after')
                    profiles=target/'collector'; profiles.mkdir()
                    for index in range(1,4):
                        path=target/('after-'+str(index)+'.profile.json'); profile=load(path)
                        path.rename(profiles/(profile['request_id']+'.json'))
                    collect_profiles(target,profiles)
                    frozen={p.name:digest(p) for p in target.glob('*.profile.json')}
                    collect_profiles(target,profiles)
                    self.assertEqual(frozen,{p.name:digest(p) for p in target.glob('*.profile.json')})
                    args=copy.copy(a); args.invocation_dir=str(target); self.verify(args=args)
                self.assertEqual(before,{p.name:digest(p) for p in d.iterdir() if p.is_file()})
                with self.assertRaises(FileExistsError): save(one/'invocation.json',{})
        result=unittest.TextTestRunner(verbosity=2).run(unittest.defaultTestLoader.loadTestsFromTestCase(CheckerTests))
        need(result.wasSuccessful(),'Offline checker self-tests failed')
        print('PASS: offline checker self-tests only; NOT live/provider evidence')
def main():
    parser=argparse.ArgumentParser(description=__doc__,epilog='capture-process executes argv directly (no shell/pipeline). Evidence is immutable; use a fresh directory outside the repo. live is read-only GET + strict check, never migration/restart/provider execution.')
    parser.add_argument('mode',choices=['selftest','fixture','capture-process','capture-activity','check','live'])
    parser.add_argument('--evidence-dir',default=os.environ.get('ASTONISH_BOT_DATABASE_EVIDENCE_DIR','/tmp/astonish-bot-database-session-evidence')); parser.add_argument('--url',default=URL); parser.add_argument('--scope',default='personal'); parser.add_argument('--bot',default=BOT); parser.add_argument('--confirm',default='')
    parser.add_argument('--label',choices=['before','after','reload'],default='after'); parser.add_argument('--samples',type=int,default=3); parser.add_argument('--activity-max-seconds',type=float,default=1); parser.add_argument('--require-runtime-file-io-zero',action='store_true'); parser.add_argument('--browser'); parser.add_argument('--curl-config')
    parser.add_argument('--invocation-dir'); parser.add_argument('--profile-dir',default=os.environ.get('ASTONISH_BOT_DATABASE_PROFILE_DIR'))
    parser.add_argument('--name'); parser.add_argument('--kind',choices=['go-test','frontend-test','build']); parser.add_argument('--requires',default='')
    argv=sys.argv[1:]; command=[]
    if '--' in argv:
        cut=argv.index('--'); command=argv[cut+1:]; argv=argv[:cut]
    a=parser.parse_args(argv); a.command=command; directory=pathlib.Path(a.evidence_dir).resolve()
    need(not directory.is_relative_to(ROOT),'Evidence must be outside source tree so source fingerprints remain stable')
    if a.mode=='selftest': selftest()
    elif a.mode=='fixture':
        required=['TestBotDatabaseRuntimeFormatMatrix','TestBotDatabaseSessionChatParity','TestBotDatabaseBackupRestore','TestBotDatabaseArtifactClosure']
        capture_process(directory,'fixture','go-test',['go','test','./pkg/store/entstore','-count=1','-json','-run','^('+ '|'.join(required)+')$'],required)
        required=['TestBotRuntimeStorageMigrationCLISubprocess']+[f'TestBotRuntimeStorageMigrationCLISubprocess/{s}/{m}' for s in ('personal','team') for m in ('convert','dry-run','busy','revoked','stale','wrong-confirm')]
        capture_process(directory,'fixture-cli','go-test',['go','test','./cmd/astonish','-count=1','-json','-run','^TestBotRuntimeStorageMigrationCLISubprocess$'],required)
    elif a.mode=='capture-process':
        command=a.command[1:] if a.command[:1]==['--'] else a.command
        need(a.name and a.kind,'--name and --kind required'); capture_process(directory,a.name,a.kind,command,[x for x in a.requires.split(',') if x])
    elif a.mode=='capture-activity': activity(a,a.label)
    elif a.mode=='live':
        need(a.require_runtime_file_io_zero,'Explicit zero runtime I/O gate required'); identity(a)
        # All risky work is parent-owned; missing artifacts refuse acceptance, not auto-run.
        need((directory/'manifest.json').is_file(),'Supply completed manifest before live verification')
        if a.browser: need(pathlib.Path(a.browser).is_file() and os.access(a.browser,os.X_OK),'Explicit browser executable invalid')
        need(not a.invocation_dir,'live always creates a fresh invocation; use check to recheck one')
        check(a,announce=False)  # Refuse missing instrumentation/source/baseline before GETs.
        invocation=new_invocation(directory)
        print('Immutable live invocation: '+str(invocation),flush=True)
        fresh=argparse.Namespace(**vars(a)); fresh.evidence_dir=str(invocation)
        activity(fresh,'after')
        collect_profiles(invocation,a.profile_dir)
        a.invocation_dir=str(invocation); check(a)
        save(invocation/'result.json',{'accepted':True,'checked_ns':time.time_ns(),'source':source(),'baseline_manifest_sha256':digest(directory/'manifest.json')})
    else: check(a)
try: main()
except (ValueError,KeyError,OSError,TypeError,json.JSONDecodeError,subprocess.CalledProcessError) as error:
    # Do not print payloads/credentials from parsing or transport exceptions.
    print('FAIL: '+(str(error) if isinstance(error,ValueError) and not isinstance(error,json.JSONDecodeError) else 'Missing, malformed or inconsistent required evidence ('+type(error).__name__+')'),file=sys.stderr); sys.exit(1)
PY
