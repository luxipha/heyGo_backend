#!/usr/bin/env python3
"""Start only the loopback destination gateway using ignored .dev profiles.

Credentials must already exist in .dev/agent-local.env. This helper never fetches
cloud secrets, edits settings, enables LLMs or falls back to cloud configuration.
"""
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import urllib.request
import urllib.error

ROOT = Path(__file__).resolve().parents[1]
DEV = ROOT / '.dev'

def main():
    spec = importlib.util.spec_from_file_location('localdev', ROOT/'scripts/dev-isolated.py')
    localdev = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(localdev)
    base = localdev.load_profile(DEV/'admin-local.env')
    localdev.guard(base)
    private = localdev.load_profile(DEV/'agent-local.env')
    if base.get('RIDE_AGENT_URL') != 'http://127.0.0.1:8085':
        raise RuntimeError('API Agent URL must be the loopback gateway')
    if base.get('RIDE_AGENT_INTERNAL_TOKEN') != private.get('RIDE_AGENT_INTERNAL_TOKEN') or len(private.get('RIDE_AGENT_INTERNAL_TOKEN','')) < 32:
        raise RuntimeError('Local API/Agent internal credentials must match')
    if private.get('HTTP_ADDR') != '127.0.0.1:8085':
        raise RuntimeError('Agent must bind only loopback')
    if private.get('GEO_PLACE_PROVIDER','google') == 'google' and not private.get('RIDE_AGENT_PLACES_KEY'):
        raise RuntimeError('Backend Places key is required in the ignored private profile')
    if private.get('RIDE_AGENT_LLM_ENABLED') not in ('true', 'false'):
        raise RuntimeError('Local AI enablement must be explicit')
    if private.get('RIDE_AGENT_LLM_ENABLED') == 'true':
        if private.get('RIDE_AGENT_PLANNER_PROVIDER') != 'gemini' or not private.get('GEMINI_API_KEY'):
            raise RuntimeError('Local AI requires the configured Gemini adapter and key')
    if not (os.stat(DEV/'agent-local.env').st_mode & 0o077) == 0:
        raise RuntimeError('Private Agent profile must have mode 0600')
    binary = DEV/'bin/ride-agent-gateway'
    pidfile = DEV/'ride-agent-gateway.pid'
    if localdev.listening(8085):
        if not pidfile.exists():
            raise RuntimeError('Agent port occupied by an untracked process; preserved')
        pid = int(pidfile.read_text().strip())
        result = subprocess.run(['ps','-p',str(pid),'-o','comm='],capture_output=True,text=True)
        if result.stdout.strip() != str(binary):
            raise RuntimeError('Tracked Agent PID does not own the expected executable')
        print('Local Agent already listening on 127.0.0.1:8085')
        return
    env = {k:os.environ[k] for k in ['PATH','HOME','TMPDIR','LANG'] if k in os.environ}
    env.update(private)
    env["DATABASE_URL"] = base["DATABASE_URL"]
    binary.parent.mkdir(exist_ok=True)
    localdev.run(['go','build','-o',str(binary),'./services/ride-agent-gateway'],env,DEV/'ride-agent-gateway-build.log',timeout=180)
    with (DEV/'ride-agent-gateway.log').open('ab') as log:
        process = subprocess.Popen([str(binary)],cwd=ROOT,env=env,stdin=subprocess.DEVNULL,stdout=log,stderr=log,start_new_session=True)
    pidfile.write_text(str(process.pid)+'\n')
    localdev.wait(lambda:localdev.listening(8085),'Local Agent',process=process)
    # An unauthenticated resolution must fail closed; no live Places request here.
    try:
        req=urllib.request.Request('http://127.0.0.1:8085/v1/resolve',data=b'{"schemaVersion":1,"message":"Lekki"}',headers={'Content-Type':'application/json'})
        urllib.request.urlopen(req,timeout=5)
    except urllib.error.HTTPError as err:
        if err.code != 401:
            raise RuntimeError('Unexpected Agent authentication response')
    else:
        raise RuntimeError('Agent accepted an unauthenticated request')
    print('Local Agent listening on 127.0.0.1:8085; authentication enforced; AI enabled: '+private['RIDE_AGENT_LLM_ENABLED'])

if __name__ == '__main__':
    try:
        main()
    except Exception:
        print('Local Agent startup failed; inspect private configuration and .dev logs. No credentials displayed.',file=sys.stderr)
        sys.exit(1)
