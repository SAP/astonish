import copy
import hashlib
import json
import unittest
from pathlib import Path
from unittest.mock import patch
import local_bot_smoke_launcher as launcher
import quality_live_launcher_test as quality_tests
import skills_acceptance_driver as driver


class SkillsLauncherTests(unittest.TestCase):
    def setUp(self):
        fixture = quality_tests.QualityLauncherTests()
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        self.state = dict(fixture.state, workflow=launcher.SKILLS_CONTRACT, skills_nonce='new-skills-nonce')
        self.actual = dict(contract=launcher.SKILLS_CONTRACT, nonce=self.state['skills_nonce'],
                           status='verified', quality=fixture.actual, passed=list(launcher.SKILLS_REQUIRED), receipts={})
        for name in launcher.SKILLS_REQUIRED:
            doc = dict(gate=name, nonce=self.state['skills_nonce'], candidate='a'*64, browser_candidate='b'*64,
                       bot_id='owned', observation={'actual': name})
            raw = json.dumps(doc).encode()
            source = 'skills-'+name+'.json'
            (Path(self.state['root'])/source).write_bytes(raw)
            self.actual['receipts'][name] = dict(SchemaVersion=1, Candidate='a'*64, BrowserCandidate='b'*64,
                Nonce=self.state['skills_nonce'], BotID='owned', Home=self.state['url']+'/#/chat/bots/personal/owned',
                Status='passed', Detail='observed', Source=source, SourceHash=hashlib.sha256(raw).hexdigest())
        self.actual['receipts']['owned-restart-three-scheduled']['RunIDs']=['tick1','tick2','tick3']
        self.actual['receipts']['manual-and-scheduled-outcomes'].update(ManualRunID='manual1', RunIDs=['tick1','tick2','tick3'])
        self.actual['receipts']['schedule-removal-manual']['ManualRunID']='manual2'
        for name, receipt in self.actual['receipts'].items():
            path = Path(self.state['root']) / receipt['Source']
            document = json.loads(path.read_text())
            document['identities'] = {key: receipt[key] for key in ('RunIDs', 'ManualRunID') if key in receipt}
            raw = json.dumps(document).encode()
            path.write_bytes(raw)
            receipt['SourceHash'] = hashlib.sha256(raw).hexdigest()

    def test_complete_preserves_17_employee_and_six_quality(self):
        launcher.validate_skills_evidence(self.state, self.actual)
        self.assertEqual(len(self.actual['quality']['employee']['receipts']),17)
        self.assertEqual(len(self.actual['quality']['receipts']),6)

    def test_every_prior_employee_and_quality_gate_remains_mandatory(self):
        for layer, names in [('quality', launcher.QUALITY_REQUIRED), ('employee', launcher.EMPLOYEE_REQUIRED)]:
            for name in names:
                bad = copy.deepcopy(self.actual)
                preserved = bad['quality'] if layer == 'quality' else bad['quality']['employee']
                del preserved['receipts'][name]
                with self.subTest(layer=layer, gate=name), self.assertRaises(RuntimeError):
                    launcher.validate_skills_evidence(self.state, bad)

    def test_missing_duplicate_foreign_candidate_replayed_receipts(self):
        for name in launcher.SKILLS_REQUIRED:
            bad=copy.deepcopy(self.actual); del bad['receipts'][name]
            with self.subTest(missing=name),self.assertRaises(RuntimeError): launcher.validate_skills_evidence(self.state,bad)
            for field,value in [('Candidate','old'),('BrowserCandidate','old'),('Nonce','replayed'),('BotID','foreign'),('Source','../foreign'),('Status','skipped')]:
                bad=copy.deepcopy(self.actual); bad['receipts'][name][field]=value
                with self.subTest(name=name,field=field),self.assertRaises(RuntimeError): launcher.validate_skills_evidence(self.state,bad)
        for key,value in [('passed',list(launcher.SKILLS_REQUIRED)+[launcher.SKILLS_REQUIRED[0]]),('nonce','old'),('quality',{}),('unverified',True),('skipped',['pg'])]:
            bad=copy.deepcopy(self.actual); bad[key]=value
            with self.subTest(key=key),self.assertRaises(RuntimeError): launcher.validate_skills_evidence(self.state,bad)
        bad=copy.deepcopy(self.actual); bad['receipts']['schedule-removal-manual']['ManualRunID']='manual1'
        with self.assertRaises(RuntimeError): launcher.validate_skills_evidence(self.state,bad)

    def test_rehashed_foreign_source_is_not_evidence(self):
        name=launcher.SKILLS_REQUIRED[0]; r=self.actual['receipts'][name]
        source=Path(self.state['root'])/r['Source']; doc=json.loads(source.read_text()); doc['nonce']='old'
        raw=json.dumps(doc).encode(); source.write_bytes(raw); r['SourceHash']=hashlib.sha256(raw).hexdigest()
        with self.assertRaises(RuntimeError): launcher.validate_skills_evidence(self.state,self.actual)

    def test_run_identity_cannot_be_changed_outside_hashed_source(self):
        bad = copy.deepcopy(self.actual)
        bad['receipts']['manual-and-scheduled-outcomes']['ManualRunID'] = 'foreign-manual'
        with self.assertRaises(RuntimeError): launcher.validate_skills_evidence(self.state, bad)

    def test_duplicate_json_keys_and_tampered_source_rejected(self):
        with self.assertRaises(ValueError): launcher.strict_json('{"receipts":{},"receipts":{}}')
        receipt = self.actual['receipts'][launcher.SKILLS_REQUIRED[0]]
        (Path(self.state['root']) / receipt['Source']).write_text('{}')
        with self.assertRaises(RuntimeError): launcher.validate_skills_evidence(self.state, self.actual)

    def test_exact_fresh_exclusive_mode_before_resources(self):
        with patch.object(launcher,'create') as create,patch.object(launcher,'setup') as setup:
            for extra in [[],['--config','x','--state','x'], *[['--config','x',mode] for mode in
                          ('--verify-bot-quality','--verify-bot-employee','--verify-chat-model','--verify-chat-first','--restart-owned','--refresh-browser')],
                          ['--config','x','--employee-command','read']]:
                with self.subTest(extra=extra),self.assertRaises(SystemExit): launcher.main(['--verify-bot-skills']+extra)
            create.assert_not_called(); setup.assert_not_called()


