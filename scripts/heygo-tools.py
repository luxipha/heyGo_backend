#!/usr/bin/env python3
"""HeyGo automation entrypoint. Subcommands are read-only/plans unless explicit execution flags are supplied."""
import argparse,subprocess,sys
from pathlib import Path
COMMANDS={
 'release':('heygo-release','release_check.py'),
 'ci-release':('heygo-release','ci_release.py'),
 'notifications':('heygo-release','notification_check.py'),
 'web':('heygo-release','verify_web.py'),
 'audit':('heygo-integration-audit','integration_audit.py'),
 'todo':('heygo-integration-audit','propose_todo.py'),
 'mobile':('heygo-mobile-readiness','mobile_check.py'),
 'checks':('heygo-mobile-readiness','run_checks.py'),
}
p=argparse.ArgumentParser(description=__doc__);p.add_argument('command',choices=COMMANDS);a=p.parse_args(sys.argv[1:2]);rest=sys.argv[2:]
root=Path(__file__).resolve().parents[1]/'tools/skills';skill,script=COMMANDS[a.command]
raise SystemExit(subprocess.call([sys.executable,str(root/skill/'scripts'/script),*rest]))
