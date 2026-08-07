package httpapi

import "net/http"

const merchantPanelHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="description" content="ETH402 merchant administration for x402 payments on Ethereum mainnet.">
<title>ETH402 merchant panel</title><link rel="stylesheet" href="/assets/site.css"></head>
<body class="app-shell"><a class="skip-link" href="#main-content">Skip to content</a>
<header class="app-header"><a class="brand" href="/"><span class="brand-mark" aria-hidden="true"><i></i><b></b></span><span>ETH<span>402</span></span></a><div class="app-header-actions"><a class="button button-small button-secondary" href="/explore">Network</a><button id="logout" class="button button-small button-secondary hidden">Sign out</button></div></header>
<main id="main-content" class="app-main">
<section id="welcome">
<div class="auth-intro"><span class="overline">MERCHANT CONSOLE</span><h1>Accept machine payments.<br>Stay in control.</h1><p>Route native USDC directly to your wallet, manage integration credentials, and understand payment activity from one secure workspace.</p><div class="auth-trust" aria-label="Merchant security model"><span><i>01</i>Email verified</span><span><i>02</i>Wallet authorized</span><span><i>03</i>Direct settlement</span></div></div>
<div id="notice" class="notice" aria-live="polite"></div>
<div class="auth-grid">
<form id="signin" class="app-card auth-card"><span class="overline">WELCOME BACK</span><h2>Sign in securely</h2><p>We’ll send a one-time link to your registered business email. Your recipient wallet unlocks sensitive actions.</p><div class="field"><label for="signin-email">Business email</label><input id="signin-email" type="email" maxlength="320" autocomplete="email" placeholder="you@company.com" required></div><button class="button form-button" type="submit">Send secure sign-in link <span>→</span></button><div class="auth-assurance"><strong>No password to remember.</strong><span>Links expire after one use. API keys and analytics stay protected behind a fresh wallet signature.</span></div></form>
<form id="register" class="app-card auth-card featured"><span class="overline">NEW MERCHANT</span><h2>Start accepting x402</h2><p>Create your merchant identity and route native USDC directly to your Ethereum recipient.</p><div class="dash-grid"><div class="field"><label for="name">Business name</label><input id="name" maxlength="200" autocomplete="organization" placeholder="Acme API" required></div><div class="field"><label for="email">Business email</label><input id="email" type="email" maxlength="320" autocomplete="email" placeholder="payments@acme.dev" required></div></div><div class="field"><label for="recipient">Ethereum recipient <span class="field-help">MAINNET</span></label><input id="recipient" class="code" pattern="0x[0-9a-fA-F]{40}" placeholder="0x…" aria-describedby="recipient-help" required><small id="recipient-help" class="field-help">You can replace this address before wallet activation.</small></div><div class="field"><label for="website">Website <span class="field-help">OPTIONAL</span></label><input id="website" type="url" inputmode="url" autocomplete="url" placeholder="https://example.com" aria-describedby="website-help"><small id="website-help" class="field-help">Only HTTPS websites can be linked from the public directory.</small></div><label class="check-row"><input id="terms" type="checkbox" required><span>I accept the current ETH402 terms and understand the facilitator supports only x402 v2 exact native-USDC payments on Ethereum mainnet.</span></label><button class="button form-button" type="submit">Create merchant <span>→</span></button></form>
</div></section>

<section id="account" class="hidden">
<div id="notice-auth" class="notice" aria-live="polite"></div>
<div class="dashboard-layout">
<aside class="dash-sidebar"><div class="merchant-summary"><strong id="merchant-name"></strong><small id="merchant-email"></small><span id="merchant-status" class="status-chip"></span></div><nav class="dash-nav" aria-label="Merchant views" role="tablist"><button id="tab-overview" class="active" data-view="overview" role="tab" aria-controls="panel-overview" aria-selected="true">Overview</button><button id="tab-analytics" data-view="analytics" role="tab" aria-controls="panel-analytics" aria-selected="false" tabindex="-1">Analytics</button><button id="tab-keys" data-view="keys" role="tab" aria-controls="panel-keys" aria-selected="false" tabindex="-1">API keys</button><button id="tab-settings" data-view="settings" role="tab" aria-controls="panel-settings" aria-selected="false" tabindex="-1">Settings</button></nav></aside>
<div class="dash-content"><div class="dash-top"><div><span class="overline">MERCHANT PANEL</span><h1 id="view-title">Overview</h1><p id="merchant-meta"></p></div><span id="wallet-badge" class="wallet-badge">WALLET REQUIRED</span></div>

