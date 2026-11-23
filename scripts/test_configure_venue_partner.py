import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('venue_config', Path(__file__).with_name('configure-venue-partner.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)

class PreserveConfigurationTests(unittest.TestCase):
    def setUp(self):
        self.before = {'serviceAccountName':'api@example.test','containers':[{'image':'registry/api@sha256:unchanged','env':[{'name':'DATABASE_URL','valueFrom':{'secretKeyRef':{'name':'database','key':'1'}}},{'name':'FCM_PROJECT_ID','value':'firebase'}]}]}

    def test_additions_preserve_existing_settings_and_identity(self):
        after=m.copy.deepcopy(self.before)
        after['containers'][0]['env'].reverse()
        after['containers'][0]['env'].append({'name':'VENUE_PARTNER_URL','value':'https://partner.example.test'})
        m.check_preserved(self.before,after)

    def test_loss_or_change_of_existing_configuration_is_rejected(self):
        for change in ['remove','secret','identity','image']:
            with self.subTest(change=change):
                after=m.copy.deepcopy(self.before)
                if change=='remove':after['containers'][0]['env'].pop()
                if change=='secret':after['containers'][0]['env'][0]['valueFrom']['secretKeyRef']['key']='2'
                if change=='identity':after['serviceAccountName']='other@example.test'
                if change=='image':after['containers'][0]['image']='different'
                with self.assertRaises(RuntimeError):m.check_preserved(self.before,after)

    def test_split_traffic_is_rejected(self):
        with self.assertRaises(RuntimeError):m.active({'status':{'traffic':[{'revisionName':'old','percent':90},{'revisionName':'new','percent':10}]}})

if __name__=='__main__':unittest.main()
