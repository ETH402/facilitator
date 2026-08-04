package httpapi

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ETH402/facilitator/internal/merchant"
	"github.com/ETH402/facilitator/internal/stats"
)

const publicSiteCSP = "default-src 'none'; style-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

type publicSiteData struct {
	Snapshot       stats.Response
	StatsAvailable bool
	TopMerchants   []merchant.PublicMerchant
	Year           int
}

var publicSiteFuncs = template.FuncMap{
	"number": func(value any) string {
		var integer int64
		switch typed := value.(type) {
		case int:
			integer = int64(typed)
		case int64:
			integer = typed
		case uint64:
			integer = int64(typed)
		default:
			return "0"
		}
		raw := strconv.FormatInt(integer, 10)
		for i := len(raw) - 3; i > 0; i -= 3 {
			raw = raw[:i] + "," + raw[i:]
		}
		return raw
	},
	"initial": func(value string) string {
		value = strings.TrimSpace(value)
		if value == "" {
			return "M"
		}
		return strings.ToUpper(string([]rune(value)[0]))
	},
	"rank": func(index int) int { return index + 1 },
	"date": func(value *time.Time) string {
		if value == nil {
			return "Awaiting first settlement"
		}
		return "Last active " + value.UTC().Format("02 Jan 2006")
	},
}