<section id="verification" class="verification-banner hidden"><div><span class="overline">SECURITY CHECK</span><h2 id="verification-title">Verify the recipient wallet</h2><p id="verification-copy">Connect the wallet that should receive USDC and sign a human-readable message. If it differs from <code id="wallet-address"></code>, you can replace the pending recipient before activation. No transaction, gas, or funds are involved.</p></div><button id="verify-wallet" class="button"><span id="verify-wallet-label">Connect or replace wallet</span> <span aria-hidden="true">→</span></button></section>
<section id="account-state" class="app-card hidden"><span class="overline">ACCOUNT STATUS</span><h2 id="account-state-title"></h2><p id="account-state-copy"></p></section>
<section id="key-once" class="app-card secret-card hidden"><span class="overline">DISPLAYED ONCE</span><h2>Save your new API key</h2><p>Store it in a secret manager. Never put it in source code, browser storage, logs, or chat.</p><code id="new-secret" class="secret-value"></code><div class="row-actions"><button id="copy-secret" class="button button-small">Copy key</button><button id="download-secret" class="button button-small button-secondary">Download</button><button id="dismiss-secret" class="button button-small button-secondary">I saved it</button></div></section>

<div id="dashboard" class="hidden">
<section id="panel-overview" class="view active" data-panel="overview" role="tabpanel" aria-labelledby="tab-overview"><div class="dash-grid three"><article class="metric-card"><small>VERIFIED PAYMENTS</small><strong id="overview-verified">—</strong><span>Since analytics opt-in</span></article><article class="metric-card"><small>CONFIRMED</small><strong id="overview-confirmed">—</strong><span>Onchain settlements</span></article><article class="metric-card"><small>CONFIRMED VOLUME</small><strong id="overview-volume">—</strong><span>USDC · private</span></article><article class="app-card wide-card"><span class="overline">INTEGRATION STATUS</span><h2>Ready for machine payments.</h2><p>Your recipient is wallet verified. Use an active API key to authenticate merchant-scoped integration calls; x402 verification and settlement remain separate protocol endpoints.</p><div class="setting-row"><div class="setting-copy"><h3>Recipient</h3><p id="overview-recipient" class="code"></p></div><span class="status-chip">ETHEREUM MAINNET</span></div><div class="setting-row"><div class="setting-copy"><h3>Public discovery</h3><p id="overview-public">Your merchant profile is private.</p></div><button class="button button-small button-secondary" data-go="settings">Manage visibility</button></div></article></div></section>

<section id="panel-analytics" class="view" data-panel="analytics" role="tabpanel" aria-labelledby="tab-analytics"><div class="app-card"><div class="setting-row"><div class="setting-copy"><span class="overline">PRIVATE ANALYTICS</span><h2>Payment activity</h2><p>Disabled by default. Counts and volume begin at opt-in and stay visible only to a wallet-authenticated session.</p></div><button id="stats-toggle" class="toggle" type="button" role="switch" aria-label="Private analytics"></button></div><div id="stats-empty" class="muted">Enable private analytics to begin a new observation window.</div><div id="stats-values" class="dash-grid three hidden"><article class="metric-card"><small>VERIFIED</small><strong id="stat-verified">0</strong><span>Valid authorizations</span></article><article class="metric-card"><small>PENDING</small><strong id="stat-pending">0</strong><span>Awaiting finality</span></article><article class="metric-card"><small>CONFIRMED</small><strong id="stat-confirmed">0</strong><span>Settled onchain</span></article><article class="metric-card"><small>FAILED</small><strong id="stat-failed">0</strong><span>Terminal failures</span></article><article class="metric-card"><small>VOLUME</small><strong id="stat-volume">0.000000</strong><span>USDC confirmed</span></article><article class="metric-card"><small>OBSERVED SINCE</small><strong id="stats-since">—</strong><span>Retention-bounded window</span></article></div></div></section>

<section id="panel-keys" class="view" data-panel="keys" role="tabpanel" aria-labelledby="tab-keys"><div class="app-card"><span class="overline">INTEGRATION CREDENTIALS</span><h2>API keys</h2><p>Create separate keys per environment or integration. New secrets are displayed once; revoked keys stop working immediately.</p><form id="new-key" class="key-form"><label class="sr-only" for="key-name">API key name</label><input id="key-name" maxlength="100" autocomplete="off" placeholder="Production API" required><button class="button" type="submit">Create key</button></form><p id="keys-empty" class="muted hidden">No API keys yet. Create one for your first integration.</p><ul id="keys" class="key-list"></ul></div></section>

