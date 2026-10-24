#!/usr/bin/env python3
"""Inspect or install versioned HeyGo skills. Never deletes files; updates require --update."""
import argparse,shutil
from pathlib import Path
p=argparse.ArgumentParser(description=__doc__);p.add_argument('--dest',type=Path,default=Path.home()/'.codex/skills');p.add_argument('--install',action='store_true');p.add_argument('--update',action='store_true');a=p.parse_args()
root=Path(__file__).resolve().parents[1]/'tools/skills';names=['heygo-release','heygo-integration-audit','heygo-mobile-readiness'];pending=[]
for name in names:
 for source in (root/name).rglob('*'):
  if not source.is_file() or '__pycache__' in source.parts:continue
  target=a.dest/name/source.relative_to(root/name)
  if target.exists() and target.read_bytes()==source.read_bytes():continue
  if target.exists() and a.install and not a.update:raise SystemExit('Existing different skill file; review and use --update to replace: '+str(target))
  pending.append((source,target))
print(str(len(pending))+' new/changed skill files; '+('installing' if a.install else 'inspection only'))
if a.install:
 for source,target in pending:target.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(source,target)
 print('Installed three HeyGo skills. No files deleted. New discovery may require a new Codex session.')