var landingPage = template.Must(template.New("landing").Funcs(publicSiteFuncs).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="description" content="Open-source x402 v2 payment infrastructure for exact native USDC payments on Ethereum mainnet.">
<title>ETH402 — Payments for the machine economy</title><link rel="stylesheet" href="/assets/site.css"></head>
<body class="public-page">
<div class="ambient ambient-one"></div><div class="ambient ambient-two"></div>
<header class="site-header shell">
<a class="brand" href="/" aria-label="ETH402 home"><span class="brand-mark" aria-hidden="true"><i></i><b></b></span><span>ETH<span>402</span></span></a>
<nav class="site-nav" aria-label="Primary"><a class="active" href="/">Home</a><a href="/explore">Network</a><a href="/status">Status</a><a href="/merchant">Merchant panel</a></nav>
<a class="button button-small" href="/merchant">Get started <span aria-hidden="true">↗</span></a>
</header>
<main>
<section class="hero shell">
<div class="hero-copy">
<div class="eyebrow"><span class="live-dot"></span> Open payment infrastructure · Ethereum mainnet</div>
<h1>Payments for the<br><span>machine economy.</span></h1>
<p class="hero-lead">A focused, open-source x402 facilitator for AI agents and APIs. Verify signed payment authorizations and settle native USDC directly to merchants.</p>
<div class="hero-actions"><a class="button" href="/merchant">Start accepting payments <span>→</span></a><a class="button button-ghost" href="/explore">Explore the network</a></div>
<div class="trust-row"><span><i>✓</i> x402 v2</span><span><i>✓</i> Exact payments</span><span><i>✓</i> Native USDC</span><span><i>✓</i> Non-custodial</span></div>
</div>
<div class="hero-visual" aria-label="ETH402 transaction flow">
<div class="orbit orbit-one"></div><div class="orbit orbit-two"></div><div class="orbit orbit-three"></div>
<div class="core-mark"><div class="eth-gem"><span></span><i></i></div><strong>402</strong><small>PAYMENT REQUIRED</small></div>
<div class="float-card card-request"><span class="float-icon">↗</span><div><small>REQUEST</small><strong>HTTP 402</strong></div></div>
<div class="float-card card-settle"><span class="float-icon success">✓</span><div><small>SETTLED</small><strong>USDC · MAINNET</strong></div></div>
<div class="float-card card-agent"><span class="float-icon">&gt;_</span><div><small>CLIENT</small><strong>AI AGENT</strong></div></div>
</div>
</section>
<section class="metric-band"><div class="shell metric-grid">
<div><span class="metric-kicker">NETWORK</span><strong>Ethereum</strong><small>eip155:1</small></div>
<div><span class="metric-kicker">ACTIVE MERCHANTS</span><strong>{{number .Snapshot.VerifiedMerchants}}</strong><small>Wallet verified</small></div>
<div><span class="metric-kicker">VERIFICATIONS</span><strong>{{number .Snapshot.SuccessfulVerifications}}</strong><small>Protocol checks passed</small></div>
<div><span class="metric-kicker">SETTLEMENTS</span><strong>{{number .Snapshot.ConfirmedSettlements}}</strong><small>Confirmed onchain</small></div>
<div><span class="metric-kicker">ASSET</span><strong>USDC</strong><small>Native contract</small></div>
</div></section>
<section class="section shell">
<div class="section-heading"><div><span class="overline">BUILT FOR AUTONOMOUS COMMERCE</span><h2>HTTP-native payments.<br>Ethereum-grade settlement.</h2></div><p>One conservative lane, implemented deliberately. Buyers authorize. Merchants deliver. ETH402 verifies and settles without ever taking custody.</p></div>
<div class="feature-grid">
<article class="feature-card"><span class="feature-number">01</span><div class="feature-icon">402</div><h3>Standard HTTP flow</h3><p>Turn a Payment Required response into a signed x402 v2 authorization your API can verify.</p><span class="feature-tag">PROTOCOL NATIVE</span></article>
<article class="feature-card"><span class="feature-number">02</span><div class="feature-icon">◇</div><h3>Direct USDC settlement</h3><p>Native USDC moves from payer to merchant on Ethereum mainnet. ETH402 never holds merchant funds.</p><span class="feature-tag">NON-CUSTODIAL</span></article>
<article class="feature-card"><span class="feature-number">03</span><div class="feature-icon">⌁</div><h3>Agent-ready infrastructure</h3><p>Deterministic schemas, idempotent settlement, and exact integer arithmetic for automated clients.</p><span class="feature-tag">MACHINE READY</span></article>
</div>
</section>
<section class="section shell flow-section">
<div class="section-heading compact"><div><span class="overline">HOW IT MOVES</span><h2>From request to finality.</h2></div></div>
<div class="protocol-flow"><div><b>01</b><strong>Request</strong><small>API returns HTTP 402</small></div><span>→</span><div><b>02</b><strong>Authorize</strong><small>Buyer signs exact USDC</small></div><span>→</span><div><b>03</b><strong>Verify</strong><small>ETH402 checks every bound</small></div><span>→</span><div><b>04</b><strong>Settle</strong><small>Transfer confirms onchain</small></div></div>
</section>
<section class="section shell merchant-showcase">
<div class="section-heading compact"><div><span class="overline">NETWORK ACTIVITY</span><h2>Merchants building on ETH402.</h2></div><a class="text-link" href="/explore">View network stats →</a></div>
{{if .TopMerchants}}<div class="leaderboard-preview">{{range .TopMerchants}}<article class="merchant-row"><span class="merchant-avatar">{{initial .Name}}</span><div class="merchant-identity"><strong>{{.Name}}</strong><small>{{date .LastConfirmedAt}}</small></div><div class="merchant-count"><strong>{{number .ConfirmedSettlements}}</strong><small>confirmed</small></div>{{if .Website}}<a class="row-link" href="{{.Website}}" rel="nofollow noopener">↗</a>{{end}}</article>{{end}}</div>
{{else}}<div class="empty-showcase"><span class="empty-orbit"></span><div><h3>The network is opening up.</h3><p>Wallet-verified merchants can choose to appear here from their dashboard. Public profiles never expose payment amounts, email addresses, or wallets.</p></div><a class="button button-ghost" href="/merchant">Create a merchant</a></div>{{end}}
</section>
<section class="cta shell"><div><span class="overline">READY TO BUILD?</span><h2>Give your API a payment layer.</h2><p>Register a recipient, verify your wallet, and integrate the exact x402 endpoints.</p></div><a class="button button-light" href="/merchant">Open merchant panel <span>→</span></a></section>
</main>
<footer class="site-footer shell"><a class="brand brand-small" href="/"><span class="brand-mark" aria-hidden="true"><i></i><b></b></span><span>ETH<span>402</span></span></a><p>Open infrastructure for standardized payments on Ethereum mainnet.</p><div><a href="/supported">Capabilities</a><a href="/stats">JSON stats</a><a href="/status">Status</a></div><small>© {{.Year}} ETH402</small></footer>
</body></html>`))

var explorePage = template.Must(template.New("explore").Funcs(publicSiteFuncs).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="description" content="Live ETH402 network statistics and opted-in merchants."><title>ETH402 network</title><link rel="stylesheet" href="/assets/site.css"></head>
<body class="public-page inner-page"><div class="ambient ambient-one"></div>
<header class="site-header shell"><a class="brand" href="/"><span class="brand-mark" aria-hidden="true"><i></i><b></b></span><span>ETH<span>402</span></span></a><nav class="site-nav"><a href="/">Home</a><a class="active" href="/explore">Network</a><a href="/status">Status</a><a href="/merchant">Merchant panel</a></nav><a class="button button-small" href="/merchant">Get started ↗</a></header>
<main class="shell explore-main"><section class="page-intro"><span class="overline">LIVE NETWORK</span><h1>Infrastructure you can inspect.</h1><p>Public aggregate activity from the ETH402 facilitator and merchants who explicitly choose to be discoverable.</p><div class="status-pill"><span class="live-dot"></span>{{if .StatsAvailable}}{{.Snapshot.Status}}{{else}}temporarily unavailable{{end}}</div></section>
<section class="stats-grid"><article><small>REGISTERED MERCHANTS</small><strong>{{number .Snapshot.RegisteredMerchants}}</strong><span>All registrations</span></article><article><small>ACTIVE MERCHANTS</small><strong>{{number .Snapshot.VerifiedMerchants}}</strong><span>Email + wallet verified</span></article><article><small>SUCCESSFUL VERIFICATIONS</small><strong>{{number .Snapshot.SuccessfulVerifications}}</strong><span>Valid x402 authorizations</span></article><article><small>CONFIRMED SETTLEMENTS</small><strong>{{number .Snapshot.ConfirmedSettlements}}</strong><span>Ethereum finality reached</span></article><article><small>SETTLEMENTS · 24H</small><strong>{{number .Snapshot.SettlementsLast24h}}</strong><span>Rolling activity</span></article><article><small>CONFIRMATION LAG</small><strong>{{.Snapshot.ConfirmationLagBlocks}}</strong><span>Blocks behind chain head</span></article></section>
<section class="network-panel"><div class="panel-heading"><div><span class="overline">TOP MERCHANTS</span><h2>Public merchant activity</h2></div><span class="privacy-label">OPT-IN ONLY</span></div>
{{if .TopMerchants}}<div class="leaderboard"><div class="leaderboard-head"><span>RANK / MERCHANT</span><span>LAST ACTIVITY</span><span>CONFIRMED</span><span></span></div>{{range $index,$merchant := .TopMerchants}}<article><div class="rank"><b>#{{rank $index}}</b><span class="merchant-avatar">{{initial $merchant.Name}}</span><strong>{{$merchant.Name}}</strong></div><span>{{date $merchant.LastConfirmedAt}}</span><strong>{{number $merchant.ConfirmedSettlements}}</strong>{{if $merchant.Website}}<a class="row-link" href="{{$merchant.Website}}" rel="nofollow noopener">↗</a>{{else}}<span></span>{{end}}</article>{{end}}</div>
{{else}}<div class="empty-showcase network-empty"><span class="empty-orbit"></span><div><h3>No public profiles yet.</h3><p>Merchant analytics are private by default. A wallet-authenticated merchant must separately opt into this leaderboard before its name and confirmed-payment count appear.</p></div></div>{{end}}
</section><p class="privacy-note">Counts begin at each merchant’s public opt-in time and include only retained, attributable confirmed payments. No payment amounts, payer identities, recipient wallets, or emails are published. <a href="/stats">Machine-readable aggregate stats →</a></p></main>
<footer class="site-footer shell"><a class="brand brand-small" href="/"><span class="brand-mark"><i></i><b></b></span><span>ETH<span>402</span></span></a><p>Open infrastructure for standardized payments on Ethereum mainnet.</p><div><a href="/supported">Capabilities</a><a href="/stats">JSON stats</a><a href="/status">Status</a></div><small>© {{.Year}} ETH402</small></footer></body></html>`))

