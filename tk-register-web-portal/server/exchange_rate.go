package main

import "net/http"

// The documentation UI reads the same rate as account responses. This exposes
// one non-secret value to signed-in users, not the administrator settings list.
func (a *app) exchangeRate(w http.ResponseWriter, r *http.Request) {
	if _, _, valid := a.authenticated(w, r, false); !valid {
		return
	}
	rate, err := a.store.ExchangeRate(r.Context())
	if err != nil {
		internal(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	ok(w, map[string]any{"exchange_rate_cny_per_usd": rate})
}