<section id="panel-settings" class="view" data-panel="settings" role="tabpanel" aria-labelledby="tab-settings"><div class="app-card"><span class="overline">PRIVACY & DISCOVERY</span><h2>Merchant settings</h2><p>Public discovery and private analytics are independent. Enabling one never enables the other.</p><div class="setting-row"><div class="setting-copy"><h3>Private payment analytics</h3><p>Allow this dashboard to aggregate your retained payment records from the moment you opt in. Nothing is published.</p></div><button id="settings-stats-toggle" class="toggle" type="button" role="switch" aria-label="Private payment analytics"></button></div><div class="setting-row"><div class="setting-copy"><h3>Public merchant profile</h3><p>Show your business name, declared website, and confirmed-payment count on the ETH402 network page. Counts begin at opt-in. Emails, wallets, payers, amounts, and volume are never shown.</p></div><button id="public-toggle" class="toggle" type="button" role="switch" aria-label="Public merchant profile"></button></div></div><div class="app-card"><span class="overline">ACCOUNT</span><h2>Merchant identity</h2><div class="setting-row"><div class="setting-copy"><h3>Business email</h3><p id="settings-email"></p></div><span class="status-chip">VERIFIED</span></div><div class="setting-row"><div class="setting-copy"><h3>USDC recipient</h3><p id="settings-recipient" class="code"></p><p>Connect and sign with a new recipient wallet to change it. Existing API keys remain valid; the recipient-change cooldown still applies.</p></div><button id="change-recipient" class="button button-small button-secondary" type="button">Change wallet</button></div><div class="setting-row"><div class="setting-copy"><h3>Website</h3><p id="settings-website">Not set</p></div></div></div></section>
</div></div></div></section>
</main><script src="/merchant/app.js" defer></script></body></html>`

const merchantPanelScript = `'use strict';
const $=id=>document.getElementById(id);
let merchant=null,walletAuthenticated=false,latestSecret='',secretRefresh=null,activeView='overview';

function notice(message,bad=false,auth=false,info=false){
  const el=$(auth?'notice-auth':'notice');
  el.textContent=message;
  el.className='notice'+(bad?' bad':info?' info':'');
  el.setAttribute('aria-live',bad?'assertive':'polite');
  if(bad)el.setAttribute('role','alert');else el.removeAttribute('role');
}
function clearSecret(){latestSecret='';secretRefresh=null;$('new-secret').textContent='';$('key-once').classList.add('hidden')}
function showSignedOut(message=''){
  merchant=null;walletAuthenticated=false;clearSecret();
  $('welcome').classList.remove('hidden');$('account').classList.add('hidden');$('logout').classList.add('hidden');
  $('dashboard').classList.add('hidden');$('verification').classList.add('hidden');$('account-state').classList.add('hidden');
  notice(message,false,false,!!message);setView('overview');
}
async function api(path,options={}){
  options.headers={...(options.headers||{}),'Content-Type':'application/json'};
  const response=await fetch(path,options);
  if(response.status===204)return null;
  let body={};try{body=await response.json()}catch{}
  if(!response.ok){
    const expired=response.status===401&&path.startsWith('/merchant/api/');
    const error=new Error(expired?'Your session expired. Sign in again.':body.error?.message||'Request failed');
    error.status=response.status;error.code=body.error?.code;
    if(expired)showSignedOut(error.message);
    throw error;
  }
  return body;
}
async function withBusy(target,task){
  const button=target.matches('button')?target:target.querySelector('button[type="submit"]');
  if(target.getAttribute('aria-busy')==='true')return;
  target.setAttribute('aria-busy','true');if(button)button.disabled=true;
  try{return await task()}finally{target.removeAttribute('aria-busy');if(button)button.disabled=false}
}
function toggleState(element,enabled){element.classList.toggle('on',enabled);element.setAttribute('aria-checked',String(enabled))}

