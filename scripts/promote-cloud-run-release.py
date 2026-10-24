#!/usr/bin/env python3
"""Promote existing immutable release images; preserve service config/IAM.
Migrations must already have succeeded. No credential values are read or printed.
"""
from pathlib import Path
import argparse,concurrent.futures,json,subprocess,time,urllib.request,urllib.error,ssl
p=argparse.ArgumentParser();p.add_argument('--tag',required=True);p.add_argument('--deploy',action='store_true');args=p.parse_args()
if not args.tag.startswith('release-') or any(c not in 'abcdefghijklmnopqrstuvwxyz0123456789-' for c in args.tag):raise SystemExit('Use an immutable release-* image tag')
project='heygo-ng';region='europe-west1';registry='europe-west1-docker.pkg.dev/heygo-ng/heygo';services=['driver-service','trip-service','payment-service','api-gateway']
backend=Path(__file__).resolve().parents[1];out=backend/'.dev/releases'/args.tag;out.mkdir(parents=True,exist_ok=True)
def cloud(*parts):return subprocess.check_output(['gcloud',*parts,'--project='+project,'--region='+region],stderr=subprocess.PIPE).decode()
def describe(name):return json.loads(cloud('run','services','describe',name,'--format=json'))
original={}
for name in services:
 d=describe(name);original[name]={'image':d['spec']['template']['spec']['containers'][0]['image'],'traffic':d['status'].get('traffic',[]),'url':d['status']['url']}
if not args.deploy:print('Inspected current revisions. Add --deploy only after backup and migration verification.');raise SystemExit(0)
rollback=out/'rollback.json'
if rollback.exists():raise SystemExit('Release already started; inspect recorded state rather than overwriting rollback information')
rollback.write_text(json.dumps(original,indent=2)+'\n')
def candidate(name):
 image=registry+'/'+name+':'+args.tag
 try:
  result=cloud('run','services','update',name,'--image='+image,'--tag='+args.tag,'--no-traffic','--quiet')
 except subprocess.CalledProcessError:
  raise RuntimeError('Candidate creation failed for '+name+'; inspect Cloud Run status privately')
 d=describe(name);t=next(t for t in d['status']['traffic'] if t.get('tag')==args.tag)
 return name,{'revision':t['revisionName'],'url':t['url'],'image':image}
with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool: candidates=dict(pool.map(candidate,services))
(out/'candidates.json').write_text(json.dumps(candidates,indent=2)+'\n')
tls=ssl.create_default_context()
if Path('/etc/ssl/cert.pem').exists():tls.load_verify_locations('/etc/ssl/cert.pem')
def check(url,path,status=200,method='GET',body=None):
 req=urllib.request.Request(url+path,data=body,method=method,headers={'Origin':'https://n.heygo.ng','Content-Type':'application/json'})
 for attempt in range(12):
  try:
   with urllib.request.urlopen(req,timeout=25,context=tls) as response:
    code=response.status;payload=response.read()
  except urllib.error.HTTPError as e:code=e.code;payload=e.read()
  except OSError:
   if attempt==11:raise RuntimeError('Candidate connection failed for '+path)
   time.sleep(3);continue
  if code==status:return payload
  if code>=500 and attempt<11:time.sleep(3);continue
  raise RuntimeError('Candidate check failed for '+path+': '+str(code))
for name in ['payment-service','api-gateway']:check(candidates[name]['url'],'/ready')
api=candidates['api-gateway']['url'];channels=json.loads(check(api,'/auth/channels'))
if not isinstance(channels.get('data'),dict):raise SystemExit('Invalid auth channels response')
check(api,'/driver/vehicle',401);check(api,'/admin/catalogue',401)
check(api,'/auth/pin/recovery/reset',400,'POST',b'{}')
# Promote dependencies first; the API remains on its previous revision until last.
for name in services:
 cloud('run','services','update-traffic',name,'--to-revisions='+candidates[name]['revision']+'=100','--quiet')
 print('Promoted',name,candidates[name]['revision'],flush=True)
for name in ['payment-service','api-gateway']:check(original[name]['url'],'/ready')
check(original['api-gateway']['url'],'/auth/channels');check(original['api-gateway']['url'],'/auth/pin/recovery/reset',400,'POST',b'{}')
(out/'verified.json').write_text(json.dumps({'tag':args.tag,'candidates':candidates,'checks':['public ready','auth channels','owner/staff authentication enforced','PIN recovery validation without sending OTP']},indent=2)+'\n')
print('Production rollout verified. No login, OTP send, payment or provider write was performed.',flush=True)
