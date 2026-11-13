#!/usr/bin/env python3
"""Use a disposable local database; never seed Heroes fixtures into app data."""
import importlib.util,os,subprocess,uuid,sys
from pathlib import Path
from urllib.parse import unquote,urlunsplit
root=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('dev',root/'scripts/dev-isolated.py');dev=importlib.util.module_from_spec(spec);spec.loader.exec_module(dev)
profile=dev.load_profile(root/'.dev/admin-local.env');url=dev.guard(profile);name='heygo_heroes_test_'+uuid.uuid4().hex[:12]
env=dict(os.environ,PGHOST='127.0.0.1',PGPORT='55439',PGDATABASE='postgres',PGUSER=unquote(url.username),PGPASSWORD=unquote(url.password or ''))
subprocess.run([dev.tool('createdb'),name],env=env,check=True,capture_output=True)
try:
 target=urlunsplit((url.scheme,url.netloc,'/'+name,url.query,''))
 result=subprocess.run(['go','test','./services/api-gateway/handler','-run','^TestHeroes','-count=1','-v',*(['-race'] if '--race' in sys.argv else [])],cwd=root,env=dict(env,HEROES_TEST_DATABASE_URL=target),timeout=600)
 raise SystemExit(result.returncode)
finally:subprocess.run([dev.tool('dropdb'),name],env=env,check=True,capture_output=True)