const titles={overview:'Overview',analytics:'Analytics',keys:'API keys',settings:'Settings'};
function setView(name,focus=false){
  activeView=titles[name]?name:'overview';
  document.querySelectorAll('[data-view]').forEach(button=>{
    const selected=button.dataset.view===activeView;
    button.classList.toggle('active',selected);button.setAttribute('aria-selected',String(selected));button.tabIndex=selected?0:-1;
    if(selected&&focus)button.focus();
  });
  document.querySelectorAll('[data-panel]').forEach(panel=>panel.classList.toggle('active',panel.dataset.panel===activeView));
  $('view-title').textContent=titles[activeView];
}
const tabs=[...document.querySelectorAll('[data-view]')];
tabs.forEach((button,index)=>{
  button.addEventListener('click',()=>setView(button.dataset.view));
  button.addEventListener('keydown',event=>{
    let next;
    if(event.key==='ArrowRight'||event.key==='ArrowDown')next=(index+1)%tabs.length;
    if(event.key==='ArrowLeft'||event.key==='ArrowUp')next=(index-1+tabs.length)%tabs.length;
    if(event.key==='Home')next=0;if(event.key==='End')next=tabs.length-1;
    if(next!==undefined){event.preventDefault();setView(tabs[next].dataset.view,true)}
  });
});
document.querySelectorAll('[data-go]').forEach(button=>button.addEventListener('click',()=>setView(button.dataset.go,true)));

function renderAccountState(){
  const panel=$('account-state'),unsupported=!['pending','active'].includes(merchant.status);
  panel.classList.toggle('hidden',!unsupported);
  if(!unsupported)return;
  const states={
    suspended:['Account suspended','Merchant operations are paused. Contact ETH402 support before attempting new integration activity.'],
    manual_review:['Account under review','Your registration needs an operator review. No further wallet action is required right now.'],
    rejected:['Registration not approved','This merchant registration cannot be activated. Contact ETH402 support if you believe this is an error.']
  };
  const state=states[merchant.status]||['Account unavailable','This account cannot be managed from the merchant panel in its current state.'];
  $('account-state-title').textContent=state[0];$('account-state-copy').textContent=state[1];
}
function renderVerification(needsWallet){
  $('verification').classList.toggle('hidden',!needsWallet);if(!needsWallet)return;
  const pending=merchant.status==='pending';
  $('verification-title').textContent=pending?'Verify the recipient wallet':'Unlock sensitive settings';
  $('verification-copy').replaceChildren();
  if(pending){
    $('verification-copy').append('Connect the wallet that should receive USDC and sign a human-readable message. If it differs from ');
    const code=document.createElement('code');code.id='wallet-address';code.textContent=merchant.recipient_address;$('verification-copy').append(code,', you can replace the pending recipient before activation. No transaction, gas, or funds are involved.');
    $('verify-wallet-label').textContent='Connect or replace wallet';
  }else{
    $('verification-copy').append('Connect the current recipient ');
    const code=document.createElement('code');code.id='wallet-address';code.textContent=merchant.recipient_address;$('verification-copy').append(code,' and sign a human-readable message to access keys, analytics, and settings. No transaction, gas, or funds are involved.');
    $('verify-wallet-label').textContent='Connect recipient wallet';
  }
}
async function load(){
  try{
    const data=await api('/merchant/api/session');merchant=data.merchant;walletAuthenticated=data.wallet_authenticated;
    $('welcome').classList.add('hidden');$('account').classList.remove('hidden');$('logout').classList.remove('hidden');notice('',false,true);
    $('merchant-name').textContent=merchant.name;$('merchant-email').textContent=merchant.business_email;
    $('merchant-meta').textContent='Ethereum mainnet · native USDC';$('merchant-status').textContent=merchant.status;
    $('overview-recipient').textContent=merchant.recipient_address;$('settings-recipient').textContent=merchant.recipient_address;
    $('settings-email').textContent=merchant.business_email;$('settings-website').textContent=merchant.website||'Not set';
    $('wallet-badge').textContent=walletAuthenticated?'WALLET AUTHENTICATED':'WALLET REQUIRED';$('wallet-badge').classList.toggle('status-chip',walletAuthenticated);
    const publicEnabled=!!merchant.public_profile_opted_in_at;toggleState($('public-toggle'),publicEnabled);
    $('overview-public').textContent=publicEnabled?'Your merchant is visible on the public network leaderboard.':'Your merchant profile is private.';
    const needsWallet=(merchant.status==='pending'&&!merchant.wallet_verified_at)||(merchant.status==='active'&&!walletAuthenticated);
    renderVerification(needsWallet);renderAccountState();
    const showDashboard=merchant.status==='active'&&walletAuthenticated;$('dashboard').classList.toggle('hidden',!showDashboard);
    if(showDashboard)await Promise.all([loadKeys(),loadStats()]);
  }catch(error){
    if(error.status!==401){
      if(merchant)notice(error.message,true,true);else notice('The merchant panel could not be loaded. Please retry in a moment.',true,false);
    }
  }
}

