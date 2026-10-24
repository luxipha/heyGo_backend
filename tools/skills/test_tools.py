import importlib.util,json,subprocess,tempfile,unittest,zipfile
from pathlib import Path
ROOT=Path(__file__).resolve().parent
def module(skill,file):
 spec=importlib.util.spec_from_file_location(file,ROOT/skill/'scripts'/(file+'.py'));m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m);return m
release=module('heygo-release','release_check');audit=module('heygo-integration-audit','integration_audit');mobile=module('heygo-mobile-readiness','mobile_check')
class Tools(unittest.TestCase):
 def setUp(self):self.tmp=tempfile.TemporaryDirectory();self.root=Path(self.tmp.name)
 def tearDown(self):self.tmp.cleanup()
 def test_git_snapshot_reports_uncommitted_extra_and_changed(self):
  r=self.root;subprocess.run(['git','init','-q',str(r)],check=True)
  (r/'services').mkdir();(r/'services/a.go').write_text('package a\n');(r/'go.mod').write_text('module a\n')
  subprocess.run(['git','-C',str(r),'add','.'],check=True);subprocess.run(['git','-C',str(r),'-c','user.name=Test','-c','user.email=test@example.invalid','commit','-qm','fixture'],check=True)
  import hashlib
  snap=r/'snapshot.json';snap.write_text(json.dumps({'files':{'services/a.go':hashlib.sha256(b'package a\n').hexdigest(),'go.mod':hashlib.sha256(b'module a\n').hexdigest()}}))
  self.assertTrue(release.inspect(r,snap)['snapshot']['matches'])
  (r/'services/a.go').write_text('package changed\n');(r/'services/b.go').write_text('package b\n');result=release.inspect(r,snap)
  self.assertTrue(result['dirty']);self.assertFalse(result['snapshot']['matches']);self.assertEqual(result['snapshot']['extra_runtime_files'],['services/b.go']);self.assertEqual(result['snapshot']['different'],['services/a.go'])
 def test_no_callback_source_or_secret_echo(self):
  (self.root/'lib').mkdir();(self.root/'lib/view.dart').write_text("class Alone {}\nButton(onPressed: () {});\nconst password='FAKE_PRIVATE_VALUE';\n")
  r=audit.audit(self.root);self.assertEqual(r['counts']['empty_callback'],1);self.assertNotIn('FAKE_PRIVATE_VALUE',json.dumps(r));self.assertEqual(r['unreferenced_symbol_candidates'][0]['symbol'],'Alone')
 def test_mobile_public_firebase_key_not_secret(self):
  (self.root/'lib').mkdir();(self.root/'pubspec.yaml').write_text('version: 1.0.0+1\n');(self.root/'lib/config.dart').write_text("const key='AIzaPUBLIC_CLIENT_KEY';")
  self.assertEqual(mobile.inspect(self.root)['candidates'],[])
 def test_artifact_leaks_only_entry_and_rule(self):
  (self.root/'lib').mkdir();(self.root/'pubspec.yaml').write_text('version: 1.0.0+1\n');artifact=self.root/'app.aab'
  with zipfile.ZipFile(artifact,'w') as z:z.writestr('base/assets/config.json','{"type":"service_account","private_key":"FAKE_PRIVATE_VALUE"}')
  r=mobile.inspect(self.root,artifact);self.assertEqual(r['artifact']['candidates'][0]['rule'],'service_account_json');self.assertNotIn('FAKE_PRIVATE_VALUE',json.dumps(r))
 def test_todo_guard_does_not_overwrite(self):
  todo=self.root/'todo.md';todo.write_text('original');report=self.root/'report.json';report.write_text('{"counts":{"empty_callback":2}}')
  script=ROOT/'heygo-integration-audit/scripts/propose_todo.py';r=subprocess.run(['python3',str(script),'--report',str(report),'--todo',str(todo),'--output',str(todo)],capture_output=True)
  self.assertNotEqual(r.returncode,0);self.assertEqual(todo.read_text(),'original')
 def test_checks_plan_does_not_execute(self):
  (self.root/'pubspec.yaml').write_text('name: fixture\n');script=ROOT/'heygo-mobile-readiness/scripts/run_checks.py';r=subprocess.run(['python3',str(script),'--app',str(self.root),'--flutter','not-an-installed-command'],capture_output=True,text=True)
  self.assertEqual(r.returncode,0);self.assertEqual(json.loads(r.stdout)['evidence'],'plan only')
 def test_checks_execute_failure_is_failure(self):
  (self.root/'pubspec.yaml').write_text('name: fixture\n');script=ROOT/'heygo-mobile-readiness/scripts/run_checks.py';r=subprocess.run(['python3',str(script),'--app',str(self.root),'--flutter','not-an-installed-command','--execute'],capture_output=True,text=True)
  self.assertNotEqual(r.returncode,0);self.assertEqual(json.loads(r.stdout)['checks'][0]['status'],'failed')
 def test_todo_proposal_never_checks_candidates(self):
  report=self.root/'report.json';report.write_text('{"counts":{"empty_callback":2},"evidence":"static candidates only"}')
  r=subprocess.run(['python3',str(ROOT/'heygo-integration-audit/scripts/propose_todo.py'),'--report',str(report)],capture_output=True,text=True,check=True)
  self.assertNotIn('[x]',r.stdout);self.assertIn('Review 2',r.stdout)
if __name__=='__main__':unittest.main()
