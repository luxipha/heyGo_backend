#!/usr/bin/env python3
"""Read-only Git/CI and deployed-snapshot reconciliation. JSON; no file contents."""
import argparse,hashlib,json,subprocess,re
from pathlib import Path

def git(root,*args):
 p=subprocess.run(['git','-C',str(root),*args],capture_output=True,text=True,timeout=30)
 if p.returncode: raise RuntimeError('Git inspection failed: '+args[0])
 return p.stdout.strip()
def inspect(root,snapshot=None):
 root=Path(root).resolve();head=git(root,'rev-parse','HEAD')
 status=git(root,'status','--porcelain=v1','-z')
 workflows=root/'.github/workflows';findings=[]
 build=workflows/'cloud-run-images.yaml';deploy=workflows/'deploy-cloud-run.yaml'
 for name,path in [('build',build),('deploy',deploy)]:
  if not path.exists():findings.append({'check':name+'_workflow','status':'missing'})
 if deploy.exists():
  text=deploy.read_text(); findings.append({'check':'deployment_trigger','status':'manual' if 'workflow_dispatch:' in text else 'review'})
  if 'ride-agent-gateway' in text:findings.append({'check':'required_service','status':'optional_needs_live_verification' if 'include_ride_agent:' in text else 'needs_live_verification','service':'ride-agent-gateway'})
  if 'needs:' not in text and 'Require successful checks for the exact selected commit' not in text:findings.append({'check':'quality_gate_dependency','status':'review','note':'Inspect whether deployment requires successful audit/build runs for the same SHA.'})
 if build.exists():findings.append({'check':'build_main_push','status':'present' if re.search(r'push:\s*\n\s*branches:\s*\n\s*- main',build.read_text()) else 'review'})
 result={'schema':1,'kind':'release','evidence':'local source inspection','repository':str(root),'head':head,'branch':git(root,'branch','--show-current'),'dirty':bool(status),'change_entries':len([x for x in status.split('\0') if x]),'checks':findings,'limits':['No workflow execution, provider authentication or production behavior verified.']}
 if snapshot:
  data=json.loads(Path(snapshot).read_text());different=[];missing=[];unsafe=[];files=data.get('files',{})
  for rel,expected in files.items():
   p=(root/rel).resolve()
   if not p.is_relative_to(root):unsafe.append(rel);continue
   if not p.is_file():missing.append(rel)
   elif hashlib.sha256(p.read_bytes()).hexdigest()!=expected:different.append(rel)
  deployed=set(files)
  current={p.relative_to(root).as_posix() for folder in ['services','shared','cmd/migrate'] for p in (root/folder).rglob('*') if p.is_file() and p.suffix in {'.go','.sql'}}
  current.update(n for n in ['go.mod','go.sum'] if (root/n).is_file())
  result['snapshot']={'tag':data.get('tag'),'checked':len(files),'different':different,'missing':missing,'extra_runtime_files':sorted(current-deployed),'unsafe_paths':unsafe,'matches':bool(files) and not (different or missing or unsafe or current-deployed)}
 return result
if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--repo',required=True);p.add_argument('--snapshot');p.add_argument('--output');a=p.parse_args()
 try:r=inspect(a.repo,a.snapshot)
 except (RuntimeError,OSError,ValueError) as e:raise SystemExit(str(e))
 out=json.dumps(r,indent=2)+'\n'
 if a.output:Path(a.output).write_text(out)
 else:print(out,end='')
