#!/usr/bin/env python3
"""Prepare a private, read-only production reconciliation plan; never applies Terraform."""
import argparse
from collections import Counter
import hashlib
import json
import os
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]
SERVICES = ['api-gateway', 'driver-service', 'trip-service', 'payment-service', 'ride-agent-gateway']
VENUE = {'venue-partner-service-key', 'venue-partner-inbound-key'}


def run(args, **kwargs):
    r = subprocess.run(args, capture_output=True, text=True, timeout=600, **kwargs)
    if r.returncode:
        raise RuntimeError('Command failed: '+args[0]+' '+args[1]+'; inspect private evidence before retrying')
    return r.stdout


def save(path, value):
    path.write_text(json.dumps(value, indent=2)+'\n')
    path.chmod(0o600)


def inputs(services, state):
    def env(service):
        return {e['name']: e for e in services[service]['spec']['template']['spec']['containers'][0].get('env', [])}
    a, p = env('api-gateway'), env('payment-service')
    literal = lambda name: a[name]['value']
    r2 = {a[n]['valueFrom']['secretKeyRef']['key'] for n in ['R2_ENDPOINT', 'R2_BUCKET', 'R2_ACCESS_KEY_ID', 'R2_SECRET_ACCESS_KEY']}
    if len(r2) != 1 or literal('FCM_AUTH_MODE') != 'adc':
        raise ValueError('Unexpected R2 version or FCM mode; review rather than replace configuration')
    image = services['api-gateway']['spec']['template']['spec']['containers'][0]['image']
    sha = image.rsplit(':', 1)[-1]
    if len(sha) != 40 or any(c not in '0123456789abcdef' for c in sha):
        raise ValueError('Expected the current immutable SHA-tagged API image')
    return {
        'project_id': 'heygo-ng', 'region': 'europe-west1', 'deploy_services': True,
        'deploy_ride_agent': True, 'image_tag': sha,
        'retain_pull_subscriber_permissions': any(r['name']=='runtime_subscriber' and r.get('instances') for r in state['resources']),
        'allowed_origins': literal('ALLOWED_ORIGINS'), 'admin_frontend_url': literal('ADMIN_FRONTEND_URL'),
        'app_url': p['APP_URL']['value'], 'monnify_base_url': p['MONNIFY_BASE_URL']['value'],
        'bug_report_provider': literal('BUG_REPORT_PROVIDER'),
        'api_gateway_concurrency': services['api-gateway']['spec']['template']['spec']['containerConcurrency'],
        'r2_secret_version': r2.pop(), 'fcm_project_id': literal('FCM_PROJECT_ID'),
        'heroes_public_url': literal('HEROES_PUBLIC_URL'), 'venue_partner_url': literal('VENUE_PARTNER_URL'),
        'venue_partner_inbound_enabled': True,
        'venue_partner_service_key_version': a['VENUE_PARTNER_SERVICE_KEY']['valueFrom']['secretKeyRef']['key'],
        'venue_partner_inbound_key_version': a['VENUE_PARTNER_INBOUND_KEY']['valueFrom']['secretKeyRef']['key'],
        'payment_admin_settings_enabled': 'HEYGO_ADMIN_SETTINGS_KEY' in p,
    }


def review(plan, subscription_names, expected_bindings=None):
    allowed = set()
    for name in subscription_names:
        allowed.add(f'google_pubsub_subscription.events["{name}"]')
        for resource in ['dead_letter_forwarder', 'runtime_subscriber']:
            allowed.add(f'google_pubsub_subscription_iam_member.{resource}["{name}"]')
    imports = set()
    for key in VENUE:
        imports.add(f'google_secret_manager_secret.runtime["{key}"]')
        imports.add(f'google_secret_manager_secret_iam_member.runtime_accessor["api-gateway:{key}"]')
    changes, rejected = [], []
    for r in plan.get('resource_changes', []):
        c = r['change']
        if c['actions'] == ['no-op'] and not c.get('importing'):
            continue
        item = {'address': r['address'], 'actions': c['actions'], 'importing': bool(c.get('importing'))}
        changes.append(item)
        if r['address'] in imports and c['actions'] == ['no-op'] and c.get('importing'):
            continue
        if r['address'] in allowed and c['actions'] == ['create']:
            if r['type'] == 'google_pubsub_subscription':
                if c['after'].get('expiration_policy') != [{'ttl': ''}] or not c['after'].get('push_config'):
                    rejected.append(item)
            if r['type'] == 'google_pubsub_subscription_iam_member':
                expected = (expected_bindings or {}).get(r['address'])
                if not expected or any(c['after'].get(k) != expected.get(k) for k in ['member', 'role', 'subscription']):
                    rejected.append(item)
            continue
        rejected.append(item)
    return {'counts': dict(Counter(','.join(c['actions']) for c in changes)), 'changes': changes, 'unexpectedChanges': rejected, 'safeForSubscriptionRestoration': not rejected}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--output', required=True, help='New private evidence directory under .dev')
    a = p.parse_args()
    out = Path(a.output).resolve()
    if not out.is_relative_to(ROOT/'.dev') or out.exists():
        p.error('Use a new evidence directory under backend/.dev')
    out.mkdir(parents=True, mode=0o700)
    cloud = lambda *args: json.loads(run(['gcloud', *args, '--project=heygo-ng', '--format=json']))
    services = {s: cloud('run', 'services', 'describe', s, '--region=europe-west1') for s in SERVICES}
    state = json.loads(run(['gcloud', 'storage', 'cat', 'gs://heygo-ng-terraform-state/foundation/default.tfstate']))
    save(out/'services-private.json', services)
    save(out/'state-before-private.json', state)
    save(out/'production.tfvars.json', inputs(services, state))
    names = {i['attributes']['name'] for r in state['resources'] if r['type']=='google_pubsub_subscription' and r['name']=='events' for i in r['instances']}
    env = os.environ.copy()
    env['GOOGLE_OAUTH_ACCESS_TOKEN'] = run(['gcloud', 'auth', 'print-access-token']).strip()
    plan = out/'reviewed.tfplan'
    command = ['terraform', '-chdir='+str(ROOT/'infra/terraform'), 'plan', '-input=false', '-no-color', '-lock-timeout=60s', '-var-file='+str(out/'production.tfvars.json'), '-out='+str(plan)]
    r = subprocess.run(command, env=env, capture_output=True, text=True, timeout=600)
    save(out/'plan-output-private.json', {'stdout':r.stdout, 'stderr':r.stderr, 'returncode':r.returncode})
    if r.returncode:
        raise SystemExit('Plan failed; inspect private evidence. Nothing applied.')
    plan.chmod(0o600)
    data = json.loads(run(['terraform', '-chdir='+str(ROOT/'infra/terraform'), 'show', '-json', str(plan)], env=env))
    save(out/'plan-private.json', data)
    bindings = {}
    for resource in state['resources']:
        if resource['type'] == 'google_pubsub_subscription_iam_member':
            for instance in resource.get('instances', []):
                address = resource['type']+'.'+resource['name']+'['+json.dumps(instance['index_key'])+']'
                bindings[address] = instance['attributes']
    result = review(data, names, bindings)
    result['planSha256'] = hashlib.sha256(plan.read_bytes()).hexdigest()
    save(out/'review-summary.json', result)
    print(json.dumps({k:v for k,v in result.items() if k!='changes'}, indent=2))
    if result['unexpectedChanges']:
        raise SystemExit('Unexpected infrastructure changes require separate review. Nothing applied.')


if __name__ == '__main__':
    main()
