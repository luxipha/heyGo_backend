import importlib.util,tempfile,unittest,subprocess,os
from unittest.mock import patch,MagicMock
from pathlib import Path
spec=importlib.util.spec_from_file_location('local_stack',Path(__file__).with_name('dev-isolated.py'));m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
class LocalStack(unittest.TestCase):
 def values(self):return {'DATABASE_URL':'postgres://local@127.0.0.1:55439/heygo_rider_staging?sslmode=disable','PUBSUB_EMULATOR_HOST':'127.0.0.1:8086','GCP_PROJECT_ID':'heygo-admin-local'}
 def test_accepts_exact_local_profile(self):self.assertEqual(m.guard(self.values()).port,55439)
 def test_refuses_remote_and_overrides(self):
  for url in ['postgres://user@remote.example:55439/heygo_rider_staging','postgres://local@127.0.0.1:5432/heygo_rider_staging','postgres://local@127.0.0.1:55439/other','postgres://local@127.0.0.1:55439/heygo_rider_staging?host=remote.example']:
   with self.subTest(url=url),self.assertRaises(ValueError):v=self.values();v['DATABASE_URL']=url;m.guard(v)
 def test_refuses_real_messaging(self):
  v=self.values();v['PUBSUB_EMULATOR_HOST']='remote.example:8086'
  with self.assertRaises(ValueError):m.guard(v)
 def test_dead_process_is_startup_failure(self):
  class Dead:
   def poll(self):return 1
  with self.assertRaises(RuntimeError):m.wait(lambda:False,'fixture',timeout=1,process=Dead())
 def test_quoted_env_without_execution(self):
  with tempfile.TemporaryDirectory() as d:
   p=Path(d)/'profile';p.write_text("A='quoted value'\nB='$(do-not-execute)'\n")
   self.assertEqual(m.load_profile(p),{'A':'quoted value','B':'$(do-not-execute)'})
 def test_default_start_launches_four_background_services_without_foreground_exec(self):
  with tempfile.TemporaryDirectory() as d:
   ports={55439,8086};launched=[]
   def launch(cmd,**kwargs):
    name=Path(cmd[0]).name;launched.append(name)
    self.assertNotIn('UNRELATED_CLOUD_SECRET',kwargs['env'])
    ports.add({'trip-service':9000,'driver-service':19100,'payment-service':9200,'api-gateway':8080}[name])
    p=MagicMock();p.pid=10000+len(launched);p.poll.return_value=None;return p
   with patch.object(m,'DEV',Path(d)),patch.object(m,'tool',side_effect=lambda n:n),patch.object(m,'listening',side_effect=lambda p:p in ports),patch.object(m,'run'),patch.object(m.subprocess,'run',return_value=subprocess.CompletedProcess([],0,stdout=b'1')),patch.object(m.subprocess,'Popen',side_effect=launch),patch.object(m.urllib.request,'urlopen'),patch.object(m,'http_status',return_value=True),patch.object(m.os,'execvpe',side_effect=AssertionError('Unexpected foreground execution')),patch.dict(os.environ,{'UNRELATED_CLOUD_SECRET':'fake'}):
    m.start(self.values())
   self.assertEqual(launched,m.SERVICES)
if __name__=='__main__':unittest.main()
