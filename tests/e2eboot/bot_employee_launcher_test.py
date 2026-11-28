import copy
import unittest
from unittest.mock import patch
import local_bot_smoke_launcher as launcher

class EmployeeLauncherTests(unittest.TestCase):
    def setUp(self):
        self.state = dict(binary_sha256='a'*64, browser_sha256='b'*64, bot_id='owned', url='http://127.0.0.1:1234')
        self.actual = dict(contract=launcher.EMPLOYEE_CONTRACT, status='verified', candidate='a'*64, browser_candidate='b'*64, passed=list(launcher.EMPLOYEE_REQUIRED), receipts={})
        for name in launcher.EMPLOYEE_REQUIRED:
            self.actual['receipts'][name] = dict(Candidate='a'*64, BotID='owned', Home=self.state['url']+'/#/chat/bots/personal/owned', Detail='assertion', Source='actual-owned-source', SchemaVersion=1)

        self.state['employee_legacy_fixture'] = dict(bot_id='legacy', home_session_id='legacy-home', trigger_id='old-trigger', schedule_id='old-schedule')
        for name in launcher.EMPLOYEE_ADOPTION_REQUIRED:
            self.actual['receipts'][name].update(LegacyBotID='legacy', LegacyHome=self.state['url']+'/#/chat/bots/personal/legacy', HistoricalTriggerID='old-trigger', HistoricalScheduleID='old-schedule')
        self.actual['receipts']['existing-bot-adoption'].update(SourceRunID='new-run', SourceUserEventID='new-event', MandateID='new-mandate', OperationID='new-operation')
        self.actual['receipts']['historical-disabled-schedule']['Stages'] = list(launcher.EMPLOYEE_HISTORY_STAGES)

    def test_complete_contract(self):
        launcher.validate_employee_evidence(self.state, self.actual)

    def test_partial_stale_unknown_and_mismatched_rejected(self):
        for key, value in [('contract', 'chat-model-v1'), ('status', 'running'), ('candidate', 'other'), ('browser_candidate', 'other'), ('passed', ['title-weather']), ('passed', list(launcher.EMPLOYEE_REQUIRED)+['fake'])]:
            with self.subTest(key=key, value=value):
                actual = copy.deepcopy(self.actual); actual[key] = value
                with self.assertRaises(RuntimeError): launcher.validate_employee_evidence(self.state, actual)

    def test_receipts_require_source_schema_and_identity(self):
        for key, value in [('Source',''), ('SchemaVersion',0), ('BotID','other'), ('Home','http://other'), ('Candidate','old')]:
            actual = copy.deepcopy(self.actual); actual['receipts']['schedule-natural'][key] = value
            with self.assertRaises(RuntimeError): launcher.validate_employee_evidence(self.state, actual)

    def test_outstanding_gates_duplicate_passes_and_bad_hash_rejected(self):
        for key, value in [('unimplemented', ['offline-restart-tick']), ('unverified', ['terminal']), ('passed', list(launcher.EMPLOYEE_REQUIRED)+['title-weather'])]:
            actual = copy.deepcopy(self.actual); actual[key] = value
            with self.assertRaises(RuntimeError): launcher.validate_employee_evidence(self.state, actual)
        state = dict(self.state, binary_sha256='not-a-hash')
        actual = dict(self.actual, candidate='not-a-hash')
        with self.assertRaises(RuntimeError): launcher.validate_employee_evidence(state, actual)

    def test_final_receipt_missing_fields_and_thirteen_receipts_remain_failure(self):
        for field in ('Candidate', 'BotID', 'Home', 'Detail', 'Source', 'SchemaVersion'):
            with self.subTest(field=field):
                actual = copy.deepcopy(self.actual)
                del actual['receipts']['terminal-evidence'][field]
                with self.assertRaises(RuntimeError):
                    launcher.validate_employee_evidence(self.state, actual)
        # A deletion receipt never substitutes for the absent final gate,
        # even if the outer status is incorrectly changed to verified.
        for status in ('blocked', 'verified'):
            actual = copy.deepcopy(self.actual)
            actual['status'] = status
            actual['passed'].remove('terminal-evidence')
            del actual['receipts']['terminal-evidence']
            with self.assertRaises(RuntimeError):
                launcher.validate_employee_evidence(self.state, actual)
            self.assertEqual(len(set(actual['receipts']) - set(launcher.EMPLOYEE_ADOPTION_REQUIRED)), 13)
            self.assertNotIn('terminal-evidence', actual['receipts'])

    def test_original_fourteen_cannot_replace_adoption_receipts(self):
        self.assertEqual(len(launcher.EMPLOYEE_REQUIRED), 17)
        actual = copy.deepcopy(self.actual)
        for name in launcher.EMPLOYEE_ADOPTION_REQUIRED:
            actual['passed'].remove(name)
            del actual['receipts'][name]
        self.assertEqual(len(actual['receipts']), 14)
        with self.assertRaises(RuntimeError): launcher.validate_employee_evidence(self.state, actual)

    def test_adoption_receipts_fail_closed(self):
        for name in launcher.EMPLOYEE_ADOPTION_REQUIRED:
            for field in ('LegacyBotID', 'LegacyHome', 'HistoricalTriggerID', 'HistoricalScheduleID'):
                with self.subTest(name=name, field=field):
                    actual = copy.deepcopy(self.actual)
                    actual['receipts'][name][field] = 'foreign'
                    with self.assertRaises(RuntimeError): launcher.validate_employee_evidence(self.state, actual)
        for field in ('SourceRunID', 'SourceUserEventID', 'MandateID', 'OperationID'):
            actual = copy.deepcopy(self.actual)
            del actual['receipts']['existing-bot-adoption'][field]
            with self.assertRaises(RuntimeError): launcher.validate_employee_evidence(self.state, actual)
        for stage in launcher.EMPLOYEE_HISTORY_STAGES:
            actual = copy.deepcopy(self.actual)
            actual['receipts']['historical-disabled-schedule']['Stages'].remove(stage)
            with self.assertRaises(RuntimeError): launcher.validate_employee_evidence(self.state, actual)
        state = copy.deepcopy(self.state)
        del state['employee_legacy_fixture']
        with self.assertRaises(RuntimeError): launcher.validate_employee_evidence(state, self.actual)

    def test_employee_state_with_config_reaches_diagnostic_resume_guard(self):
        import json
        import tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state_path = root/'state.json'; config_path = root/'config.json'
            state_path.write_text(json.dumps({'root':str(root)})); config_path.write_text('{}')
            with patch.object(launcher, 'config_environment', return_value={}), patch.object(launcher, 'prerequisites', return_value=[]), patch.object(launcher, 'create') as create, patch.object(launcher, 'validate_employee_resume', side_effect=RuntimeError('diagnostic guard')) as guard, patch.object(launcher, 'setup') as setup:
                self.assertEqual(launcher.main(['--verify-bot-employee','--config',str(config_path),'--state',str(state_path)]), 1)
                guard.assert_called_once(); create.assert_not_called(); setup.assert_not_called()

    def test_config_required_and_conflicting_modes_rejected_before_create(self):
        with patch.object(launcher, 'create') as create:
            for args in [[], ['--state','/tmp/old'], ['--config','/tmp/config','--verify-chat-model']]:
                with self.assertRaises(SystemExit): launcher.main(['--verify-bot-employee']+args)
            create.assert_not_called()

if __name__ == '__main__': unittest.main()
