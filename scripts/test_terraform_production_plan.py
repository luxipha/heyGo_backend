import importlib.util
from pathlib import Path
import unittest

spec=importlib.util.spec_from_file_location('production_plan',Path(__file__).with_name('terraform-production-plan.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

class PlanBoundaryTests(unittest.TestCase):
    def resource(self, address, kind, actions, after=None, importing=None):
        return {'address':address,'type':kind,'change':{'actions':actions,'after':after,'importing':importing}}

    def test_restoration_and_existing_secret_import_are_allowed(self):
        resources=[self.resource('google_pubsub_subscription.events["known"]','google_pubsub_subscription',['create'],{'expiration_policy':[{'ttl':''}],'push_config':[{'push_endpoint':'https://service.test/internal/pubsub/event'}]}),self.resource('google_secret_manager_secret.runtime["venue-partner-service-key"]','google_secret_manager_secret',['no-op'],importing={'id':'existing'})]
        self.assertTrue(m.review({'resource_changes':resources},{'known'})['safeForSubscriptionRestoration'])

    def test_deletion_replacement_runtime_update_and_unknown_create_are_rejected(self):
        for address,kind,actions in [('google_pubsub_subscription.events["known"]','google_pubsub_subscription',['delete']),('google_pubsub_subscription.events["known"]','google_pubsub_subscription',['delete','create']),('google_cloud_run_v2_service.api_gateway[0]','google_cloud_run_v2_service',['update']),('google_project_iam_member.unexpected','google_project_iam_member',['create'])]:
            with self.subTest(address=address,actions=actions):
                result=m.review({'resource_changes':[self.resource(address,kind,actions)]},{'known'})
                self.assertFalse(result['safeForSubscriptionRestoration'])

    def test_restored_binding_cannot_expand_existing_permissions(self):
        address='google_pubsub_subscription_iam_member.runtime_subscriber["known"]'
        expected={'member':'serviceAccount:api@example.test','role':'roles/pubsub.subscriber','subscription':'known'}
        resource=self.resource(address,'google_pubsub_subscription_iam_member',['create'],dict(expected))
        self.assertTrue(m.review({'resource_changes':[resource]},{'known'},{address:expected})['safeForSubscriptionRestoration'])
        resource['change']['after']['role']='roles/owner'
        self.assertFalse(m.review({'resource_changes':[resource]},{'known'},{address:expected})['safeForSubscriptionRestoration'])

    def test_expiring_subscription_is_rejected(self):
        resource=self.resource('google_pubsub_subscription.events["known"]','google_pubsub_subscription',['create'],{'expiration_policy':[{'ttl':'2678400s'}],'push_config':[{}]})
        self.assertFalse(m.review({'resource_changes':[resource]},{'known'})['safeForSubscriptionRestoration'])

if __name__=='__main__':unittest.main()