$('signin').addEventListener('submit',event=>{event.preventDefault();withBusy(event.currentTarget,async()=>{
  try{await api('/v1/merchants/admin-link',{method:'POST',body:JSON.stringify({business_email:$('signin-email').value})});notice('If that address is registered, a one-time sign-in link has been sent.')}catch(error){notice(error.message,true)}
})});
$('register').addEventListener('submit',event=>{event.preventDefault();withBusy(event.currentTarget,async()=>{
  try{await api('/v1/merchants/register',{method:'POST',body:JSON.stringify({name:$('name').value,business_email:$('email').value,recipient_address:$('recipient').value,website:$('website').value,description:'',accept_terms:$('terms').checked})});notice('Check your inbox and use the newest verification link.')}catch(error){notice(error.message,true)}
})});
$('logout').addEventListener('click',event=>withBusy(event.currentTarget,async()=>{
  try{await api('/merchant/api/logout',{method:'POST'});showSignedOut()}catch(error){if(error.status!==401)notice('Sign-out failed. Your session is still active; please try again.',true,true)}
}));

function utf8Hex(value){return '0x'+Array.from(new TextEncoder().encode(value),byte=>byte.toString(16).padStart(2,'0')).join('')}
async function connectedAccount(){
  if(!window.ethereum)throw new Error('No compatible Ethereum wallet was found in this browser.');
  const accounts=await window.ethereum.request({method:'eth_requestAccounts'}),account=accounts[0]||'';
  if(!/^0x[0-9a-fA-F]{40}$/.test(account))throw new Error('The wallet did not return a valid Ethereum account.');return account;
}
$('verify-wallet').addEventListener('click',event=>withBusy(event.currentTarget,async()=>{
  try{
    const account=await connectedAccount(),pending=merchant.status==='pending',replacing=pending&&account.toLowerCase()!==merchant.recipient_address.toLowerCase();
    if(replacing&&!confirm('Replace the pending recipient '+merchant.recipient_address+' with '+account+'? Activation will require a signature from the new wallet.'))return;
    const challenge=await api('/merchant/api/wallet-challenge',{method:'POST',body:JSON.stringify(pending?{address:account}:{})});
    if(account.toLowerCase()!==challenge.address.toLowerCase())throw new Error('Connected wallet does not match the current recipient. Switch accounts in your wallet and try again.');
    const signature=await window.ethereum.request({method:'personal_sign',params:[utf8Hex(challenge.message),account]});
    const activated=await api('/merchant/api/verify-wallet',{method:'POST',body:JSON.stringify({challenge_id:challenge.id,message:challenge.message,signature})});
    if(activated.api_key){
      showSecret(activated.api_key,load);notice('Recipient verified. Save the API key before leaving this page.',false,true);
    }else{notice('Wallet authentication complete.',false,true);await load()}
  }catch(error){if(error.status!==401)notice(error.message,true,true)}
}));
$('change-recipient').addEventListener('click',event=>withBusy(event.currentTarget,async()=>{
  try{
    const account=await connectedAccount();if(account.toLowerCase()===merchant.recipient_address.toLowerCase())throw new Error('Select a different wallet account before changing the recipient.');
    if(!confirm('Change the USDC recipient from '+merchant.recipient_address+' to '+account+'?'))return;
    const challenge=await api('/merchant/api/recipient-challenge',{method:'POST',body:JSON.stringify({new_address:account})});
    if(account.toLowerCase()!==challenge.address.toLowerCase())throw new Error('The recipient challenge does not match the connected account.');
    const signature=await window.ethereum.request({method:'personal_sign',params:[utf8Hex(challenge.message),account]});
    await api('/merchant/api/verify-recipient-change',{method:'POST',body:JSON.stringify({challenge_id:challenge.id,message:challenge.message,signature})});
    notice('Recipient wallet changed successfully.',false,true);await load();
  }catch(error){if(error.status!==401)notice(error.message,true,true)}
}));

