package httpapi

import (
	"bytes"
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
	Snapshot           stats.Response
	StatsAvailable     bool
	MerchantsAvailable bool
	TopMerchants       []merchant.PublicMerchant
	Year               int
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
	"statusClass": func(value string) string {
		switch value {
		case "operational":
			return "status-operational"
		case "degraded":
			return "status-degraded"
		case "outage":
			return "status-outage"
		default:
			return "status-unknown"
		}
	},
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
<body class="public-page"><a class="skip-link" href="#main-content">Skip to content</a>
<div class="ambient ambient-one"></div><div class="ambient ambient-two"></div>
<header class="site-header shell">
<a class="brand" href="/" aria-label="ETH402 home"><span class="brand-mark" aria-hidden="true"><i></i><b></b></span><span>ETH<span>402</span></span></a>
<nav class="site-nav" aria-label="Primary"><a class="active" aria-current="page" href="/">Home</a><a href="/explore">Network</a><a href="/status">Status</a><a href="/merchant">Merchant panel</a></nav>
<nav class="mobile-nav" aria-label="Primary"><a href="/explore">Network</a><a href="/status">Status</a><a href="/merchant">Merchant</a></nav>
<a class="button button-small" href="/merchant">Get started <span aria-hidden="true">↗</span></a>
</header>
<main id="main-content">
<section class="hero shell">
<div class="hero-copy">
<div class="eyebrow {{if .StatsAvailable}}{{statusClass .Snapshot.Status}}{{else}}status-unknown{{end}}"><span class="live-dot"></span> Ethereum mainnet · {{if .StatsAvailable}}{{.Snapshot.Status}}{{else}}status unavailable{{end}}</div>
<h1>The payment layer<br><span>for machine commerce.</span></h1>
<p class="hero-lead">Open-source x402 infrastructure for AI agents and APIs. Verify exact payment authorizations and settle native USDC directly from buyers to merchants.</p>
<div class="hero-actions"><a class="button" href="/merchant">Start accepting payments <span>→</span></a><a class="button button-ghost" href="https://github.com/ETH402/facilitator" rel="noopener">View source <span aria-hidden="true">↗</span></a></div>
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
<div><span class="metric-kicker">ACTIVE MERCHANTS</span><strong>{{if .StatsAvailable}}{{number .Snapshot.VerifiedMerchants}}{{else}}—{{end}}</strong><small>{{if .StatsAvailable}}Wallet verified{{else}}Stats unavailable{{end}}</small></div>
<div><span class="metric-kicker">VERIFICATIONS</span><strong>{{if .StatsAvailable}}{{number .Snapshot.SuccessfulVerifications}}{{else}}—{{end}}</strong><small>{{if .StatsAvailable}}Protocol checks passed{{else}}Stats unavailable{{end}}</small></div>
<div><span class="metric-kicker">SETTLEMENTS</span><strong>{{if .StatsAvailable}}{{number .Snapshot.ConfirmedSettlements}}{{else}}—{{end}}</strong><small>{{if .StatsAvailable}}Confirmed onchain{{else}}Stats unavailable{{end}}</small></div>
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
<section class="section shell open-section">
<div class="open-copy"><span class="overline">OPEN BY DESIGN</span><h2>Trust the constraints.<br>Inspect the code.</h2><p>ETH402 takes one deliberately narrow payment lane and makes every boundary visible: x402 v2, exact payments, Ethereum mainnet, and native USDC. No custody, opaque routing, or proprietary client is required.</p><div class="proof-list"><span>Apache-2.0 licensed</span><span>Reproducible releases</span><span>Signed, attested images</span></div><div class="open-actions"><a class="button button-ghost" href="https://github.com/ETH402/facilitator" rel="noopener">Explore on GitHub <span aria-hidden="true">↗</span></a><a class="text-link" href="https://github.com/ETH402/facilitator/blob/main/docs/INTEGRATION.md" rel="noopener">Read integration guide →</a></div></div>
<div class="capability-window" aria-label="ETH402 supported payment capability"><div class="window-bar"><span></span><span></span><span></span><b>GET /supported</b></div><pre><code><i>200 OK</i>
{
  <b>"x402Version"</b>: 2,
  <b>"scheme"</b>: "exact",
  <b>"network"</b>: "eip155:1",
  <b>"extra"</b>: {
    <b>"asset"</b>: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
    <b>"assetTransferMethod"</b>: "eip3009"
  }
}</code></pre><div class="window-foot"><span><i></i> PRODUCTION API</span><span>INTEGER MONEY · IDEMPOTENT SETTLEMENT</span></div></div>
</section>
<section class="section shell merchant-showcase">
<div class="section-heading compact"><div><span class="overline">NETWORK ACTIVITY</span><h2>Merchants building on ETH402.</h2></div><a class="text-link" href="/explore">View network stats →</a></div>
{{if not .MerchantsAvailable}}<div class="empty-showcase"><span class="empty-orbit unavailable"></span><div><h3>Merchant directory temporarily unavailable.</h3><p>The payment API remains separate from this public directory. Refresh shortly for opted-in merchant activity.</p></div><a class="button button-ghost" href="/status">View status</a></div>
{{else if .TopMerchants}}<div class="leaderboard-preview">{{range .TopMerchants}}<article class="merchant-row"><span class="merchant-avatar">{{initial .Name}}</span><div class="merchant-identity"><strong>{{.Name}}</strong><small>{{date .LastConfirmedAt}}</small></div><div class="merchant-count"><strong>{{number .ConfirmedSettlements}}</strong><small>confirmed</small></div>{{if .Website}}<a class="row-link" href="{{.Website}}" rel="nofollow noopener" aria-label="Visit {{.Name}} website">↗</a>{{end}}</article>{{end}}</div>
{{else}}<div class="empty-showcase"><span class="empty-orbit"></span><div><h3>The network is opening up.</h3><p>Wallet-verified merchants can choose to appear here from their dashboard. Public profiles never expose payment amounts, email addresses, or wallets.</p></div><a class="button button-ghost" href="/merchant">Create a merchant</a></div>{{end}}
</section>
<section class="cta shell"><div><span class="overline">READY TO BUILD?</span><h2>Give your API a payment layer.</h2><p>Register a recipient, verify your wallet, and integrate the exact x402 endpoints.</p></div><a class="button button-light" href="/merchant">Open merchant panel <span>→</span></a></section>
</main>
<footer class="site-footer shell"><a class="brand brand-small" href="/"><span class="brand-mark" aria-hidden="true"><i></i><b></b></span><span>ETH<span>402</span></span></a><p>Open infrastructure for standardized payments on Ethereum mainnet.</p><div><a href="https://github.com/ETH402/facilitator" rel="noopener">GitHub</a><a href="/supported">Capabilities</a><a href="/stats">JSON stats</a><a href="/status">Status</a></div><small>© {{.Year}} ETH402</small></footer>
</body></html>`))

var explorePage = template.Must(template.New("explore").Funcs(publicSiteFuncs).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="description" content="Live ETH402 network statistics and opted-in merchants."><title>ETH402 network</title><link rel="stylesheet" href="/assets/site.css"></head>
<body class="public-page inner-page"><a class="skip-link" href="#main-content">Skip to content</a><div class="ambient ambient-one"></div>
<header class="site-header shell"><a class="brand" href="/"><span class="brand-mark" aria-hidden="true"><i></i><b></b></span><span>ETH<span>402</span></span></a><nav class="site-nav" aria-label="Primary"><a href="/">Home</a><a class="active" aria-current="page" href="/explore">Network</a><a href="/status">Status</a><a href="/merchant">Merchant panel</a></nav><nav class="mobile-nav" aria-label="Primary"><a href="/">Home</a><a href="/status">Status</a><a href="/merchant">Merchant</a></nav><a class="button button-small" href="/merchant">Get started ↗</a></header>
<main id="main-content" class="shell explore-main"><section class="page-intro"><span class="overline">LIVE NETWORK</span><h1>Infrastructure you can inspect.</h1><p>Public aggregate activity from the ETH402 facilitator and merchants who explicitly choose to be discoverable.</p><div class="status-pill {{if .StatsAvailable}}{{statusClass .Snapshot.Status}}{{else}}status-unknown{{end}}"><span class="live-dot"></span>{{if .StatsAvailable}}{{.Snapshot.Status}}{{else}}temporarily unavailable{{end}}</div></section>
<section class="stats-grid"><article><small>REGISTERED MERCHANTS</small><strong>{{if .StatsAvailable}}{{number .Snapshot.RegisteredMerchants}}{{else}}—{{end}}</strong><span>{{if .StatsAvailable}}All registrations{{else}}Stats unavailable{{end}}</span></article><article><small>ACTIVE MERCHANTS</small><strong>{{if .StatsAvailable}}{{number .Snapshot.VerifiedMerchants}}{{else}}—{{end}}</strong><span>{{if .StatsAvailable}}Email + wallet verified{{else}}Stats unavailable{{end}}</span></article><article><small>SUCCESSFUL VERIFICATIONS</small><strong>{{if .StatsAvailable}}{{number .Snapshot.SuccessfulVerifications}}{{else}}—{{end}}</strong><span>{{if .StatsAvailable}}Valid x402 authorizations{{else}}Stats unavailable{{end}}</span></article><article><small>CONFIRMED SETTLEMENTS</small><strong>{{if .StatsAvailable}}{{number .Snapshot.ConfirmedSettlements}}{{else}}—{{end}}</strong><span>{{if .StatsAvailable}}Ethereum finality reached{{else}}Stats unavailable{{end}}</span></article><article><small>SETTLEMENTS · 24H</small><strong>{{if .StatsAvailable}}{{number .Snapshot.SettlementsLast24h}}{{else}}—{{end}}</strong><span>{{if .StatsAvailable}}Rolling activity{{else}}Stats unavailable{{end}}</span></article><article><small>CONFIRMATION LAG</small><strong>{{if .StatsAvailable}}{{.Snapshot.ConfirmationLagBlocks}}{{else}}—{{end}}</strong><span>{{if .StatsAvailable}}Blocks behind chain head{{else}}Stats unavailable{{end}}</span></article></section>
<section class="network-panel"><div class="panel-heading"><div><span class="overline">TOP MERCHANTS</span><h2>Public merchant activity</h2></div><span class="privacy-label">OPT-IN ONLY</span></div>
{{if not .MerchantsAvailable}}<div class="empty-showcase network-empty"><span class="empty-orbit unavailable"></span><div><h3>Merchant directory temporarily unavailable.</h3><p>The public directory could not be loaded. This does not imply that payment verification or settlement is unavailable.</p></div></div>
{{else if .TopMerchants}}<div class="leaderboard"><div class="leaderboard-head"><span>RANK / MERCHANT</span><span>LAST ACTIVITY</span><span>CONFIRMED</span><span></span></div>{{range $index,$merchant := .TopMerchants}}<article><div class="rank"><b>#{{rank $index}}</b><span class="merchant-avatar">{{initial $merchant.Name}}</span><strong>{{$merchant.Name}}</strong></div><span>{{date $merchant.LastConfirmedAt}}</span><strong>{{number $merchant.ConfirmedSettlements}}</strong>{{if $merchant.Website}}<a class="row-link" href="{{$merchant.Website}}" rel="nofollow noopener" aria-label="Visit {{$merchant.Name}} website">↗</a>{{else}}<span></span>{{end}}</article>{{end}}</div>
{{else}}<div class="empty-showcase network-empty"><span class="empty-orbit"></span><div><h3>No public profiles yet.</h3><p>Merchant analytics are private by default. A wallet-authenticated merchant must separately opt into this leaderboard before its name and confirmed-payment count appear.</p></div></div>{{end}}
</section><p class="privacy-note">Counts begin at each merchant’s public opt-in time and include only retained, attributable confirmed payments. No payment amounts, payer identities, recipient wallets, or emails are published. <a href="/stats">Machine-readable aggregate stats →</a></p></main>
<footer class="site-footer shell"><a class="brand brand-small" href="/"><span class="brand-mark" aria-hidden="true"><i></i><b></b></span><span>ETH<span>402</span></span></a><p>Open infrastructure for standardized payments on Ethereum mainnet.</p><div><a href="https://github.com/ETH402/facilitator" rel="noopener">GitHub</a><a href="/supported">Capabilities</a><a href="/stats">JSON stats</a><a href="/status">Status</a></div><small>© {{.Year}} ETH402</small></footer></body></html>`))

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
			data.TopMerchants, data.MerchantsAvailable = merchants, true
		} else if d.Logger != nil {
			d.Logger.ErrorContext(r.Context(), "public merchant leaderboard unavailable", "error", err)
		}
	}
	return data
}

func renderPublicPage(w http.ResponseWriter, page *template.Template, data publicSiteData) {
	var body bytes.Buffer
	if err := page.Execute(&body, data); err != nil {
		http.Error(w, "page temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=10")
	w.Header().Set("Content-Security-Policy", publicSiteCSP)
	_, _ = w.Write(body.Bytes())
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
