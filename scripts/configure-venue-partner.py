#!/usr/bin/env python3
"""Prepare or promote an additive venue configuration revision. Never reads secret values."""
import argparse
import copy
import json
import re
import ssl
import subprocess
import time
import urllib.error
import urllib.request
from pathlib import Path
from urllib.parse import urlsplit

PROJECT = 'heygo-ng'
REGION = 'europe-west1'
SERVICE = 'api-gateway'
NAMES = {'VENUE_PARTNER_URL', 'VENUE_PARTNER_SERVICE_KEY', 'VENUE_PARTNER_INBOUND_KEY'}


def cloud(*args):
    result = subprocess.run(['gcloud', *args, '--project='+PROJECT, '--region='+REGION], capture_output=True, text=True, timeout=300)
    if result.returncode:
        raise RuntimeError('Cloud operation failed; inspect its status before retrying: '+args[0]+' '+args[1])
    return result.stdout


def describe():
    return json.loads(cloud('run', 'services', 'describe', SERVICE, '--format=json'))


def active(service):
    traffic = [t for t in service['status']['traffic'] if t.get('percent', 0)]
    if len(traffic) != 1 or traffic[0].get('percent') != 100 or not traffic[0].get('revisionName'):
        raise RuntimeError('Expected one explicit revision serving 100% of traffic')
    return traffic[0]['revisionName']


def stable_spec(spec):
    spec = copy.deepcopy(spec)
    spec['containers'][0]['env'] = sorted((e for e in spec['containers'][0].get('env', []) if e['name'] not in NAMES), key=lambda e: e['name'])
    return spec


def check_preserved(before, after):
    if stable_spec(before) != stable_spec(after):
        raise RuntimeError('Candidate changed existing configuration; production traffic remains unchanged')


def check(url, path, expected, body=None):
    context = ssl.create_default_context()
    if Path('/etc/ssl/cert.pem').exists():
        context.load_verify_locations('/etc/ssl/cert.pem')
    for attempt in range(6):
        request = urllib.request.Request(url+path, data=body, headers={'Content-Type': 'application/json'})
        try:
            with urllib.request.urlopen(request, timeout=25, context=context) as response:
                status = response.status
        except urllib.error.HTTPError as error:
            status = error.code
        except OSError:
            if attempt == 5:
                raise RuntimeError('HTTP connection failed: '+path)
            time.sleep(3)
            continue
        if status == expected:
            return
        if status >= 500 and attempt < 5:
            time.sleep(3)
            continue
        raise RuntimeError('HTTP check failed: '+path+' status='+str(status))


