import copy
import json
import unittest
from unittest.mock import patch
import local_bot_smoke_launcher as launcher
import bot_employee_launcher_test as employee_tests


class QualityLauncherTests(unittest.TestCase):
    def setUp(self):
        fixture = employee_tests.EmployeeLauncherTests(); fixture.setUp()
        import tempfile
        from pathlib import Path
        import hashlib
        directory = tempfile.TemporaryDirectory(); self.addCleanup(directory.cleanup)
        self.state = dict(fixture.state, workflow=launcher.QUALITY_CONTRACT, quality_nonce='fresh', root=directory.name)
        self.actual = dict(contract=launcher.QUALITY_CONTRACT, nonce='fresh', status='verified', employee=fixture.actual,
                           passed=list(launcher.QUALITY_REQUIRED), receipts={})
        for name in launcher.QUALITY_REQUIRED:
            self.actual['receipts'][name] = dict(Candidate='a'*64, BrowserCandidate='b'*64, Nonce='fresh',
                BotID='owned', Home=self.state['url']+'/#/chat/bots/personal/owned', SchemaVersion=1,
                Source='quality-'+name+'.json', SourceHash=hashlib.sha256(b'{}').hexdigest(), Detail='observed', Status='passed')
            (Path(directory.name)/('quality-'+name+'.json')).write_bytes(b'{}')
        self.actual['receipts']['three-offline-assessed-results']['RunIDs']=['one','two','three']

    def test_complete_distinct_contract(self):
        launcher.validate_quality_evidence(self.state,self.actual)
        self.assertEqual(len(launcher.EMPLOYEE_REQUIRED),17)

    def test_missing_skipped_duplicate_foreign_and_old_evidence(self):
        mutations = [('contract',launcher.EMPLOYEE_CONTRACT),('nonce','old'),('skipped',['x']),('diagnostic_resume',True),
                     ('passed',list(launcher.QUALITY_REQUIRED)+[launcher.QUALITY_REQUIRED[0]]),('employee',{}),('unverified',True)]
        for key,value in mutations:
            actual=copy.deepcopy(self.actual);actual[key]=value
            with self.subTest(key=key), self.assertRaises(RuntimeError): launcher.validate_quality_evidence(self.state,actual)
        for name in launcher.QUALITY_REQUIRED:
            for key,value in [('Candidate','old'),('BrowserCandidate','old'),('Nonce','old'),('BotID','foreign'),('Status','skipped'),('SourceHash','bad')]:
                actual=copy.deepcopy(self.actual);actual['receipts'][name][key]=value
                with self.subTest(name=name,key=key),self.assertRaises(RuntimeError):launcher.validate_quality_evidence(self.state,actual)
        actual=copy.deepcopy(self.actual);actual['receipts']['three-offline-assessed-results']['RunIDs']=['one']*3
        with self.assertRaises(RuntimeError):launcher.validate_quality_evidence(self.state,actual)
        with self.assertRaises(RuntimeError):launcher.validate_quality_evidence(dict(self.state,diagnostic_resume=True),self.actual)

    def test_strict_json_rejects_duplicate_case_alias_and_trailing(self):
        for raw in ['{"status":"verified","status":"blocked"}', '{"a":{"Candidate":1,"candidate":2}}', '{}{}']:
            with self.assertRaises(ValueError):launcher.strict_json(raw)
        self.assertEqual(launcher.strict_json(json.dumps(self.actual)),self.actual)

    def test_quality_requires_config_fresh_and_exclusive_before_resources(self):
        with patch.object(launcher,'create') as create,patch.object(launcher,'setup') as setup:
            for extra in [[], ['--config','x','--state','x'], ['--config','x','--verify-bot-employee'],
                          ['--config','x','--verify-chat-model'],['--config','x','--verify-chat-first'],
                          ['--config','x','--restart-owned'],['--config','x','--refresh-browser']]:
                with self.subTest(extra=extra),self.assertRaises(SystemExit):launcher.main(['--verify-bot-quality']+extra)
            create.assert_not_called();setup.assert_not_called()

if __name__=='__main__':unittest.main()
