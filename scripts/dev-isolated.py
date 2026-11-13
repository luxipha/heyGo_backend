#!/usr/bin/env python3
"""Start only the named loopback development stack; never use .env.local/cloud defaults."""
import argparse,json,os,re,shlex,shutil,socket,subprocess,sys,time,urllib.request,urllib.error
from pathlib import Path
from urllib.parse import urlsplit,unquote,parse_qs
ROOT=Path(__file__).resolve().parents[1];DEV=ROOT/'.dev';SERVICES=['trip-service','driver-service','payment-service','api-gateway']
def load_profile(path):
 values={}
 for line in path.read_text().splitlines():
  if not line.strip() or line.lstrip().startswith('#'):continue
  key,sep,value=line.partition('=')
  if not sep or not re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]*',key):raise ValueError('Unsupported local environment entry')
  parts=shlex.split(value,comments=True)
  if len(parts)>1:raise ValueError('Quote multiword environment values')
  values[key]=parts[0] if parts else ''
 return values
def guard(values):
 u=urlsplit(values.get('DATABASE_URL',''))
 if u.scheme not in {'postgres','postgresql'} or u.hostname!='127.0.0.1' or u.port!=55439 or u.path!='/heygo_rider_staging' or set(parse_qs(u.query))-{'sslmode','connect_timeout'}:raise ValueError('Refusing nonstandard database: expected named local database on 127.0.0.1:55439 without host overrides')
 if values.get('PUBSUB_EMULATOR_HOST')!='127.0.0.1:8086' or values.get('GCP_PROJECT_ID')!='heygo-admin-local':raise ValueError('Refusing nonlocal messaging configuration')
 if not u.username:raise ValueError('Local database username required')
 return u
def listening(port):
 with socket.socket() as s:s.settimeout(.3);return s.connect_ex(('127.0.0.1',port))==0
def wait(check,description,timeout=60,process=None):
 until=time.monotonic()+timeout
 while time.monotonic()<until:
  if process is not None and process.poll() is not None:raise RuntimeError(description+' exited; inspect .dev logs')
  if check():return
  time.sleep(.5)
 raise RuntimeError(description+' did not become ready; inspect .dev logs')
def tool(name):
 if name in {'initdb','pg_ctl','psql','createdb'}:
  for base in ['/usr/local/opt/postgresql@17/bin','/opt/homebrew/opt/postgresql@17/bin']:
   candidate=Path(base)/name
   if candidate.is_file():return str(candidate)
 result=shutil.which(name)
 if result:return result
 raise RuntimeError('Required local tool not found: '+name)

def run(cmd,env,log,stdin=None,timeout=180):
 with log.open('ab') as f:
  p=subprocess.run(cmd,cwd=ROOT,env=env,input=stdin,stdout=f,stderr=f,timeout=timeout)
 if p.returncode:raise RuntimeError('Local command failed; inspect '+str(log.relative_to(ROOT)))
def http_status(path):
 try:
  with urllib.request.urlopen('http://127.0.0.1:8080'+path,timeout=2) as r:return r.status==200
 except (OSError,urllib.error.HTTPError):return False
