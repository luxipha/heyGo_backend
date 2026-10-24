#!/usr/bin/env python3
"""Read production FCM/IAM/Scheduler state; optional retained validate-only job."""
import argparse,json,subprocess,sys
p=argparse.ArgumentParser(description=__doc__);p.add_argument('--project',default='heygo-ng');p.add_argument('--firebase-project',default='heygo-c5ae1');p.add_argument('--region',default='europe-west1');p.add_argument('--validate-only',action='store_true');a=p.parse_args()
def cloud(*args):
 r=subprocess.run(['gcloud',*args,'--format=json'],capture_output=True,text=True,timeout=180)
 if r.returncode:raise RuntimeError('Cloud inspection failed: '+args[0]+' '+args[1]+'; inspect credentials/permissions privately')
 return json.loads(r.stdout)
try:
 d=cloud('run','services','describe','api-gateway','--project='+a.project,'--region='+a.region);spec=d['spec']['template']['spec'];env={x['name']:x.get('value') for x in spec['containers'][0].get('env',[])};sa=spec['serviceAccountName']
 traffic=[x for x in d['status'].get('traffic',[]) if x.get('percent')]
 configured=bool(env.get('FCM_SERVICE_ACCOUNT_FILE')) or (env.get('FCM_AUTH_MODE')=='adc' and bool(env.get('FCM_PROJECT_ID')))
 scheduler=cloud('scheduler','jobs','describe','gateway-maintenance','--project='+a.project,'--location='+a.region)
 policy=cloud('projects','get-iam-policy',a.firebase_project);roles=[b['role'] for b in policy.get('bindings',[]) if 'serviceAccount:'+sa in b.get('members',[])]
 details=[]
 for role in roles:
  if '/roles/' in role:
   project,name=role.split('/roles/');rd=cloud('iam','roles','describe',name,'--project='+project.split('/')[-1])
  else:rd=cloud('iam','roles','describe',role)
  details.append({'role':role,'can_send':'cloudmessaging.messages.create' in rd.get('includedPermissions',[]),'conditional':any('condition' in b for b in policy.get('bindings',[]) if b['role']==role)})
 r={'schema':1,'kind':'notifications','evidence':'live configuration inspection','configured_template':configured,'auth_mode':env.get('FCM_AUTH_MODE'),'firebase_project':env.get('FCM_PROJECT_ID'),'runtime_identity':sa,'traffic':traffic,'sender_roles':details,'scheduler':{'state':scheduler.get('state'),'schedule':scheduler.get('schedule'),'lastAttemptTime':scheduler.get('lastAttemptTime'),'status_code':scheduler.get('status',{}).get('code',0)},'limits':['Template settings may differ from a serving revision. Verify traffic/revisions.','IAM conditions, deny policies and inherited roles require separate review.','No physical-device or scheduled-broadcast delivery is proven.']}
 if a.validate_only:
  job=cloud('run','jobs','describe','heygo-fcm-validation','--project='+a.project,'--region='+a.region)
  template=job['spec']['template']['spec']['template']['spec'];container=template['containers'][0]
  # Never execute a different command merely because a job has the expected name.
  if container.get('command')!=['/app/fcm-validate'] or template.get('serviceAccountName')!=sa:raise RuntimeError('Unexpected validation job command/identity; stop and review it')
  if not container.get('image','').endswith('/api-gateway:fcm-validate-20261024'):raise RuntimeError('Validation image changed; inspect its source before executing')
  image=cloud('artifacts','docker','images','describe',container['image'],'--project='+a.project)
  if image.get('image_summary',{}).get('digest')!='sha256:694a97085b4c118f52282b194b527651593c466f113bc01aa31bbd87e83f2d29':raise RuntimeError('Validation image digest changed; stop and review its source')
  execution=cloud('run','jobs','execute','heygo-fcm-validation','--project='+a.project,'--region='+a.region,'--wait','--quiet')
  r['validate_only_execution']=execution.get('metadata',{}).get('name');r['evidence']='live configuration and non-delivering validation job'
 print(json.dumps(r,indent=2))
except (RuntimeError,KeyError,ValueError,subprocess.TimeoutExpired) as e:print(json.dumps({'status':'blocked','reason':str(e)}));sys.exit(1)
