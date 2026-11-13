// A fixed upstream, not an open proxy. No cookies, bodies or tokens are logged.
export async function proxy(request, env, fetchUpstream = fetch) {
 let origin;
 try {
  origin = new URL(env.API_ORIGIN);
  if (origin.protocol !== 'https:' || !origin.hostname.endsWith('.run.app') || origin.username || origin.password || origin.pathname !== '/' || origin.search || origin.hash) throw new Error('invalid origin');
 } catch {
  return Response.json({ error: { code: 'api_unconfigured', message: 'API routing is not configured' } }, { status: 503, headers: { 'Cache-Control': 'no-store' } });
 }
 const incoming = new URL(request.url);
 const target = new URL(origin);
 target.pathname = incoming.pathname;
 target.search = incoming.search;
 const headers = new Headers(request.headers);
 headers.delete('Host');
 headers.delete('Forwarded');
 headers.delete('X-Forwarded-Host');
 // Cloud Run remains responsible for authentication, authorization and CORS.
 const upstreamRequest = new Request(target, { method: request.method, headers, body: ['GET','HEAD'].includes(request.method) ? undefined : request.body, redirect: 'manual', duplex: 'half' });
 try {
  const response = await fetchUpstream(upstreamRequest);
  if (response.status === 101) return response;
  const responseHeaders = new Headers(response.headers);
  responseHeaders.set('Cache-Control', 'no-store');
  return new Response(response.body, { status: response.status, statusText: response.statusText, headers: responseHeaders });
 } catch {
  return Response.json({ error: { code: 'api_unavailable', message: 'The API could not be reached' } }, { status: 502, headers: { 'Cache-Control': 'no-store' } });
 }
}
export default { fetch(request, env) { return proxy(request, env); } };
