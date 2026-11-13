#!/usr/bin/env python3
"""Run only the named isolated HeyGo journey; --run explicitly creates synthetic records."""
import argparse,importlib.util,os,subprocess
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
def main():
 parser=argparse.ArgumentParser(description=__doc__)
 parser.add_argument('--run',action='store_true')
 parser.add_argument('--verify-latest',action='store_true')
 parser.add_argument('--drivers',type=int,choices=range(1,26),default=25)
 parser.add_argument('--scenario',choices=['complete','cancel','decline','reconnect','stale-gps','no-driver'],default='complete')
 args=parser.parse_args()
 spec=importlib.util.spec_from_file_location('isolated',ROOT/'scripts/dev-isolated.py');local=importlib.util.module_from_spec(spec);spec.loader.exec_module(local)
 values=local.load_profile(ROOT/'.dev/admin-local.env');local.guard(values)
 env={k:os.environ[k] for k in ['PATH','HOME','TMPDIR','LANG'] if k in os.environ};env.update(values)
 command=['go','run','./scripts/simulation-journey','--drivers='+str(args.drivers),'--scenario='+args.scenario]
 if args.run:command.append('--run')
 if args.verify_latest:command.append('--verify-latest')
 return subprocess.call(command,cwd=ROOT,env=env)
if __name__=='__main__':raise SystemExit(main())