def smoke(url):
    for path, status, body in [('/ready', 200, None), ('/auth/channels', 200, None), ('/heroes/venues', 401, None), ('/admin/catalogue', 401, None), ('/partner/venue-earnings', 401, b'{}')]:
        check(url, path, status, body)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--stage', choices=['prepare', 'promote'], required=True)
    p.add_argument('--expected-revision', required=True)
    p.add_argument('--partner-url', default='')
    p.add_argument('--outbound-version', default='')
    p.add_argument('--inbound-version', default='')
    p.add_argument('--tag', required=True)
    p.add_argument('--evidence', default='venue-evidence')
    p.add_argument('--execute', action='store_true')
    a = p.parse_args()
    current = describe()
    if active(current) != a.expected_revision:
        p.error('Production traffic differs from the reviewed revision')
    directory = Path(a.evidence)
    state_file = directory/'candidate.json'
    if not a.execute:
        print(json.dumps({'stage': a.stage, 'currentRevision': active(current), 'action': 'inspection_only'}))
        return
    if not re.fullmatch(r'venue-[a-z0-9-]+', a.tag):
        p.error('Invalid candidate tag')
    if a.stage == 'prepare':
        url = urlsplit(a.partner_url)
        if url.scheme != 'https' or not url.hostname or url.username or url.password or url.path not in ['', '/'] or url.query or url.fragment:
            p.error('Partner URL must be an HTTPS origin without /api')
        if not all(re.fullmatch(r'[1-9][0-9]*', v) for v in [a.outbound_version, a.inbound_version]):
            p.error('Pin both secret references to numeric versions')
        directory.mkdir(parents=True, exist_ok=True)
        if state_file.exists():
            p.error('Evidence already exists; inspect rather than overwrite')
        before = current['spec']['template']['spec']
        prior_revision = json.loads(cloud('run', 'revisions', 'describe', a.expected_revision, '--format=json'))
        # Only these three settings may be added or updated; all others are preserved.
        env = {'VENUE_PARTNER_URL': a.partner_url.rstrip('/')}
        secrets = {'VENUE_PARTNER_SERVICE_KEY': ('heygo-venue-partner-service-key', a.outbound_version), 'VENUE_PARTNER_INBOUND_KEY': ('heygo-venue-partner-inbound-key', a.inbound_version)}
        rollback = {'service': SERVICE, 'previousRevision': active(current), 'previousImage': before['containers'][0]['image'], 'previousTraffic': current['status']['traffic']}
        (directory/'rollback.json').write_text(json.dumps(rollback, indent=2)+'\n')
        cloud('run', 'services', 'update', SERVICE, '--update-env-vars=VENUE_PARTNER_URL='+env['VENUE_PARTNER_URL'], '--update-secrets='+','.join(k+'='+s+':'+v for k,(s,v) in secrets.items()), '--tag='+a.tag, '--no-traffic', '--quiet')
        updated = describe()
        if active(updated) != a.expected_revision:
            raise RuntimeError('Traffic unexpectedly changed during candidate creation')
        target = next(t for t in updated['status']['traffic'] if t.get('tag') == a.tag)
        revision = json.loads(cloud('run', 'revisions', 'describe', target['revisionName'], '--format=json'))
        check_preserved(before, updated['spec']['template']['spec'])
        if revision['status'].get('imageDigest') != prior_revision['status'].get('imageDigest') or not revision['status'].get('imageDigest'):
            raise RuntimeError('Candidate image digest changed')
        mounted = {e['name']:e for e in revision['spec']['containers'][0]['env']}
        if mounted['VENUE_PARTNER_URL'].get('value') != env['VENUE_PARTNER_URL']:
            raise RuntimeError('Candidate URL mismatch')
        for name,(secret,version) in secrets.items():
            if mounted[name].get('valueFrom',{}).get('secretKeyRef') != {'name':secret,'key':version}:
                raise RuntimeError('Candidate secret reference mismatch')
        evidence = {**rollback, 'candidateRevision': target['revisionName'], 'candidateUrl': target['url'], 'candidateTag': a.tag, 'candidateSpec': revision['spec'], 'candidateServiceSpec': updated['spec']['template']['spec'], 'imageDigest': revision['status']['imageDigest']}
        state_file.write_text(json.dumps(evidence, indent=2)+'\n')
        smoke(target['url'])
        evidence['candidateChecksPassed'] = True
        state_file.write_text(json.dumps(evidence, indent=2)+'\n')
        print(json.dumps({'candidateRevision':target['revisionName'], 'candidateUrl':target['url'], 'productionTrafficUnchanged':True, 'existingConfigurationPreserved':True}))
    else:
        evidence = json.loads(state_file.read_text())
        if evidence['previousRevision'] != a.expected_revision or evidence['candidateTag'] != a.tag or not evidence.get('candidateChecksPassed'):
            p.error('Candidate evidence does not match the reviewed rollout')
        revision = json.loads(cloud('run','revisions','describe',evidence['candidateRevision'],'--format=json'))
        if revision['spec'] != evidence['candidateSpec']:
            p.error('Candidate configuration no longer matches its evidence')
        if current['spec']['template']['spec'] != evidence['candidateServiceSpec']:
            p.error('Service template changed since candidate preparation')
        smoke(evidence['candidateUrl'])
        cloud('run','services','update-traffic',SERVICE,'--to-revisions='+evidence['candidateRevision']+'=100','--quiet')
        promoted = describe()
        if active(promoted) != evidence['candidateRevision']:
            raise RuntimeError('Promotion verification failed; inspect traffic before retrying')
        smoke(promoted['status']['url'])
        (directory/'verified.json').write_text(json.dumps({'revision':active(promoted),'image':revision['spec']['containers'][0]['image'],'readinessAndAuthChecks':'passed'},indent=2)+'\n')
        print(json.dumps({'promotedRevision':active(promoted),'checks':'passed'}))


if __name__ == '__main__':
    main()