def start(values,initialize=False,service=None,watch=False):
 u=guard(values);env={k:os.environ[k] for k in ['PATH','HOME','TMPDIR','LANG','TZ','JAVA_HOME'] if k in os.environ};env.update(values)
 # Keep helper processes and services loopback-bound, independent of inherited cloud/provider env.
 env.update(HTTP_ADDR='127.0.0.1:8080',PAYMENT_HTTP_ADDR='127.0.0.1:9200',DRIVER_GRPC_ADDR='127.0.0.1:19100',TRIP_GRPC_ADDR='127.0.0.1:9000',PUBSUB_DELIVERY_MODE='pull')
 pg=dict(env,PGHOST='127.0.0.1',PGPORT='55439',PGDATABASE='postgres',PGUSER=unquote(u.username),PGPASSWORD=unquote(u.password or ''))
 cluster=DEV/'postgres';legacy=Path('/private/tmp/heygo-bug-report-pg');new=False
 if not listening(55439):
  if not (cluster/'PG_VERSION').exists() and (legacy/'PG_VERSION').exists():cluster=legacy
  if not (cluster/'PG_VERSION').exists():
   if not initialize:raise RuntimeError('Local PostgreSQL data directory is missing. No database was created. Use --initialize for a fresh persistent .dev/postgres cluster; this cannot restore old demo data.')
   if cluster.exists() and any(cluster.iterdir()):raise RuntimeError('Existing incomplete cluster preserved; inspect it before initialization')
   run([tool('initdb'),'-D',str(cluster),'--username='+unquote(u.username),'--auth-local=trust','--auth-host=trust'],env,DEV/'postgres-bootstrap.log');new=True
  run([tool('pg_ctl'),'-D',str(cluster),'-l',str(DEV/'postgres.log'),'-o','-h 127.0.0.1 -p 55439 -k '+str(DEV),'-w','start'],env,DEV/'postgres-bootstrap.log')
 wait(lambda:listening(55439),'Local PostgreSQL')
 p=subprocess.run([tool('psql'),'-X','-At','-c',"SELECT 1 FROM pg_database WHERE datname='heygo_rider_staging'"],env=pg,capture_output=True,timeout=20)
 if p.returncode:raise RuntimeError('Local PostgreSQL is listening but authentication failed; existing data preserved')
 fresh_db=p.stdout.strip()!=b'1'
 if fresh_db:
  if not initialize:raise RuntimeError('Named local database missing; use --initialize to create it explicitly')
  run([tool('createdb'),'heygo_rider_staging'],pg,DEV/'postgres-bootstrap.log')
 print('PostgreSQL ready'+(' (fresh local database; old records were not restored)' if fresh_db else ''),flush=True)
 run([tool('go'),'run','./cmd/migrate'],env,DEV/'migration-local.log',timeout=240)
 if fresh_db and values.get('LOCAL_ADMIN_PASSWORD'):
  run([tool('go'),'run','./cmd/admin-user','admin-local','super_admin'],env,DEV/'admin-bootstrap.log',stdin=(values['LOCAL_ADMIN_PASSWORD']+'\n').encode())
 print('Local migrations complete',flush=True)
 if not listening(8086):
  with (DEV/'pubsub-emulator.log').open('ab') as log:
   p=subprocess.Popen([tool('gcloud'),'beta','emulators','pubsub','start','--project=heygo-admin-local','--host-port=127.0.0.1:8086','--quiet'],env=env,stdin=subprocess.DEVNULL,stdout=log,stderr=log,start_new_session=True)
  (DEV/'pubsub-emulator.pid').write_text(str(p.pid)+'\n');wait(lambda:listening(8086),'Pub/Sub emulator',process=p)
 # Provision the actual Go contracts and subscriber groups, not a second topic catalogue.
 constants=dict(re.findall(r'^\s*(\w+)\s*=\s*"((?:trip|driver|payment)\.(?:event|cmd)\.[^"]+)"',(ROOT/'shared/contracts/messaging.go').read_text(),re.M))
 def put(path,body):
  req=urllib.request.Request('http://127.0.0.1:8086/v1/projects/heygo-admin-local/'+path,data=json.dumps(body).encode(),method='PUT',headers={'Content-Type':'application/json'})
  try:
   with urllib.request.urlopen(req,timeout=5) as r:r.read()
  except urllib.error.HTTPError as e:
   if e.code!=409:raise RuntimeError('Emulator provisioning failed')
 for topic in constants.values():put('topics/'+topic,{})
 for subscriber_service in SERVICES:
  source=(ROOT/'services'/subscriber_service/'main.go').read_text();group=re.search(r'groupID\s*=\s*"([^"]+)"',source);topics=re.search(r'topics\s*=\s*\[\]string\{([^}]+)\}',source,re.S)
  if not group or not topics:raise RuntimeError('Cannot resolve service messaging contracts: '+subscriber_service)
  for symbol in re.findall(r'contracts\.(\w+)',topics.group(1)):
   topic=constants[symbol];name=group.group(1)+'-'+topic.replace('.','-');put('subscriptions/'+name,{'topic':'projects/heygo-admin-local/topics/'+topic,'ackDeadlineSeconds':30})
 print('Pub/Sub emulator and service subscriptions ready',flush=True)
 if service:
  env['ENV_FILE']=str(DEV/'admin-local.env')
  os.execvpe('sh',['sh',str(ROOT/'scripts/dev.sh'),*(['-w'] if watch else []),service],env)
 for service in SERVICES:
  pidfile=DEV/(service+'.pid');binary=DEV/'bin'/service;port={'trip-service':9000,'driver-service':19100,'payment-service':9200,'api-gateway':8080}[service]
  if pidfile.exists():
   try:pid=int(pidfile.read_text().strip());os.kill(pid,0)
   except (ValueError,ProcessLookupError):pid=None
   if pid:
    executable=subprocess.run(['ps','-p',str(pid),'-o','comm='],capture_output=True,text=True).stdout.strip()
    if executable!=str(binary):raise RuntimeError('Tracked PID belongs to another process; refusing to modify it')
    wait(lambda:listening(port),service);print(service+' already listening',flush=True);continue
  if listening(port):raise RuntimeError('Port '+str(port)+' is occupied by an untracked process; no process was stopped')
  print('Building '+service,flush=True);binary.parent.mkdir(exist_ok=True)
  run([tool('go'),'build','-o',str(binary),'./services/'+service],env,DEV/(service+'-build.log'),timeout=240)
  svcenv=dict(env)
  if service in ['driver-service','trip-service']:svcenv['GRPC_ADDR']='127.0.0.1:'+str(port)
  with (DEV/(service+'.log')).open('ab') as log:
   process=subprocess.Popen([str(binary)],cwd=ROOT,env=svcenv,stdin=subprocess.DEVNULL,stdout=log,stderr=log,start_new_session=True)
  pidfile.write_text(str(process.pid)+'\n');wait(lambda:listening(port),service,process=process)
  print(service+' listening on 127.0.0.1:'+str(port),flush=True)
 if (DEV/'agent-local.env').exists():
  subprocess.run([sys.executable,str(ROOT/'scripts/dev-agent.py')],cwd=ROOT,check=True)
 wait(lambda:http_status('/ready'),'API readiness')
 print('Local backend ready: http://127.0.0.1:8080 — Driver and Rider can connect.',flush=True)
 print('Payment provider readiness and real OTP delivery depend on local Admin configuration. No production settings were copied.',flush=True)
if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('service',nargs='?',choices=SERVICES);p.add_argument('-w','--watch',action='store_true');p.add_argument('--initialize',action='store_true',help='Create a fresh local database only when missing; never resets existing data');a=p.parse_args()
 if a.watch and not a.service:p.error('Watch mode requires a service')
 try:start(load_profile(DEV/'admin-local.env'),a.initialize,a.service,a.watch)
 except (OSError,ValueError,RuntimeError,subprocess.TimeoutExpired) as e:print('Local startup failed: '+str(e),file=sys.stderr);sys.exit(1)
