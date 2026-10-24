#!/usr/bin/env python3
"""Publish Admin over verified FTPS. Credentials stay in backend/keys.md.
Run without --publish to inspect only. Preserve all existing remote files.
"""
from pathlib import Path
from urllib.parse import urlparse
import argparse,datetime,ftplib,hashlib,io,json,ssl

p=argparse.ArgumentParser();p.add_argument('--publish',action='store_true');args=p.parse_args()
backend=Path(__file__).resolve().parents[1];dist=backend.parent/'apps/admin/dist'
account={};current=None;control=None
for line in (backend/'keys.md').read_text().splitlines():
    if ':' not in line:continue
    key,value=line.split(':',1);key=key.strip().lower();value=value.strip().strip('`')
    if key=='ftp hostname':current={};account=current
    elif current is not None and key in ['username','password']:current[key]=value
    if key=='temporary control panel url':control=value
host=urlparse(control if '://' in control else '//'+control).hostname if control else None
if not host or not account.get('username') or not account.get('password'):raise SystemExit('Hosting configuration incomplete; inspect keys.md privately')
ctx=ssl.create_default_context()
if Path('/etc/ssl/cert.pem').exists():ctx.load_verify_locations('/etc/ssl/cert.pem')
ftp=ftplib.FTP_TLS(context=ctx,timeout=40);ftp.connect(host,21);ftp.login(account['username'],account['password']);ftp.prot_p()
root='/domains/n.heygo.ng/public_html';ftp.cwd(root)
print('Verified FTPS connection to Admin document root.',flush=True)
if not args.publish:
    print('Inspection only. Use --publish after API verification and npm run build:production.');ftp.quit();raise SystemExit(0)
if not (dist/'index.html').is_file():raise SystemExit('Production dist missing')
stamp=datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ');backup=backend/'.dev/releases'/stamp;backup.mkdir(parents=True,exist_ok=True)
def retrieve(rel):
    out=io.BytesIO()
    try:ftp.retrbinary('RETR '+rel,out.write);return out.getvalue()
    except ftplib.error_perm as e:
        if str(e).startswith('550'):return None
        raise

def save_backup(rel,content):
    target=backup/'previous'/rel;target.parent.mkdir(parents=True,exist_ok=True);target.write_bytes(content)

old_index=retrieve('index.html')
if old_index is None:raise SystemExit('Existing Admin entry point not found; stop before publishing')
save_backup('index.html',old_index)
ftp.storbinary('STOR index.html.backup-'+stamp,io.BytesIO(old_index))
files=sorted(p for p in dist.rglob('*') if p.is_file() and p.relative_to(dist).as_posix()!='index.html')
uploaded=0
for local in files:
    rel=local.relative_to(dist).as_posix();new=local.read_bytes();old=retrieve(rel)
    if old==new:continue
    if old is not None:save_backup(rel,old)
    path=''
    for part in rel.split('/')[:-1]:
        path+='/'+part
        try:ftp.mkd(root+path)
        except ftplib.error_perm as e:
            if not str(e).startswith('550'):raise
    ftp.storbinary('STOR '+rel,io.BytesIO(new));uploaded+=1
    if uploaded%10==0:print('Uploaded',uploaded,'changed assets/config files.',flush=True)
index=(dist/'index.html').read_bytes();temp='index.html.next-'+stamp
ftp.storbinary('STOR '+temp,io.BytesIO(index));ftp.rename(temp,'index.html');ftp.quit()
(backup/'manifest.json').write_text(json.dumps({'timestamp':stamp,'root':root,'indexSHA256':hashlib.sha256(index).hexdigest(),'files':{p.relative_to(dist).as_posix():hashlib.sha256(p.read_bytes()).hexdigest() for p in dist.rglob('*') if p.is_file()}},indent=2)+'\n')
print('Published Admin; replaced entry point last. No remote files deleted. Rollback files:',backup,flush=True)
