package httpapi

import "net/http"

const merchantPanelHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>ETH402 merchant</title>
<style>
:root{color-scheme:light dark;--bg:#f6f7f9;--card:#fff;--fg:#14171a;--muted:#667085;--line:#e4e7ec;--brand:#3157d5;--ok:#147a42;--bad:#c93434}
@media(prefers-color-scheme:dark){:root{--bg:#0b0e14;--card:#121722;--fg:#eef2f6;--muted:#98a2b3;--line:#29313d;--brand:#88a3ff;--ok:#55d58b;--bad:#ff7b7b}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}
header,main{max-width:70rem;margin:auto;padding:1.25rem}header{display:flex;justify-content:space-between;align-items:center}header strong{letter-spacing:.04em}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(18rem,1fr));gap:1rem}.card{background:var(--card);border:1px solid var(--line);border-radius:.8rem;padding:1.2rem;box-shadow:0 1px 2px #0001}
h1{font-size:1.5rem;margin:.2rem 0}h2{font-size:1.05rem;margin:0 0 .8rem}p{color:var(--muted)}label{display:block;margin:.7rem 0 .25rem;font-weight:600}
input,textarea,button{font:inherit}input,textarea{width:100%;padding:.65rem;border:1px solid var(--line);border-radius:.45rem;background:var(--bg);color:var(--fg)}
button{border:0;border-radius:.45rem;padding:.65rem .9rem;background:var(--brand);color:#fff;font-weight:650;cursor:pointer}button.secondary{background:transparent;color:var(--fg);border:1px solid var(--line)}button.danger{background:transparent;color:var(--bad);border:1px solid var(--line)}button:disabled{opacity:.55;cursor:wait}
.row{display:flex;gap:.6rem;align-items:center;flex-wrap:wrap}.spread{justify-content:space-between}.muted{color:var(--muted)}.ok{color:var(--ok)}.bad{color:var(--bad)}.hidden{display:none!important}
code{overflow-wrap:anywhere}.secret{display:block;padding:.8rem;background:var(--bg);border:1px solid var(--line);border-radius:.4rem;margin:.7rem 0}.metric{font-size:1.5rem;font-weight:750}.metric-label{font-size:.78rem;color:var(--muted)}
ul{list-style:none;padding:0;margin:0}li{padding:.65rem 0;border-top:1px solid var(--line)}small{color:var(--muted)}#notice{min-height:1.5rem;margin:.25rem 0 1rem}
</style></head><body>
<header><strong>ETH402</strong><div class="row"><a href="/status" class="muted">Status</a><button id="logout" class="secondary hidden">Sign out</button></div></header>
<main>
<section id="welcome"><h1>Merchant administration</h1><p>Verify your business recipient, manage integration keys, and optionally view private payment statistics.</p>
<div id="notice" aria-live="polite"></div><div class="grid">
<form id="signin" class="card"><h2>Sign in</h2><p>Receive a one-time link at your registered business email.</p><label for="signin-email">Business email</label><input id="signin-email" type="email" maxlength="320" required><p><button type="submit">Email me a sign-in link</button></p></form>
<form id="register" class="card"><h2>Create merchant</h2><label for="name">Business name</label><input id="name" maxlength="200" required><label for="email">Business email</label><input id="email" type="email" maxlength="320" required><label for="recipient">Ethereum recipient</label><input id="recipient" pattern="0x[0-9a-fA-F]{40}" required><label for="website">Website</label><input id="website" type="url"><label class="row"><input id="terms" type="checkbox" required style="width:auto"> I accept the current ETH402 terms.</label><p><button type="submit">Create and verify</button></p></form>
</div></section>
<section id="account" class="hidden"><div id="notice-auth" aria-live="polite"></div><div class="card"><div class="row spread"><div><h1 id="merchant-name"></h1><p id="merchant-meta"></p></div><strong id="merchant-status"></strong></div></div>
<div id="verification" class="card hidden" style="margin-top:1rem"><h2>Verify recipient wallet</h2><p>Connect the wallet registered as <code id="wallet-address"></code>. You will sign a human-readable SIWE message; this does not send a transaction or spend funds.</p><button id="verify-wallet">Connect and verify</button></div>
<div id="key-once" class="card hidden" style="margin-top:1rem"><h2>Save your API key now</h2><p>This value is shown once. Store it in a secret manager; do not put it in browser storage, source code, or chat.</p><code id="new-secret" class="secret"></code><div class="row"><button id="copy-secret">Copy</button><button id="download-secret" class="secondary">Download</button><button id="dismiss-secret" class="secondary">I saved it</button></div></div>
<div id="dashboard" class="grid hidden" style="margin-top:1rem">
<section class="card"><div class="row spread"><h2>Private statistics</h2><button id="stats-toggle" class="secondary"></button></div><p>Off by default. When enabled, this panel aggregates only your payments from the opt-in time, within the configured retention window. No third-party analytics or tracking scripts are used.</p><div id="stats-empty" class="muted"></div><div id="stats-values" class="grid hidden"><div><div id="stat-verified" class="metric"></div><div class="metric-label">Verified payments</div></div><div><div id="stat-confirmed" class="metric"></div><div class="metric-label">Confirmed</div></div><div><div id="stat-pending" class="metric"></div><div class="metric-label">Pending</div></div><div><div id="stat-volume" class="metric"></div><div class="metric-label">USDC confirmed</div></div></div><small id="stats-since"></small></section>
<section class="card"><h2>Integration keys</h2><form id="new-key" class="row"><input id="key-name" maxlength="100" placeholder="Key name" required style="flex:1"><button type="submit">Create</button></form><ul id="keys"></ul></section>
</div></section>
</main><script src="/merchant/app.js" defer></script></body></html>`

const merchantPanelScript = `'use strict';
const $=id=>document.getElementById(id); let merchant=null,walletAuthenticated=false,latestSecret='';
function notice(message,bad=false,auth=false){const el=$(auth?'notice-auth':'notice');el.textContent=message;el.className=bad?'bad':'ok'}
async function api(path,options={}){options.headers={...(options.headers||{}),'Content-Type':'application/json'};const response=await fetch(path,options);if(response.status===204)return null;let body={};try{body=await response.json()}catch{}if(!response.ok){const e=new Error(body.error?.message||'Request failed');e.status=response.status;e.code=body.error?.code;throw e}return body}
function showSignedOut(){merchant=null;$('welcome').classList.remove('hidden');$('account').classList.add('hidden');$('logout').classList.add('hidden')}
async function load(){try{const data=await api('/merchant/api/session');merchant=data.merchant;walletAuthenticated=data.wallet_authenticated;$('welcome').classList.add('hidden');$('account').classList.remove('hidden');$('logout').classList.remove('hidden');$('merchant-name').textContent=merchant.name;$('merchant-meta').textContent=merchant.business_email+' · '+merchant.recipient_address;$('merchant-status').textContent=merchant.status;$('wallet-address').textContent=merchant.recipient_address;const needsWallet=(merchant.status==='pending'&&!merchant.wallet_verified_at)||(merchant.status==='active'&&!walletAuthenticated);$('verification').classList.toggle('hidden',!needsWallet);$('dashboard').classList.toggle('hidden',merchant.status!=='active'||!walletAuthenticated);if(merchant.status==='active'&&walletAuthenticated){await Promise.all([loadKeys(),loadStats()])}}catch(e){if(e.status===401)showSignedOut();else notice(e.message,true,true)}}
$('signin').addEventListener('submit',async e=>{e.preventDefault();try{await api('/v1/merchants/admin-link',{method:'POST',body:JSON.stringify({business_email:$('signin-email').value})});notice('If that address is registered, a one-time sign-in link has been sent.')}catch(err){notice(err.message,true)}});
$('register').addEventListener('submit',async e=>{e.preventDefault();try{await api('/v1/merchants/register',{method:'POST',body:JSON.stringify({name:$('name').value,business_email:$('email').value,recipient_address:$('recipient').value,website:$('website').value,description:'',accept_terms:$('terms').checked})});notice('Check your email and use the newest verification link.')}catch(err){notice(err.message,true)}});
$('logout').addEventListener('click',async()=>{await api('/merchant/api/logout',{method:'POST'}).catch(()=>{});showSignedOut()});
function utf8Hex(value){return '0x'+Array.from(new TextEncoder().encode(value),b=>b.toString(16).padStart(2,'0')).join('')}
$('verify-wallet').addEventListener('click',async()=>{const button=$('verify-wallet');button.disabled=true;try{if(!window.ethereum)throw new Error('No compatible Ethereum wallet was found in this browser.');const challenge=await api('/merchant/api/wallet-challenge',{method:'POST',body:'{}'});const accounts=await window.ethereum.request({method:'eth_requestAccounts'});const account=accounts[0]||'';if(account.toLowerCase()!==challenge.address.toLowerCase())throw new Error('Connected wallet does not match the registered recipient.');const signature=await window.ethereum.request({method:'personal_sign',params:[utf8Hex(challenge.message),account]});const activated=await api('/merchant/api/verify-wallet',{method:'POST',body:JSON.stringify({challenge_id:challenge.id,message:challenge.message,signature})});if(activated.api_key){latestSecret=activated.api_key;$('new-secret').textContent=latestSecret;$('key-once').classList.remove('hidden');notice('Recipient verified. Save the API key before leaving this page.',false,true)}else{notice('Wallet authentication complete.',false,true)}await load()}catch(err){notice(err.message,true,true)}finally{button.disabled=false}});
async function loadKeys(){const data=await api('/merchant/api/api-keys');const list=$('keys');list.replaceChildren();for(const key of data.keys){const li=document.createElement('li'),row=document.createElement('div'),label=document.createElement('span'),button=document.createElement('button');row.className='row spread';label.textContent=key.name+' · '+key.prefix+(key.revoked_at?' · revoked':'');button.textContent='Revoke';button.className='danger';button.disabled=!!key.revoked_at;button.addEventListener('click',async()=>{if(!confirm('Revoke this API key?'))return;await api('/merchant/api/api-keys/'+encodeURIComponent(key.id),{method:'DELETE'});await loadKeys()});row.append(label,button);li.append(row);list.append(li)}}
$('new-key').addEventListener('submit',async e=>{e.preventDefault();try{const data=await api('/merchant/api/api-keys',{method:'POST',body:JSON.stringify({name:$('key-name').value})});latestSecret=data.api_key;$('new-secret').textContent=latestSecret;$('key-once').classList.remove('hidden');$('key-name').value='';await loadKeys()}catch(err){notice(err.message,true,true)}});
async function loadStats(){const enabled=!!merchant.stats_opted_in_at;$('stats-toggle').textContent=enabled?'Disable':'Enable';$('stats-values').classList.toggle('hidden',!enabled);$('stats-empty').textContent=enabled?'':'Statistics are disabled.';if(!enabled)return;try{const s=await api('/merchant/api/stats');$('stat-verified').textContent=s.verified_payments;$('stat-confirmed').textContent=s.confirmed_settlements;$('stat-pending').textContent=s.pending_settlements;$('stat-volume').textContent=s.confirmed_volume_usdc;$('stats-since').textContent='Observed since '+new Date(s.observed_since).toLocaleString()}catch(err){notice(err.message,true,true)}}
$('stats-toggle').addEventListener('click',async()=>{const enabled=!merchant.stats_opted_in_at;try{await api('/merchant/api/stats-consent',{method:'PUT',body:JSON.stringify({enabled})});await load()}catch(err){notice(err.message,true,true)}});
$('copy-secret').addEventListener('click',()=>navigator.clipboard.writeText(latestSecret));
$('download-secret').addEventListener('click',()=>{const blob=new Blob([latestSecret+'\n'],{type:'text/plain'}),a=document.createElement('a');a.href=URL.createObjectURL(blob);a.download='eth402-api-key.txt';a.click();URL.revokeObjectURL(a.href)});
$('dismiss-secret').addEventListener('click',()=>{latestSecret='';$('new-secret').textContent='';$('key-once').classList.add('hidden')});
load();`

func (d Dependencies) merchantPanel(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	_, _ = w.Write([]byte(merchantPanelHTML))
}

func (d Dependencies) merchantPanelJS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	_, _ = w.Write([]byte(merchantPanelScript))
}
