#!/usr/bin/env python3
"""Read-only HTTP/index/asset verification; explicitly not browser verification."""
import argparse,hashlib,json,ssl,urllib.request,urllib.error
from urllib.parse import urljoin,urlparse
from html.parser import HTMLParser
from pathlib import Path
class Assets(HTMLParser):
 def __init__(self):super().__init__();self.paths=[]
 def handle_starttag(self,tag,attrs):
  a=dict(attrs)
  if tag=='script' and a.get('src'):self.paths.append(a['src'])
  if tag=='link' and a.get('rel')=='stylesheet' and a.get('href'):self.paths.append(a['href'])
def verify(url,dist=None):
 if urlparse(url).scheme!='https':raise ValueError('Use an HTTPS deployment URL')
 ctx=ssl.create_default_context()
 if Path('/etc/ssl/cert.pem').exists():ctx.load_verify_locations('/etc/ssl/cert.pem')
 def get(u):
  try:
   with urllib.request.urlopen(u,context=ctx,timeout=30) as r:return r.status,r.read()
  except urllib.error.HTTPError as e:return e.code,b''
 status,b=get(url);r={'schema':1,'kind':'web','evidence':'HTTP only','status':status,'assets':[],'limits':['HTTP success does not prove rendering, login or API behavior. A Cloudflare 403 requires browser/origin investigation; do not weaken security.']}
 if status!=200:return r
 r['index_sha256']=hashlib.sha256(b).hexdigest();parser=Assets();parser.feed(b.decode(errors='replace'));base=Path(dist).resolve() if dist else None
 if base and (base/'index.html').exists():r['index_matches']=b==(base/'index.html').read_bytes()
 for src in parser.paths:
  target=urljoin(url,src)
  if urlparse(target).netloc!=urlparse(url).netloc:continue
  code,asset=get(target);item={'path':urlparse(target).path,'status':code,'sha256':hashlib.sha256(asset).hexdigest() if code==200 else None}
  if base:
   local=(base/urlparse(target).path.lstrip('/')).resolve()
   item['matches']=code==200 and local.is_relative_to(base) and local.is_file() and local.read_bytes()==asset
  r['assets'].append(item)
 return r
if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--url',required=True);p.add_argument('--dist');a=p.parse_args();print(json.dumps(verify(a.url,a.dist),indent=2))
