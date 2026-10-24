#!/usr/bin/env python3
"""Flutter configuration/artifact preflight; static evidence, no credentials printed."""
import argparse,hashlib,json,re,zipfile
from pathlib import Path
BAD={
 'private_key':rb'-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----',
 'service_account_json':rb'"type"\s*:\s*"service_account"',
 'database_credentials':rb'postgres(?:ql)?://[^\s:/]+:[^\s@]+@',
 'aws_access_key':rb'\bAKIA[0-9A-Z]{16}\b',
}
def inspect(app,artifact=None,defines=None):
 root=Path(app).resolve();checks=[]
 def add(name,status,**extra):checks.append(dict(check=name,status=status,**extra))
 pub=root/'pubspec.yaml'
 if not pub.is_file():raise ValueError('Expected Flutter app root')
 text=pub.read_text();v=re.search(r'^version:\s*(\S+)',text,re.M);add('version','present' if v else 'missing',value=v.group(1) if v else None)
 gradles=[root/'android/app/build.gradle',root/'android/app/build.gradle.kts'];gradle=next((p for p in gradles if p.is_file()),None)
 if gradle:
  text=gradle.read_text();app_id=re.search(r'applicationId\s*(?:=\s*)?["\']([^"\']+)',text);add('android_application_id','present' if app_id else 'review',value=app_id.group(1) if app_id else None)
  add('release_signing','review' if re.search(r'signingConfig\s*(?:=\s*)?signingConfigs(?:\[|\.)["\']?debug',text) else 'inspect_native_config',note='Presence of release signing code does not prove keystore validity or artifact signing.')
 add('android_firebase_file','present' if (root/'android/app/google-services.json').is_file() else 'missing')
 add('ios_firebase_file','present' if (root/'ios/Runner/GoogleService-Info.plist').is_file() else 'missing',note='APNs capability and provider setup still need verification.')
 add('shorebird_config','present' if (root/'shorebird.yaml').is_file() else 'absent',note='Optional; config presence does not prove a Shorebird release.')
 findings=[]
 for p in sorted((root/'lib').rglob('*.dart')):
  if p.is_symlink():continue
  b=p.read_bytes();text=b.decode(errors='replace')
  for rule,pattern in BAD.items():
   if re.search(pattern,b):findings.append({'rule':rule,'file':p.relative_to(root).as_posix()})
  for m in re.finditer(r'(?:bool|String)\.fromEnvironment\(\s*["\']([^"\']+)["\'][\s\S]{0,200}?defaultValue:\s*([^,)]+)',text):
   if re.search(r'DEV|BYPASS|SEED|MOCK',m.group(1),re.I):
    value=m.group(2).strip();findings.append({'rule':'development_define_default','file':p.relative_to(root).as_posix(),'line':text.count('\n',0,m.start())+1,'define':m.group(1),'enabled_literal':value in {'true',"'true'",'"true"'}})
 if defines:
  d=json.loads(Path(defines).read_text());enabled=[k for k,v in d.items() if re.search(r'DEV|BYPASS|SEED|MOCK',k,re.I) and str(v).lower() not in {'false','0','','none'}];add('release_defines','blocked' if enabled else 'review',enabled_development_keys=enabled,note='Values omitted. Verify production API/provider settings privately.')
 r={'schema':1,'kind':'mobile','evidence':'static source/config inspection','app':str(root),'checks':checks,'candidates':findings,'limits':['No build, signing, runtime flags, Firebase delivery, store policy or real trip is proven by this report.','Firebase client API keys and project IDs are public configuration, not server secrets.','Credential signatures are a partial scan; native keys require package/certificate restrictions.']}
 if artifact:
  p=Path(artifact);hits=[];scanned=0;skipped=[]
  with zipfile.ZipFile(p) as z:
   for entry in z.infolist():
    if entry.is_dir():continue
    if entry.file_size>256*1024*1024:skipped.append(entry.filename);continue
    b=z.read(entry);scanned+=1
    for rule,pattern in BAD.items():
     if re.search(pattern,b):hits.append({'rule':rule,'entry':entry.filename})
    if re.search(rb'https?://(?:localhost|127\.0\.0\.1|10\.0\.2\.2)(?=[:/\x00\s])',b):hits.append({'rule':'development_url_candidate','entry':entry.filename})
  r['artifact']={'name':p.name,'sha256':hashlib.sha256(p.read_bytes()).hexdigest(),'entries_scanned':scanned,'entries_skipped':skipped,'candidates':hits,'note':'Binary matches need review; this does not verify signing or prove production endpoints are used.'}
 return r
if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--app',required=True);p.add_argument('--artifact');p.add_argument('--defines');p.add_argument('--output');a=p.parse_args()
 try:r=inspect(a.app,a.artifact,a.defines)
 except (ValueError,OSError,zipfile.BadZipFile) as e:raise SystemExit(str(e))
 out=json.dumps(r,indent=2)+'\n'
 if a.output:Path(a.output).write_text(out)
 else:print(out,end='')
