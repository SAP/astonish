import json
import unittest
from skill_surface_acceptance import CASES, validate

class SkillSurfaceGateTest(unittest.TestCase):
    def events(self):
        return [event for test, cases in CASES.items() for event in
                ([{'Test': test, 'Action': 'output', 'Output': f'SKILL_SURFACE {case} PASS'} for case in cases]
                 + [{'Test': test, 'Action': 'pass'}])]

    def check(self, events):
        return validate('\n'.join(json.dumps(event) for event in events))

    def test_complete(self):
        self.assertEqual(len(self.check(self.events())), sum(map(len, CASES.values())))

    def test_each_missing_case_fails(self):
        events = self.events()
        for index in range(len(events)):
            with self.subTest(index=index), self.assertRaises(ValueError):
                self.check(events[:index] + events[index+1:])

    def test_skip_and_fail_rejected(self):
        for action in ('skip', 'fail'):
            with self.assertRaises(ValueError):
                self.check(self.events() + [{'Test': next(iter(CASES)), 'Action': action}])

    def test_wrong_test_cannot_attest(self):
        events = self.events()
        events[0]['Test'] = 'OtherTest'
        with self.assertRaises(ValueError):
            self.check(events)

if __name__ == '__main__':
    unittest.main()
