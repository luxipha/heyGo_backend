#!/usr/bin/env python3
"""Static candidates and API call graph hints. No deletion or completion claims."""
import argparse,collections,json,re
from pathlib import Path
RULES={
 'empty_callback':r'\bon(?:Pressed|Tap|Changed|Selected)\s*:\s*(?:\([^)]*\)|\w+)\s*(?:async\s*)?\{\s*\}',
 'disabled_callback':r'\bon(?:Pressed|Tap|Changed)\s*:\s*null\b',
 'local_preference':r'\b(?:SharedPreferences|setState)\b',
 'fixture_or_placeholder':r'\b(?:mock|fixture|placeholder|demo|seedTrip)\w*|Moniepoint MFB|Ibrahim Adeleke|Build 382|iPhone 15 Pro',
 'fixed_progress':r'\b(?:value|progress)\s*:\s*(?:0(?:\.0)?|\.72|0\.72)\s*[,)]',
 'unavailable':r'not available|[Uu]navailable|[Cc]oming soon',
 'direct_dialog':r'\bshowDialog\s*\(',
}
def files(root):
 return sorted(p for p in root.rglob('*') if p.is_file() and not p.is_symlink() and p.suffix in {'.dart','.tsx','.ts','.go'} and not set(p.parts)&{'build','.git','node_modules','.dart_tool','dist','vendor'})
def audit(app,backend=None):
 root=Path(app).resolve();source=files(root/'lib' if (root/'lib').is_dir() else root/'src');findings=[];texts={p:p.read_text(errors='replace') for p in source}
 for p,text in texts.items():
  for rule,pattern in RULES.items():
   for match in re.finditer(pattern,text):
    line=text.count('\n',0,match.start())+1
    # Omit source excerpts: code may contain credentials or personal data.
    findings.append({'rule':rule,'file':p.relative_to(root).as_posix(),'line':line,'status':'candidate','next':'Trace the mounted caller, callback, persistence and failure behavior.'})
 alltext='\n'.join(texts.values());symbols=[]
 for p,text in texts.items():
  for match in re.finditer(r'\b(?:class|enum)\s+(\w+)',text):
   name=match.group(1);count=len(re.findall(r'\b'+re.escape(name)+r'\b',alltext))
   if count==1:symbols.append({'symbol':name,'file':p.relative_to(root).as_posix(),'status':'candidate_no_other_source_reference','limits':'May be an entrypoint, generated integration or referenced outside scanned sources.'})
 calls=[]
 for p,text in texts.items():
  for match in re.finditer(r'\b(?:api|apiClient|_api|_apiClient|client)\.(\w+)\s*\(',text):calls.append({'method':match.group(1),'file':p.relative_to(root).as_posix(),'line':text.count('\n',0,match.start())+1})
 endpoints=[]
 # Extract only literal route paths; never request bodies, keys or source snippets.
 if backend:
  br=Path(backend).resolve()
  for p in files(br/'services'):
   for match in re.finditer(r'\.(GET|POST|PUT|PATCH|DELETE|HandleFunc|Handle)\s*\(\s*"([^"\n]+)"',p.read_text(errors='replace')):
    route=match.group(2)
    if route.startswith('/') and len(route)<200:endpoints.append({'method':match.group(1),'path':route,'file':p.relative_to(br).as_posix()})
 return {'schema':1,'kind':'integration','evidence':'static candidates only','app':str(root),'files_scanned':len(source),'counts':dict(collections.Counter(x['rule'] for x in findings)),'candidates':findings,'unreferenced_symbol_candidates':symbols,'api_calls':calls,'backend_route_candidates':endpoints,'limits':['Not a Dart/TypeScript compiler or complete call graph. Comments/strings may match.','No finding authorizes removal or proves a mounted flow is broken.','Routes may be grouped, dynamically registered or use other syntax. Trace handlers/services/migrations/events/tests manually.']}
if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--app',required=True);p.add_argument('--backend');p.add_argument('--output');a=p.parse_args()
 root=Path(a.app)
 if not (root/'lib').is_dir() and not (root/'src').is_dir():p.error('Expected an app root with lib/ or src/')
 out=json.dumps(audit(a.app,a.backend),indent=2)+'\n'
 if a.output:Path(a.output).write_text(out)
 else:print(out,end='')