function showSecret(secret,refresh){latestSecret=secret;secretRefresh=refresh;$('new-secret').textContent=secret;$('key-once').classList.remove('hidden')}
async function loadKeys(){
  const data=await api('/merchant/api/api-keys'),list=$('keys');list.replaceChildren();$('keys-empty').classList.toggle('hidden',data.keys.length!==0);
  for(const key of data.keys){
    const li=document.createElement('li'),info=document.createElement('div'),name=document.createElement('strong'),meta=document.createElement('small'),button=document.createElement('button');
    name.textContent=key.name;meta.textContent=key.prefix+(key.revoked_at?' · revoked':' · active');info.append(name,meta);
    button.textContent='Revoke';button.className='button button-small button-danger';button.disabled=!!key.revoked_at;
    button.addEventListener('click',()=>{if(!confirm('Revoke this API key? This cannot be undone.'))return;withBusy(button,async()=>{try{await api('/merchant/api/api-keys/'+encodeURIComponent(key.id),{method:'DELETE'});await loadKeys()}catch(error){if(error.status!==401)notice(error.message,true,true)}})});
    li.append(info,button);list.append(li);
  }
}
$('new-key').addEventListener('submit',event=>{event.preventDefault();withBusy(event.currentTarget,async()=>{
  try{const data=await api('/merchant/api/api-keys',{method:'POST',body:JSON.stringify({name:$('key-name').value})});showSecret(data.api_key,loadKeys);$('key-name').value='';notice('API key created. Save it before continuing.',false,true)}catch(error){if(error.status!==401)notice(error.message,true,true)}
})});
async function loadStats(){
  const enabled=!!merchant.stats_opted_in_at;toggleState($('stats-toggle'),enabled);toggleState($('settings-stats-toggle'),enabled);
  $('stats-values').classList.toggle('hidden',!enabled);$('stats-empty').classList.toggle('hidden',enabled);
  if(!enabled){$('overview-verified').textContent='—';$('overview-confirmed').textContent='—';$('overview-volume').textContent='—';return}
  const stats=await api('/merchant/api/stats');$('stat-verified').textContent=stats.verified_payments;$('stat-confirmed').textContent=stats.confirmed_settlements;
  $('stat-pending').textContent=stats.pending_settlements;$('stat-failed').textContent=stats.failed_settlements;$('stat-volume').textContent=stats.confirmed_volume_usdc;
  $('stats-since').textContent=new Date(stats.observed_since).toLocaleDateString();$('overview-verified').textContent=stats.verified_payments;
  $('overview-confirmed').textContent=stats.confirmed_settlements;$('overview-volume').textContent=stats.confirmed_volume_usdc;
}
async function setStatsConsent(button){
  await withBusy(button,async()=>{const enabled=!merchant.stats_opted_in_at;try{await api('/merchant/api/stats-consent',{method:'PUT',body:JSON.stringify({enabled})});notice(enabled?'Private analytics enabled.':'Private analytics disabled.',false,true);await load()}catch(error){if(error.status!==401)notice(error.message,true,true)}});
}
$('stats-toggle').addEventListener('click',event=>setStatsConsent(event.currentTarget));$('settings-stats-toggle').addEventListener('click',event=>setStatsConsent(event.currentTarget));
$('public-toggle').addEventListener('click',event=>withBusy(event.currentTarget,async()=>{
  const enabled=!merchant.public_profile_opted_in_at;try{await api('/merchant/api/public-profile',{method:'PUT',body:JSON.stringify({enabled})});notice(enabled?'Public merchant profile enabled.':'Public merchant profile disabled.',false,true);await load()}catch(error){if(error.status!==401)notice(error.message,true,true)}
}));
$('copy-secret').addEventListener('click',event=>withBusy(event.currentTarget,async()=>{
  try{if(!latestSecret)throw new Error('No API key is available to copy.');await navigator.clipboard.writeText(latestSecret);notice('API key copied.',false,true)}catch{notice('Copy was blocked by the browser. Select the key above and copy it manually.',true,true)}
}));
$('download-secret').addEventListener('click',()=>{if(!latestSecret)return;const blob=new Blob([latestSecret+'\n'],{type:'text/plain'}),link=document.createElement('a'),url=URL.createObjectURL(blob);link.href=url;link.download='eth402-api-key.txt';link.click();setTimeout(()=>URL.revokeObjectURL(url),1000)});
$('dismiss-secret').addEventListener('click',event=>withBusy(event.currentTarget,async()=>{const refresh=secretRefresh;clearSecret();if(refresh)try{await refresh()}catch(error){if(error.status!==401)notice(error.message,true,true)}}));
setView(activeView);load();`

func (d Dependencies) merchantPanel(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	_, _ = w.Write([]byte(merchantPanelHTML))
}

func (d Dependencies) merchantPanelJS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	_, _ = w.Write([]byte(merchantPanelScript))
}
