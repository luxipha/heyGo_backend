#!/usr/bin/env python3
"""Inspect or explicitly dispatch the existing GitHub production workflow. Never commits/pushes."""
import argparse,datetime,json,re,subprocess,sys
from pathlib import Path
from release_check import inspect,git
p=argparse.ArgumentParser(description=__doc__);p.add_argument('--repo',required=True);p.add_argument('--commit',required=True);p.add_argument('--execute',action='store_true');p.add_argument('--confirm');p.add_argument('--backup-id');p.add_argument('--include-ride-agent',action='store_true');a=p.parse_args()
root=Path(a.repo).resolve()
if not re.fullmatch('[a-f0-9]{40}',a.commit):p.error('--commit must be a full lowercase SHA')
r=inspect(root);r['requested_commit']=a.commit;r['action']='inspection_only'
if not a.execute:print(json.dumps(r,indent=2));sys.exit(0)
if a.confirm!=a.commit:p.error('--confirm must equal the exact SHA; obtain task-specific deployment authorization first')
if r['dirty'] or r['head']!=a.commit or r['branch']!='main':raise SystemExit('Stop: clean main at the requested commit is required; review changes separately.')
def gh(*args):
 r=subprocess.run(['gh',*args],cwd=root,capture_output=True,text=True,timeout=60)
 if r.returncode:raise SystemExit('GitHub check failed; inspect authentication/permissions privately. Nothing else dispatched.')
 return r.stdout
remote=git(root,'remote','get-url','origin');m=re.search(r'github\.com[:/]([^/]+/[^/]+?)(?:\.git)?$',remote)
if not m:raise SystemExit('Expected GitHub origin')
repo=m.group(1);main=json.loads(gh('api',f'repos/{repo}/commits/main'))['sha']
if main!=a.commit:raise SystemExit('Stop: remote main does not match requested SHA.')
for workflow in ['audit.yaml','cloud-run-images.yaml']:
 runs=json.loads(gh('api',f'repos/{repo}/actions/workflows/{workflow}/runs?head_sha={a.commit}&branch=main&event=push&per_page=10'))['workflow_runs']
 if not runs or runs[0].get('conclusion')!='success':raise SystemExit('Stop: latest push run must pass for '+workflow)
if not a.backup_id or not a.backup_id.isdigit():raise SystemExit('Stop: --backup-id must identify a reviewed, recent successful production database backup.')
b=subprocess.run(['gcloud','sql','backups','describe',a.backup_id,'--instance=heygo-postgres','--project=heygo-ng','--format=json'],capture_output=True,text=True,timeout=60)
if b.returncode:raise SystemExit('Stop: database backup cannot be verified.')
backup=json.loads(b.stdout)
try:age=(datetime.datetime.now(datetime.timezone.utc)-datetime.datetime.fromisoformat(backup['endTime'].replace('Z','+00:00'))).total_seconds()
except (KeyError,ValueError):raise SystemExit('Stop: backup completion timestamp unavailable.')
if backup.get('status')!='SUCCESSFUL' or not 0<=age<=86400:raise SystemExit('Stop: successful backup within 24 hours is required; this script does not create one.')
# Missing services must be resolved before migrations or candidate deployment.
wf=(root/'.github/workflows/deploy-cloud-run.yaml').read_text()
for service in ['api-gateway','driver-service','trip-service','payment-service','ride-agent-gateway']:
 if service=='ride-agent-gateway' and not a.include_ride_agent:continue
 if service not in wf:continue
 check=subprocess.run(['gcloud','run','services','describe',service,'--project=heygo-ng','--region=europe-west1','--format=value(metadata.name)'],capture_output=True,timeout=45)
 if check.returncode:raise SystemExit('Stop: required Cloud Run service cannot be verified: '+service)
gh('workflow','run','deploy-cloud-run.yaml','--ref','main','-f','image_tag='+a.commit,'-f','confirmation=deploy','-f','backup_id='+a.backup_id,'-f','include_ride_agent='+str(a.include_ride_agent).lower())
print(json.dumps({'dispatched':True,'commit':a.commit,'limits':'Dispatch is not rollout success. Watch the run and verify production.'},indent=2))
