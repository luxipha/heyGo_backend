#!/usr/bin/env python3
"""Run spatial inspector checks in a disposable local database, never in the Admin data database."""
import importlib.util
import os
import subprocess
import uuid
from pathlib import Path
from urllib.parse import unquote, urlsplit, urlunsplit

root=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('local',root/'scripts/dev-isolated.py')
local=importlib.util.module_from_spec(spec)
spec.loader.exec_module(local)
profile=local.load_profile(root/'.dev/admin-local.env')
url=local.guard(profile)
name='heygo_inspector_test_'+uuid.uuid4().hex[:12]
env=dict(os.environ,PGHOST='127.0.0.1',PGPORT='55439',PGDATABASE='postgres',PGUSER=unquote(url.username),PGPASSWORD=unquote(url.password or ''))
createdb=local.tool('createdb')
dropdb=str(Path(createdb).with_name('dropdb'))
created=False
try:
 subprocess.run([createdb,name],env=env,check=True,capture_output=True)
 created=True
 target=urlunsplit((url.scheme,url.netloc,'/'+name,url.query,''))
 testenv=dict(env,GEOFENCE_INSPECTOR_TEST_DATABASE_URL=target)
 result=subprocess.run(['go','test','./services/api-gateway/handler','-run','^TestGeofence(PointInspectorSpatialAndEffectiveRules|InspectorRejectsInvalidCoordinates)$','-count=1','-v'],cwd=root,env=testenv,timeout=180)
 raise SystemExit(result.returncode)
finally:
 if created:
  subprocess.run([dropdb,name],env=env,check=True,capture_output=True)
