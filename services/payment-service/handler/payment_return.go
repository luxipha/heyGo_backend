package handler

import "net/http"

// PaymentReturn is a static checkout handoff, never evidence of payment success.
func PaymentReturn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Return to HeyGo</title><style>body{margin:0;background:#f5f9f6;color:#172033;font:17px system-ui,sans-serif;display:grid;min-height:100svh;place-items:center}main{margin:24px;padding:32px;max-width:380px;background:white;border:1px solid #dfe8e2;border-radius:20px}h1{font-size:26px}p{line-height:1.6;color:#526171}.brand{color:#009b72;font-weight:750}</style></head><body><main><div class="brand">HeyGo</div><h1>Return to HeyGo</h1><p>You can close this page and reopen the HeyGo app to check your payment.</p><p>Your payment status will update after verification.</p></main></body></html>`))
}
