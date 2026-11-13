#!/usr/bin/env python3
"""Verify 1000 online Drivers / 100 real quoted bookings against the isolated local backend.
This is an explicit load test, not a production readiness guarantee. Records are retained.
"""
import argparse

def main():
 parser=argparse.ArgumentParser(description=__doc__)
 parser.add_argument('--execute',action='store_true',help='Create synthetic participants and run the local load scenario')
 parser.add_argument('--concurrency',type=int,default=8,choices=range(1,101),metavar='1..100')
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
 pg={**os.environ,'PGHOST':'127.0.0.1','PGPORT':'55439','PGDATABASE':'heygo_rider_staging','PGUSER':m.guard(v).username,'PGPASSWORD':m.guard(v).password or ''}
 def sql(q):
  p=subprocess.run([m.tool('psql'),'-X','-At','-c',q],env=pg,capture_output=True,text=True)
  if p.returncode:raise RuntimeError('Local load inspection query failed')
  return p.stdout.strip()
 try:
  csrf=req('/admin/auth/login',{'username':'admin-local','password':v['LOCAL_ADMIN_PASSWORD']})['csrfToken'];run=req('/admin/simulation/runs',{'name':'Concurrent matching load 1000 Drivers 100 Riders '+str(int(time.time())),'seed':876});rid=run['id'];revision=run['revision']
  for role,count in [('driver',1000),('rider',100)]:
   p={'role':role,'name':'Load '+role,'controller':'simulator','latitude':6.428,'longitude':3.421,'approval':'approved'if role=='driver'else'','autoAccept':role=='driver','online':role=='driver','balanceKobo':0,'modelId':'','modelYear':0,'categoryIds':[],'driveTrip':False,'movementSpeedMps':0,'waitAtPickupSeconds':0}
   revision=req('/admin/simulation/runs/'+rid+'/population',{'revision':revision,'participant':p,'count':count,'radiusMeters':300})['revision']
  begin=time.monotonic();req('/admin/simulation/runs/'+rid+'/start',{'revision':revision,'requestConcurrency':args.concurrency});until(lambda:sql(f"SELECT status FROM simulation_runs WHERE id='{rid}'")in('running','failed'),180);assert sql(f"SELECT status FROM simulation_runs WHERE id='{rid}'")=='running'
  online=int(sql(f"SELECT count(*) FROM simulation_participants WHERE simulation_run_id='{rid}' AND runtime_status='online'"));assert online==1000,online;print('1000 real online Driver sessions connected in',round(time.monotonic()-begin,2),'seconds',flush=True)
  riders=json.loads(sql(f"SELECT json_agg(id::text ORDER BY name)FROM simulation_participants WHERE simulation_run_id='{rid}'AND role='rider'"));result,_=command(riders[0],'search',message='Eko Hotel Victoria Island Lagos');place=result.get('place')or result['candidates'][0];placeId=place['id']
  previewStart=time.monotonic()
  for actor in riders:req('/admin/simulation/runs/'+rid+'/participants/'+actor+'/rider-actions',{'commandId':str(uuid.uuid4()),'action':'preview','placeId':placeId})
  until(lambda:int(sql(f"SELECT count(*)FROM simulation_rider_actions WHERE simulation_run_id='{rid}'AND action='preview'AND status IN ('succeeded','failed')"))==100,300)
  failed=sql(f"SELECT count(*)FROM simulation_rider_actions WHERE simulation_run_id='{rid}'AND action='preview'AND status='failed'");assert failed=='0','Preview failures '+failed
  quotes=json.loads(sql(f"SELECT json_agg(jsonb_build_object('actor',participant_id,'fares',result->'rideFares'))FROM simulation_rider_actions WHERE simulation_run_id='{rid}'AND action='preview'AND status='succeeded'"));print('100 real quotes completed in',round(time.monotonic()-previewStart,2),'seconds',flush=True)
  bookingStart=time.monotonic()
  for quote in quotes:
   fare=next(f for f in quote['fares']if f['packageSlug']=='sedan');req('/admin/simulation/runs/'+rid+'/participants/'+quote['actor']+'/rider-actions',{'commandId':str(uuid.uuid4()),'action':'book','fareId':fare['id'],'confirmed':True})
  until(lambda:int(sql(f"SELECT count(*)FROM simulation_rider_actions WHERE simulation_run_id='{rid}'AND action='book'AND status IN ('succeeded','failed')"))==100,160)
  bookFailed=int(sql(f"SELECT count(*)FROM simulation_rider_actions WHERE simulation_run_id='{rid}'AND action='book'AND status='failed'"));assert bookFailed==0,'Booking failures '+str(bookFailed)
  counts=lambda:json.loads(sql(f"SELECT COALESCE(jsonb_object_agg(status,n),'{{}}')FROM(SELECT t.status,count(*)n FROM trips t JOIN simulation_participants p ON p.actor_user_id=t.rider_id WHERE p.simulation_run_id='{rid}'GROUP BY t.status)x"))
  until(lambda:counts().get('accepted',0)==100,120);assert sql(f"SELECT count(DISTINCT t.assigned_driver_id)FROM trips t JOIN simulation_participants p ON p.actor_user_id=t.rider_id WHERE p.simulation_run_id='{rid}'")=='100';print('100 bookings accepted by 100 distinct real matched Drivers in',round(time.monotonic()-bookingStart,2),'seconds',flush=True)
  state=snapshot();report={'runId':rid,'requestConcurrency':args.concurrency,'passed':True,'onlineDrivers':online,'riders':100,'outcomes':counts(),'distinctAssignedDrivers':100,'summary':state['summary'],'noRoadMovementInLoadTest':True,'unfinishedTripsPreserved':True};Path('/tmp/heygo-sim-matching-load-proof.json').write_text(json.dumps(report,indent=2));print(json.dumps(report),flush=True)
 except Exception as error:
  report={'passed':False,'runId':run['id'] if run else None,'requestConcurrency':args.concurrency,'failure':str(error),'recordsPreserved':True}
  if run:
   try:
    report['summary']=snapshot()['summary']
    report['outcomes']=json.loads(sql(f"SELECT COALESCE(jsonb_object_agg(status,n),'{{}}')FROM(SELECT t.status,count(*)n FROM trips t JOIN simulation_participants p ON p.actor_user_id=t.rider_id WHERE p.simulation_run_id='{run['id']}'GROUP BY t.status)x"))
   except Exception:report['inspection']='Inspect preserved run manually'
  Path('/tmp/heygo-sim-matching-load-proof.json').write_text(json.dumps(report,indent=2));print(json.dumps(report),flush=True)
  raise
 finally:
  if run:
   try:
    state=snapshot()
    if state['status']in('running','starting','paused'):req('/admin/simulation/runs/'+run['id']+'/stop',{'revision':state['revision']});until(lambda:sql(f"SELECT status FROM simulation_runs WHERE id='{run['id']}'")=='stopped',180)
   except Exception as e:print('Cleanup:',str(e),flush=True)
  if csrf:
   try:req('/admin/auth/logout',{})
   except Exception:pass

if __name__=="__main__":main()
