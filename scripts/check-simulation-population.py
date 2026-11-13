#!/usr/bin/env python3
"""Explicit local-only simulator verification. Creates synthetic records and preserves them."""
import argparse

def main():
 parser=argparse.ArgumentParser(description=__doc__)
 parser.add_argument('--execute',action='store_true',help='Create synthetic local participants and execute the verification')
 args=parser.parse_args()
 if not args.execute:
  parser.print_help();return
 import importlib.util,json,urllib.request,urllib.error,http.cookiejar,uuid,time,os
 from pathlib import Path
 root=Path(__file__).resolve().parents[1];spec=importlib.util.spec_from_file_location('local',root/'scripts/dev-isolated.py');m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m);v=m.load_profile(root/'.dev/admin-local.env');m.guard(v)
 jar=http.cookiejar.CookieJar();op=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar));csrf='';run=None

 def req(path,body=None,attempt=0):
  h={'Content-Type':'application/json','Origin':'http://localhost:4001','X-CSRF-Token':csrf};request=urllib.request.Request('http://127.0.0.1:8080'+path,data=None if body is None else json.dumps(body).encode(),headers=h)
  try:
   with op.open(request,timeout=25) as x:return json.load(x)['data']
  except urllib.error.HTTPError as e:

   if e.code==429 and attempt<3:
    time.sleep(min(60,max(1,int(e.headers.get('Retry-After','1')))));return req(path,body,attempt+1)
   error=json.load(e).get('error',{});error=error if isinstance(error,dict)else {'code':error};raise RuntimeError(f'{path}: {e.code} {error.get("code")}')
 def until(f,seconds=80):
  end=time.monotonic()+seconds
  while time.monotonic()<end:
   value=f()
   if value:return value
   time.sleep(1)
  raise RuntimeError('Timed out')
 def snapshot():return req('/admin/simulation/runs/'+run['id'])
 def command(actor,action,**body):
  cmd=str(uuid.uuid4());payload={'commandId':cmd,'action':action,**body};req('/admin/simulation/runs/'+run['id']+'/participants/'+actor+'/rider-actions',payload)
  result=until(lambda:next((a for a in snapshot().get('riderActions',[])if a['id']==cmd and a['status'] in ('succeeded','failed')),None))
  if result['status']!='succeeded':raise RuntimeError('Action failed: '+str(result))
  return result['result'],payload
 import subprocess
 results=[]
 try:
  csrf=req('/admin/auth/login',{'username':'admin-local','password':v['LOCAL_ADMIN_PASSWORD']})['csrfToken']
  for old in req('/admin/simulation/runs')['items']:
   if old['name'].startswith('Population verification')and old['status']in('starting','running','paused','stopping','failed'):
    try:req('/admin/simulation/runs/'+old['id']+'/recover',{'revision':old['revision']})
    except Exception:pass
  for count in [25,100,1000]:
   run=req('/admin/simulation/runs',{'name':f'Population verification {count} '+str(int(time.time())),'seed':754});revision=run['revision']
   driver={'role':'driver','name':'Population Driver','controller':'simulator','latitude':6.428,'longitude':3.421,'approval':'approved','autoAccept':True,'online':count<=100,'balanceKobo':0,'modelId':'','modelYear':0,'categoryIds':[],'driveTrip':False,'movementSpeedMps':0,'waitAtPickupSeconds':0}
   added=req('/admin/simulation/runs/'+run['id']+'/population',{'revision':revision,'participant':driver,'count':count,'radiusMeters':500});revision=added['revision']
   if count==1000:
    rider={'role':'rider','name':'Population Rider','controller':'simulator','latitude':6.428,'longitude':3.421,'approval':'','autoAccept':False,'online':False,'balanceKobo':0,'modelId':'','modelYear':0,'categoryIds':[],'driveTrip':False,'movementSpeedMps':0,'waitAtPickupSeconds':0}
    revision=req('/admin/simulation/runs/'+run['id']+'/population',{'revision':revision,'participant':rider,'count':100,'radiusMeters':300})['revision']
   start=time.monotonic();req('/admin/simulation/runs/'+run['id']+'/start',{'revision':revision})
   state=until(lambda:(lambda s:s if s['status']in('running','failed','stopped')else None)(snapshot()),500);assert state['status']=='running',state['status'];elapsed=round(time.monotonic()-start,2)
   statuses={};[statuses.update({p['runtimeStatus']:statuses.get(p['runtimeStatus'],0)+1})for p in state['participants']];expected='online'if count<=100 else'offline';assert statuses.get(expected)==count,statuses
   print(json.dumps({'runId':run['id'],'drivers':count,'riders':100 if count==1000 else 0,'startupSeconds':elapsed,'statuses':statuses}),flush=True)
   if count==25:
    paused=req('/admin/simulation/runs/'+run['id']+'/pause',{'revision':state['revision'],'paused':True});assert snapshot()['status']=='paused';req('/admin/simulation/runs/'+run['id']+'/pause',{'revision':paused['revision'],'paused':False});assert snapshot()['status']=='running'
   state=snapshot();start=time.monotonic();req('/admin/simulation/runs/'+run['id']+'/stop',{'revision':state['revision']});until(lambda:snapshot()['status']=='stopped',180);stop=round(time.monotonic()-start,2)
   result={'runId':run['id'],'drivers':count,'riders':100 if count==1000 else 0,'online':count<=100,'startupSeconds':elapsed,'cleanupSeconds':stop};results.append(result);print('Stopped',count,'cleanup seconds',stop,flush=True)
  Path('/tmp/heygo-sim-population-proof.json').write_text(json.dumps(results,indent=2))
 finally:
  if run:
   try:
    state=snapshot()
    if state['status']in('running','starting','paused'):req('/admin/simulation/runs/'+run['id']+'/stop',{'revision':state['revision']})
   except Exception as e:print('Cleanup:',str(e),flush=True)
  if csrf:
   try:req('/admin/auth/logout',{})
   except:pass

if __name__=="__main__":main()
