#!/usr/bin/env python3
"""Propose an evidence appendix; never edits the supplied todo or auto-completes candidates."""
import argparse,datetime,json
from pathlib import Path
p=argparse.ArgumentParser(description=__doc__);p.add_argument('--report',action='append',required=True);p.add_argument('--todo');p.add_argument('--output');a=p.parse_args()
if a.todo and a.output and Path(a.todo).resolve()==Path(a.output).resolve():p.error('Output must differ from the real todo')
lines=['## Proposed audit update — '+datetime.date.today().isoformat(),'','Review this proposal before applying it. Static findings remain unverified.','']
for name in a.report:
 r=json.loads(Path(name).read_text());lines.extend(['Source report: '+Path(name).name,'Evidence: '+r.get('evidence','unspecified'),''])
 if r.get('dirty'):lines.append('- [ ] Reconcile working changes with the deployed snapshot before a Git-driven release.')
 for category,count in r.get('counts',{}).items():lines.append(f'- [ ] Review {count} `{category}` candidates in the mounted app; keep confirmed findings only.')
 for check in r.get('checks',[]):
  if isinstance(check,dict) and check.get('status') not in {'passed','present'}:lines.append('- [ ] Review '+check.get('check','check')+' ('+check.get('status','unknown')+').')
 lines.extend(['- [ ] Record authenticated, provider and physical-device checks separately where applicable.',''])
text='\n'.join(lines)+'\n'
if a.output:Path(a.output).write_text(text)
else:print(text,end='')
