#!/usr/bin/env python3
"""Plan or run Flutter analyze/test or Admin production build. Never builds/uploads a release."""
import argparse,hashlib,json,shutil,subprocess
from pathlib import Path
p=argparse.ArgumentParser(description=__doc__);p.add_argument('--app',required=True);p.add_argument('--kind',choices=['flutter','admin'],default='flutter');p.add_argument('--flutter',default='flutter');p.add_argument('--execute',action='store_true');p.add_argument('--output');a=p.parse_args();root=Path(a.app).resolve()
if a.kind=='flutter':
 if not (root/'pubspec.yaml').is_file():p.error('Expected Flutter app root')
 commands=[[a.flutter,'analyze'],[a.flutter,'test']]
else:
 package=root/'package.json'
 if not package.is_file():p.error('Expected Admin package.json')
 if 'build:production' not in json.loads(package.read_text()).get('scripts',{}):p.error('No existing build:production script')
 commands=[['npm','run','build:production']]
r={'schema':1,'kind':'checks','evidence':'plan only','commands':commands,'checks':[],'limits':['Successful local commands do not prove deployed/provider/device behavior.']}
if a.execute:
 r['evidence']='local command execution'
 for cmd in commands:
  try:
   run=subprocess.run(cmd,cwd=root,capture_output=True,timeout=900);code=run.returncode;digest=hashlib.sha256(run.stdout+run.stderr).hexdigest()
  except (OSError,subprocess.TimeoutExpired):code=-1;digest=None
  r['checks'].append({'check':' '.join(cmd[1:]),'status':'passed' if code==0 else 'failed','exit_code':code,'output_sha256':digest})
  if code:break
out=json.dumps(r,indent=2)+'\n'
if a.output:Path(a.output).write_text(out)
else:print(out,end='')
raise SystemExit(1 if any(x['status']=='failed' for x in r['checks']) else 0)