class SkillsDriverTests(unittest.TestCase):
    def events(self):
        return [dict(Package=driver.MODULE+'pkg/api', Action=action, **({'Test':'TestProof'} if action!='start' else {}))
                for action in ('start','run','pass')] + [dict(Package=driver.MODULE+'pkg/api',Action='pass')]

    def validate(self,events):
        evidence=driver.ExactGoEvidence('pkg/api',['TestProof'])
        for e in events: evidence.feed(json.dumps(e))
        return evidence.errors()

    def test_exact_evidence_negative(self):
        events=self.events(); self.assertEqual(self.validate(events),[])
        self.assertTrue(self.validate(events[0:1]+events[2:]))
        self.assertTrue(self.validate(events[:-1]))
        self.assertTrue(self.validate(events+events))
        self.assertTrue(self.validate([dict(e,Package='foreign') for e in events]))
        self.assertTrue(self.validate(events+[dict(events[1],Action='skip')]))
        self.assertTrue(self.validate([events[2],events[1],events[3]]))
        self.assertTrue(self.validate(events+[dict(events[1],Test='TestProof/foreign')]))

    def test_postgres_requires_dsn_no_skips(self):
        with patch.dict(driver.os.environ,{},clear=True),patch.object(driver,'run') as run:
            self.assertEqual(driver.main(['--skills-stage','postgres']),2); run.assert_not_called()
        seen=[]
        def run(command,env,evidence=None,**kw):
            seen.append((command,env,evidence)); return True
        with patch.dict(driver.os.environ,{'ASTONISH_TEST_DSN':'disposable','ASTONISH_BOTS_TEST_DB_ISOLATED':'1'},clear=True),patch.object(driver,'run',side_effect=run):
            self.assertEqual(driver.main(['--skills-stage','postgres']),0)
        self.assertEqual(seen[0][1]['ASTONISH_REQUIRE_SKILL_POSTGRES'],'1')
        self.assertIn('-tags=integration',seen[0][0])
        self.assertEqual(seen[0][2].required,driver.required_cases(driver.POSTGRES['pkg/store/entstore']))

    def test_all_executes_every_gate_and_stops_on_failure(self):
        import tempfile
        with tempfile.TemporaryDirectory() as root:
            config = Path(root) / 'explicit.json'
            config.write_text('{}')
            calls = []
            def run(command, env, evidence=None, **kwargs):
                calls.append((command, env, evidence))
                return True
            source = {'ASTONISH_TEST_DSN': 'disposable', 'ASTONISH_BOTS_TEST_DB_ISOLATED': '1',
                      'OPENAI_API_KEY': 'must-not-inherit', 'KUBECONFIG': 'normal-cluster'}
            with patch.dict(driver.os.environ, source, clear=True), patch.object(launcher, 'config_environment', return_value={'ASTONISH_BOT_BROWSER_PATH': '/explicit/browser'}), patch.object(driver, 'run', side_effect=run):
                self.assertEqual(driver.main(['--skills-stage', 'all', '--config', str(config)]), 0)
            self.assertEqual(len(calls), 6)
            self.assertEqual(calls[-2][0][-1], str(driver.ROOT/'tests/e2eboot/bot_skill_surface_acceptance.py'))
            self.assertEqual(calls[-1][0][-3:], ['--verify-bot-skills', '--config', str(config.resolve())])
            for _, env, _ in calls:
                self.assertNotIn('OPENAI_API_KEY', env)
                self.assertNotIn('KUBECONFIG', env)
            with patch.dict(driver.os.environ, source, clear=True), patch.object(launcher, 'config_environment', return_value={'ASTONISH_BOT_BROWSER_PATH': '/explicit/browser'}), patch.object(driver, 'run', return_value=False) as failed:
                self.assertEqual(driver.main(['--skills-stage', 'all', '--config', str(config)]), 1)
                self.assertEqual(failed.call_count, 1)

    def test_all_requires_explicit_config(self):
        with self.assertRaises(SystemExit): driver.main(['--skills-stage','all'])
        with self.assertRaises(SystemExit): driver.main(['--skills-stage','recovery','--config','x'])

if __name__=='__main__': unittest.main()
