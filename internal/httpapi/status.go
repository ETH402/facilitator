package httpapi

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/ETH402/facilitator/internal/stats"
)

// statusPage is the human-readable view of the same snapshot /stats returns.
//
// It is self-contained: no external stylesheet, script, font, or image. VISION.md
// commits to operating without proprietary dependencies, and a status page that
// fetches assets from a third party fails during exactly the network problems it
// exists to report — besides telling that third party who is reading it.
//
// It renders whatever the snapshot contains, including omitted volume figures, so
// it cannot disclose more than the JSON does.
var statusPage = template.Must(template.New("status").Funcs(template.FuncMap{
	"badge": func(state string) string {
		switch state {
		case stats.StateOperational:
			return "ok"
		case stats.StateDegraded:
			return "warn"
		case stats.StateOutage:
			return "bad"
		default:
			return "off"
		}
	},
	"headline": func(status string) string {
		switch status {
		case stats.StateOperational:
			return "All systems operational"
		case stats.StateDegraded:
			return "Degraded performance"
		case stats.StateOutage:
			return "Service outage"
		default:
			return "Status unknown"
		}
	},
	"pretty": func(name string) string {
		return strings.ReplaceAll(strings.ReplaceAll(name, "_", " "), "rpc", "RPC")
	},
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>ETH402 status</title>
<style>
:root{color-scheme:light dark;--fg:#111;--muted:#666;--bg:#fff;--line:#e5e5e5;
--ok:#1a7f37;--warn:#9a6700;--bad:#cf222e;--off:#57606a}
@media(prefers-color-scheme:dark){:root{--fg:#e6e6e6;--muted:#9aa0a6;--bg:#0d1117;--line:#30363d;
--ok:#3fb950;--warn:#d29922;--bad:#f85149;--off:#8b949e}}
body{margin:0;padding:2.5rem 1.25rem;background:var(--bg);color:var(--fg);
font:16px/1.55 system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:44rem;margin:0 auto}
h1{font-size:1.5rem;margin:0 0 .25rem}
.sub{color:var(--muted);font-size:.9rem;margin:0 0 2rem}
ul{list-style:none;padding:0;margin:0 0 2rem;border-top:1px solid var(--line)}
li{display:flex;align-items:baseline;gap:.75rem;padding:.8rem .25rem;
border-bottom:1px solid var(--line)}
.name{flex:1;text-transform:capitalize}
.detail{color:var(--muted);font-size:.85rem}
.state{font-weight:600;font-size:.85rem;text-transform:uppercase;letter-spacing:.04em}
.ok{color:var(--ok)}.warn{color:var(--warn)}.bad{color:var(--bad)}.off{color:var(--off)}
dl{display:grid;grid-template-columns:auto 1fr;gap:.4rem 1.25rem;margin:0;font-size:.9rem}
dt{color:var(--muted)}dd{margin:0;font-variant-numeric:tabular-nums}
footer{margin-top:2.5rem;color:var(--muted);font-size:.8rem}
a{color:inherit}
</style></head><body><main>
<h1 class="{{badge .Status}}">{{headline .Status}}</h1>
<p class="sub">{{.Service}} &middot; {{.Network}} &middot; {{.Asset}} &middot; refreshed continuously</p>
<ul>
{{range .Components}}<li>
<span class="name">{{pretty .Name}}</span>
{{if .Detail}}<span class="detail">{{.Detail}}</span>{{end}}
<span class="state {{badge .State}}">{{.State}}</span>
</li>{{end}}
</ul>
<dl>
<dt>Uptime</dt><dd>{{.UptimeSeconds}}s</dd>
<dt>Confirmed settlements</dt><dd>{{.ConfirmedSettlements}}</dd>
<dt>Settlements (24h)</dt><dd>{{.SettlementsLast24h}}</dd>
<dt>Confirmation lag</dt><dd>{{.ConfirmationLagBlocks}} blocks</dd>
{{if .TotalPaymentVolumeUSDC}}<dt>Settled volume</dt><dd>{{.TotalPaymentVolumeUSDC}} USDC</dd>{{end}}
</dl>
<footer>Machine-readable: <a href="/stats">/stats</a> &middot; schema v{{.SchemaVersion}}</footer>
</main></body></html>
`))

// status serves the status page. It reads the same cached snapshot as /stats, so a
// public page cannot be used to drive database or RPC load from outside.
func (d Dependencies) status(w http.ResponseWriter, r *http.Request) {
	if d.Stats == nil {
		http.Error(w, "status is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	snapshot, err := d.Stats.Get(r.Context())
	if err != nil {
		// A failure here means the aggregates could not be read, which is itself an
		// outage. Saying so beats a blank page, and the detail stays in the log
		// because this endpoint is public.
		d.Logger.ErrorContext(r.Context(), "status page aggregation failed", "error", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = statusPage.Execute(w, stats.Response{
			SchemaVersion: stats.SchemaVersion, Service: "ETH402",
			Network: "eip155:1", Asset: "USDC", Status: stats.StateOutage,
			Components: []stats.Component{{
				Name: "database", State: stats.StateOutage, Detail: "unreachable",
			}},
		})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// No inline script or style source beyond this document's own <style>, and
	// nothing loaded from anywhere. Stated explicitly so a later edit that adds a
	// CDN link breaks visibly instead of silently.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'")
	w.Header().Set("Cache-Control", "public, max-age=10")
	if err := statusPage.Execute(w, snapshot); err != nil {
		d.Logger.ErrorContext(r.Context(), "status page render failed", "error", err)
	}
}
