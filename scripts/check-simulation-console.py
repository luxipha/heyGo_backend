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
 try:
  csrf=req('/admin/auth/login',{'username':'admin-local','password':v['LOCAL_ADMIN_PASSWORD']})['csrfToken']
  cat=req('/admin/catalogue');print('Active categories:',[c['name'] for c in cat['categories']if c.get('status')=='active'],flush=True)
  run=req('/admin/simulation/runs',{'name':'Console Rider verification '+str(int(time.time())),'seed':42});revision=run['revision'];ids={}
  for role,name,lat in [('driver','Automatic',6.4288),('driver','Pending',6.4281),('rider','Console Rider',6.428)]:
   p={'role':role,'name':name,'controller':'simulator','latitude':lat,'longitude':3.421,'approval':('pending' if name=='Pending' else 'approved')if role=='driver'else'','autoAccept':role=='driver','online':role=='driver','balanceKobo':0,'modelId':'','modelYear':0,'categoryIds':[],'driveTrip':role=='driver','movementSpeedMps':30 if role=='driver' else 0,'waitAtPickupSeconds':60 if role=='driver' else 0}
   added=req('/admin/simulation/runs/'+run['id']+'/participants',{'revision':revision,'participant':p});revision=added['revision'];ids[name]=added['id']
  req('/admin/simulation/runs/'+run['id']+'/start',{'revision':revision});until(lambda:snapshot()['status']=='running');print('Running',run['id'],flush=True)
  result,_=command(ids['Console Rider'],'search',message='Eko Hotel Victoria Island Lagos');place=result.get('place') or result['candidates'][0];preview,_=command(ids['Console Rider'],'preview',placeId=place['id']);assert not snapshot()['trips'];fare=preview['rideFares'][0]['id'];print('Search + real preview; no trip created',flush=True)
  result,payload=command(ids['Console Rider'],'book',fareId=fare,confirmed=True);req('/admin/simulation/runs/'+run['id']+'/participants/'+ids['Console Rider']+'/rider-actions',payload)
  trip=until(lambda:next((t for t in snapshot()['trips']if t['status']=='accepted'),None));assert len(snapshot()['trips'])==1;print('One accepted booking after identical retry',flush=True)
  diag=until(lambda:next((e for e in snapshot()['events']if e['kind']=='matching.candidates'),None),30);trace=req('/admin/simulation/runs/'+run['id']+'/matching/'+trip['id']);assert trace['items'][-1]['data']['candidates'];print('Actual matching diagnostic snapshot',flush=True)
  result,_=command(ids['Console Rider'],'cancel',tripId=trip['id'],confirmed=True,reason='Simulation cancellation verification');until(lambda:any(t['status']=='cancelled'for t in snapshot()['trips']));print('Actual Rider cancellation processed',flush=True)
  Path('/tmp/heygo-sim-console-proof.json').write_text(json.dumps({'runId':run['id'],'passed':True,'searchPreviewWithoutBooking':True,'idempotentBooking':True,'matchingSnapshot':True,'cancelled':True}))
 finally:
  if run:
   try:
    s=snapshot()
    if s['status'] in ('running','starting'):req('/admin/simulation/runs/'+run['id']+'/stop',{'revision':s['revision']});until(lambda:snapshot()['status']=='stopped')
   except Exception as e:print('Cleanup:',str(e))
  if csrf:
   try:req('/admin/auth/logout',{})
   except Exception:pass

if __name__=="__main__":main()
