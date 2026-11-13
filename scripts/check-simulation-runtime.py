#!/usr/bin/env python3
"""Local runtime smoke check: creates synthetic fixtures and actual local bookings.
Uses the existing named development profile. Does not contact production or real
payment destinations. Preserves records; closes only its own test sessions/runs.
"""
import argparse
import importlib.util,json,urllib.request,urllib.error,http.cookiejar,uuid,time,os,subprocess,secrets,hashlib
from pathlib import Path

def main():
 parser=argparse.ArgumentParser(description=__doc__)
 parser.add_argument('--execute',action='store_true',help='Create and run the local synthetic verification journey')
 args=parser.parse_args()
 if not args.execute:
  parser.print_help();return
 root=Path(__file__).resolve().parents[1];spec=importlib.util.spec_from_file_location('local',root/'scripts/dev-isolated.py');local=importlib.util.module_from_spec(spec);spec.loader.exec_module(local);v=local.load_profile(root/'.dev/admin-local.env');u=local.guard(v)
 pg={**os.environ,'PGHOST':u.hostname,'PGPORT':str(u.port),'PGDATABASE':u.path[1:],'PGUSER':u.username,'PGPASSWORD':u.password or ''}
 def sql(q):
  r=subprocess.run([local.tool('psql'),'-X','-At','-c',q],env=pg,capture_output=True,text=True)
  if r.returncode:raise RuntimeError('Local verification query failed')
  return r.stdout.strip()
 jar=http.cookiejar.CookieJar();op=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar));csrf='';riderToken=None;sid=None;run=None;tripID=None
 class ApiError(Exception):pass
 def req(path,body=None,method=None,actor=None,key=None,attempt=0):
  h={'Content-Type':'application/json','Origin':'http://localhost:4001'}
  if actor:h['Authorization']='Bearer '+actor
  elif csrf:h['X-CSRF-Token']=csrf
  if key:h['Idempotency-Key']=key
  r=urllib.request.Request('http://127.0.0.1:8080'+path,None if body is None else json.dumps(body).encode(),h,method=method)
  try:
   with op.open(r,timeout=25) as x:return json.load(x).get('data',{})
  except urllib.error.HTTPError as e:
   if e.code==429 and attempt<2:
    time.sleep(min(60,max(1,int(e.headers.get('Retry-After','1')))));return req(path,body,method,actor,key,attempt+1)
   try:code=json.load(e).get('error',{}).get('code')
   except Exception:code='unknown'
   raise ApiError(f'{path}: HTTP {e.code} ({code})')
 def snapshot():return req('/admin/simulation/runs/'+run['id'])
 def until(check,label,seconds=80):
  end=time.monotonic()+seconds
  while time.monotonic()<end:
   value=check()
   if value:return value
   time.sleep(1)
  raise RuntimeError('Timed out: '+label)
 auth=req('/admin/auth/login',{'username':'admin-local','password':v.get('LOCAL_ADMIN_PASSWORD','')});csrf=auth['csrfToken']
 try:
  run=req('/admin/simulation/runs',{'name':'Movement verification '+str(uuid.uuid4())[:8],'seed':42});revision=run['revision'];ids={}
  for name,lat,approval,auto,balance in [('Manual',6.428,'approved',False,0),('Auto',6.43,'approved',True,0),('Pending',6.42801,'pending',True,0),('Below balance',6.42802,'approved',True,-1)]:
   cfg={'role':'driver','name':name,'controller':'simulator','latitude':lat,'longitude':3.421,'approval':approval,'autoAccept':auto,'online':True,'balanceKobo':balance,'modelId':'','modelYear':0,'categoryIds':[],'driveTrip':name=='Auto','movementSpeedMps':40 if name=='Auto' else 0,'waitAtPickupSeconds':6 if name=='Auto' else 0}
   result=req('/admin/simulation/runs/'+run['id']+'/participants',{'revision':revision,'participant':cfg});revision=result['revision'];ids[name]=result['id']
  req('/admin/simulation/runs/'+run['id']+'/start',{'revision':revision});print('Runtime start requested',flush=True)
  state=until(lambda:(lambda x:x if x['status'] in ['running','failed'] else None)(snapshot()),'runtime setup')
  if state['status']=='failed':print(json.dumps(state['events'][-5:],default=str));raise RuntimeError('Runtime provisioning failed')
  byname={p['name']:p for p in state['participants']};print('Actor states:',{n:p['runtimeStatus'] for n,p in byname.items()},flush=True)
  assert byname['Manual']['runtimeStatus']=='online' and byname['Auto']['runtimeStatus']=='online'
  assert byname['Pending']['runtimeStatus']=='blocked' and byname['Below balance']['runtimeStatus']=='blocked'
  assert byname['Below balance']['live']['balanceKobo']==-1 and byname['Manual']['live']['balanceKobo']==0
  assert byname['Pending']['live']['eligibleToDrive']==False and byname['Below balance']['live']['eligibleToDrive']==True
  reasons={e['participantId']:e['data'].get('reason','') for e in state['events'] if e['kind']=='availability.blocked'}
  assert 'driver_not_eligible' in reasons[ids['Pending']] and 'operating_balance_low' in reasons[ids['Below balance']],reasons
  # A new clearly synthetic Rider session supplies the real booking client.
  uid=str(uuid.uuid4());sid=str(uuid.uuid4());riderToken=secrets.token_hex(32);refresh=secrets.token_hex(32);owner='heygo-simulation:runtime-verification:'+run['id'];h=lambda x:hashlib.sha256(x.encode()).hexdigest()
  sql(f"INSERT INTO users(id,casper_id_user_id,human_id,roles,verified,email,email_verified,active)VALUES('{uid}','{owner}','{owner}',ARRAY['rider'],TRUE,'{uid}@simulation.heygo.invalid',TRUE,TRUE); INSERT INTO user_sessions(id,user_id,access_token_hash,refresh_token_hash,access_expires_at,refresh_expires_at,device_name)VALUES('{sid}','{uid}','{h(riderToken)}','{h(refresh)}',NOW()+INTERVAL '15 minutes',NOW()+INTERVAL '16 minutes','Simulation runtime verification')")
  resolved=req('/rider/agent/resolve',{'schemaVersion':1,'guided':True,'message':'Eko Hotel Victoria Island Lagos'},actor=riderToken);place=resolved.get('place')
  if not place:place=req('/rider/agent/resolve',{'schemaVersion':1,'guided':True,'placeId':resolved['candidates'][0]['id']},actor=riderToken)['place']
  preview=req('/trip/preview',{'schemaVersion':1,'pickup':{'latitude':6.428,'longitude':3.421},'destination':place['location'],'destinationPlaceId':place['id']},actor=riderToken);fare=preview['rideFares'][0]['id'];key=str(uuid.uuid4());req('/trip/start',{'rideFareID':fare,'confirmed':True},actor=riderToken,key=key);req('/trip/start',{'rideFareID':fare,'confirmed':True},actor=riderToken,key=key);tripID=req('/rider/fares/'+fare+'/trip',actor=riderToken)['trip']['id']
  offered=until(lambda:next((p for p in snapshot()['participants'] if p.get('offer',{} ) and p['offer']['tripId']==tripID),None),'current offer')
  assert offered['name']=='Manual','Nearest eligible manual Driver should receive first offer'
  command=str(uuid.uuid4());result=req('/admin/simulation/runs/'+run['id']+'/participants/'+offered['id']+'/offer-response',{'commandId':command,'tripId':tripID,'action':'decline'});assert result['status']=='queued'
  assigned=until(lambda:(lambda x:x if x['status']=='accepted' else None)(req('/rider/trips/'+tripID,actor=riderToken)),'auto-accept after manual decline',100)
  assert assigned['driver']['id']==byname['Auto']['actorUserId'];print('Real offer: manual decline → auto Driver accepted',flush=True)
  events=snapshot()['events'];assert any(e['kind']=='offer.accept_sent' and e['data'].get('source')=='server_preference' for e in events)
  observed={'accepted'};positions=set();deadline=time.monotonic()+240;last=None
  while time.monotonic()<deadline:
   current=req('/rider/trips/'+tripID,actor=riderToken);status=current['status'];observed.add(status)
   if status!=last:print('Authoritative trip stage:',status,flush=True);last=status
   location=(current.get('driver') or {}).get('location') or {}
   if location.get('latitude') is not None:positions.add((location['latitude'],location['longitude']))
   if status=='completed':break
   if status=='cancelled':raise RuntimeError('Unexpected cancellation during movement')
   time.sleep(1)
  else:
   print(json.dumps(snapshot()['events'][-8:],default=str));raise RuntimeError('Movement journey timed out')
  assert {'accepted','arrived','started','completed'}<=observed,observed
  assert len(positions)>=3,'No observable Driver road movement'
  receipt=req('/rider/trips/'+tripID+'/receipt',actor=riderToken);assert receipt['providerVerified']==False and receipt['settlement']['status']=='pending'
  assert sql(f"SELECT count(*) FROM trips WHERE ride_fare_id='{fare}'")=="1"
  movementEvents=[e for e in snapshot()['events'] if e['kind']=='movement.route_ready'];assert {e['data']['leg'] for e in movementEvents}=={'pickup','destination'}
  print('Both road legs passed; observed Driver positions:',len(positions),flush=True)
  state=snapshot();req('/admin/simulation/runs/'+run['id']+'/stop',{'revision':state['revision']});until(lambda:snapshot()['status']=='stopped','runtime cleanup')
  userids=[p['actorUserId'] for p in byname.values()];quoted=','.join("'"+x+"'::uuid" for x in userids)
  assert sql(f'SELECT count(*) FROM user_sessions WHERE user_id IN ({quoted}) AND revoked_at IS NULL')=='0'
  assert sql(f'SELECT count(*) FROM drivers WHERE id IN ({quoted}) AND online_requested')=='0'
  report={'passed':True,'runId':run['id'],'approvedOnline':2,'pendingBlocked':True,'balanceBlocked':True,'manualDeclineRematchedToAuto':True,'serverPreferenceRead':True,'ownedSessionsRevoked':True,'ownedDriversOffline':True,'recordsPreserved':True,'observedStages':sorted(observed),'observedPositions':len(positions),'roadLegs':['pickup','destination'],'singleIdempotentBooking':True,'settlement':'pending'}
  Path('/tmp/heygo-simulation-movement-proof.json').write_text(json.dumps(report,indent=2));print(json.dumps(report),flush=True)
 finally:
  if run:
   try:
    state=snapshot()
    if state['status'] in ['starting','running','paused']:req('/admin/simulation/runs/'+run['id']+'/stop',{'revision':state['revision']})
   except Exception:pass
  if sid:sql(f"UPDATE user_sessions SET revoked_at=NOW()WHERE id='{sid}'")
  req('/admin/auth/logout',{})

if __name__=='__main__': main()