func (d Dependencies) publicSiteRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", d.landing)
	mux.HandleFunc("GET /explore", d.explore)
	mux.HandleFunc("GET /assets/site.css", d.siteCSS)
}

func (d Dependencies) publicData(r *http.Request, limit int) publicSiteData {
	data := publicSiteData{Year: time.Now().UTC().Year()}
	if d.Stats != nil {
		if snapshot, err := d.Stats.Get(r.Context()); err == nil {
			data.Snapshot, data.StatsAvailable = snapshot, true
		} else if d.Logger != nil {
			d.Logger.ErrorContext(r.Context(), "public site stats unavailable", "error", err)
		}
	}
	if d.Merchant != nil {
		if merchants, err := d.Merchant.PublicLeaderboard(r.Context(), limit); err == nil {
			data.TopMerchants = merchants
		} else if d.Logger != nil {
			d.Logger.ErrorContext(r.Context(), "public merchant leaderboard unavailable", "error", err)
		}
	}
	return data
}

func renderPublicPage(w http.ResponseWriter, page *template.Template, data publicSiteData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=10")
	w.Header().Set("Content-Security-Policy", publicSiteCSP)
	if err := page.Execute(w, data); err != nil {
		http.Error(w, "page temporarily unavailable", http.StatusServiceUnavailable)
	}
}

func (d Dependencies) landing(w http.ResponseWriter, r *http.Request) {
	renderPublicPage(w, landingPage, d.publicData(r, 3))
}

func (d Dependencies) explore(w http.ResponseWriter, r *http.Request) {
	renderPublicPage(w, explorePage, d.publicData(r, 50))
}

func (d Dependencies) siteCSS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	_, _ = w.Write([]byte(siteCSSContent))
}
