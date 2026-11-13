import { test } from 'node:test';
import assert from 'node:assert/strict';
import { proxy } from './worker.mjs';
const env = { API_ORIGIN: 'https://api-gateway-example-ew.a.run.app' };
test('preserves credentialed requests and cookies without caching staff data', async () => {
 let outgoing;
 const request = new Request('https://api.heygo.ng/admin/support/cases?id=real', { method: 'POST', headers: { Cookie: 'staff=session', 'X-CSRF-Token': 'csrf', Origin: 'https://n.heygo.ng', 'Content-Type': 'application/json' }, body: JSON.stringify({ subject: 'Review' }) });
 const response = await proxy(request,env,async (value) => { outgoing = value; return new Response('{}',{ headers: { 'Set-Cookie': 'staff=session; HttpOnly; Secure; SameSite=Lax; Path=/admin', 'Cache-Control': 'public, max-age=300', 'Access-Control-Allow-Origin': 'https://n.heygo.ng' } }); });
 assert.equal(outgoing.url,'https://api-gateway-example-ew.a.run.app/admin/support/cases?id=real');
 assert.equal(outgoing.headers.get('Cookie'),'staff=session'); assert.equal(outgoing.headers.get('X-CSRF-Token'),'csrf');
 assert.equal(outgoing.headers.get('Origin'),'https://n.heygo.ng'); assert.equal(await outgoing.text(),'{"subject":"Review"}');
 assert.equal(outgoing.redirect,'manual'); assert.equal(response.headers.get('Cache-Control'),'no-store');
 assert.match(response.headers.get('Set-Cookie'),/HttpOnly; Secure; SameSite=Lax/);
});
test('rejects unconfigured/arbitrary upstreams and never uses a query URL as upstream', async () => {
 for (const url of ['', 'http://localhost:8080','https://evil.example','https://u:p@service.run.app','https://service.run.app/admin']) {
  let called=false; const response=await proxy(new Request('https://api.heygo.ng/?url=https://evil.example'),{ API_ORIGIN:url },async()=>{called=true;return new Response();});
  assert.equal(response.status,503); assert.equal(called,false);
 }
});
test('passes permission errors through and fails closed on upstream failure', async () => {
 const request=()=>new Request('https://api.heygo.ng/admin/staff');
 assert.equal((await proxy(request(),env,async()=>new Response('{"error":{"code":"denied"}}',{status:403}))).status,403);
 const response=await proxy(request(),env,async()=>{throw new Error('private upstream details');});
 assert.equal(response.status,502);assert.equal((await response.text()).includes('private upstream details'),false);
});
