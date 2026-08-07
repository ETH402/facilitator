package httpapi

const siteCSSContent = `:root{color-scheme:dark;--ink:#f7f9ff;--muted:#8d9ab5;--blue:#1d64ff;--blue2:#4d86ff;--navy:#020817;--panel:#071126;--panel2:#0a1630;--line:rgba(130,159,220,.16);--line-strong:rgba(130,159,220,.27);--green:#39d98a;--red:#ff5d72;--sans:Inter,ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;--mono:"SFMono-Regular",Consolas,"Liberation Mono",monospace}

*{box-sizing:border-box}
html{scroll-behavior:smooth}
body{margin:0;background:var(--navy);color:var(--ink);font-family:var(--sans);font-size:15px;line-height:1.55;overflow-x:hidden}
a{color:inherit;text-decoration:none}
button,input,textarea{font:inherit}
button{cursor:pointer}
button:disabled{cursor:not-allowed;opacity:.58;transform:none!important;box-shadow:none!important}
code{overflow-wrap:anywhere}
.skip-link{position:fixed;left:16px;top:12px;z-index:1000;padding:10px 14px;background:#fff;color:#071126;font-weight:750;transform:translateY(-160%)}
.skip-link:focus{transform:none}
:where(a,button,input,textarea):focus-visible{outline:3px solid #8eb0ff;outline-offset:3px}
.sr-only{position:absolute!important;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}
.shell{width:min(1180px,calc(100% - 40px));margin-inline:auto}
.public-page{min-height:100vh;background-image:linear-gradient(rgba(36,76,150,.045) 1px,transparent 1px),linear-gradient(90deg,rgba(36,76,150,.045) 1px,transparent 1px);background-size:54px 54px}
.ambient{position:fixed;border-radius:50%;filter:blur(100px);pointer-events:none;opacity:.22;z-index:-1}
.ambient-one{width:600px;height:600px;background:#064fff;top:-320px;left:50%;transform:translateX(-50%)}
.ambient-two{width:420px;height:420px;background:#102c91;right:-260px;top:620px}
.site-header{height:82px;display:flex;align-items:center;justify-content:space-between;border-bottom:1px solid var(--line);position:relative;z-index:10}
.brand{display:flex;align-items:center;gap:10px;font-size:20px;font-weight:850;letter-spacing:-.035em}
.brand>span:last-child>span{color:var(--blue2)}
.brand-mark{width:25px;height:30px;display:inline-block;position:relative}
.brand-mark i,.brand-mark b{position:absolute;left:3px;width:19px;clip-path:polygon(50% 0,100% 72%,50% 100%,0 72%)}
.brand-mark i{height:18px;background:linear-gradient(135deg,#fff 0 50%,#9ca8bf 50%);top:0}
.brand-mark b{height:14px;background:linear-gradient(135deg,#2870ff 0 50%,#0745d0 50%);bottom:0;transform:rotate(180deg)}
.site-nav{display:flex;align-items:center;gap:30px;color:#9eabc4;font-size:13px;font-weight:650}
.site-nav a{padding:30px 0;position:relative}
.site-nav a:hover,.site-nav a.active{color:#fff}
.site-nav a.active:after{content:"";position:absolute;left:0;right:0;bottom:-1px;height:2px;background:var(--blue2);box-shadow:0 0 16px var(--blue)}
.mobile-nav{display:none;align-items:center;gap:15px;color:#a9b5ca;font-size:12px;font-weight:700}
.button{display:inline-flex;align-items:center;justify-content:center;gap:16px;padding:14px 20px;border:1px solid var(--blue);background:var(--blue);color:#fff;font-weight:750;font-size:13px;letter-spacing:.01em;border-radius:8px;box-shadow:0 10px 34px rgba(22,93,255,.22);transition:background .2s ease,border-color .2s ease,box-shadow .2s ease,transform .2s ease}
.button:hover{background:#3474ff;transform:translateY(-1px);box-shadow:0 14px 40px rgba(22,93,255,.34)}
.button-small{padding:10px 14px;font-size:12px}
.button-ghost{background:rgba(8,19,44,.58);border-color:var(--line);box-shadow:none}
.button-ghost:hover{background:rgba(16,34,72,.8);border-color:rgba(90,130,220,.4)}
.button-light{background:#fff;color:#071126;border-color:#fff;box-shadow:none}
.hero{min-height:680px;display:grid;grid-template-columns:1.04fr .96fr;align-items:center;gap:30px;padding-block:80px 90px}
.eyebrow,.overline{font-family:var(--mono);font-size:11px;font-weight:700;letter-spacing:.16em;color:#7fa5ff;text-transform:uppercase}
.eyebrow{display:inline-flex;align-items:center;gap:9px;max-width:100%;padding:8px 12px;border:1px solid rgba(69,116,220,.25);border-radius:999px;background:rgba(8,23,54,.55);margin-bottom:26px;line-height:1.35}
.live-dot{display:inline-block;width:7px;height:7px;border-radius:50%;background:var(--green);box-shadow:0 0 12px var(--green)}
.status-degraded .live-dot{background:#ffbd59;box-shadow:0 0 12px #ffbd59}
.status-outage .live-dot{background:var(--red);box-shadow:0 0 12px var(--red)}
.status-unknown .live-dot{background:#8290aa;box-shadow:none}
.hero h1{font-size:clamp(50px,6vw,82px);line-height:.98;letter-spacing:-.065em;margin:0;max-width:720px}
.hero h1 span{color:transparent;-webkit-text-stroke:1px #4778df;text-shadow:0 0 40px rgba(26,91,255,.14)}
.hero-lead{font-size:18px;color:#a1adc2;max-width:610px;margin:30px 0}
.hero-actions{display:flex;gap:12px;margin-bottom:34px}
.trust-row{display:flex;flex-wrap:wrap;gap:18px;color:#7988a5;font-size:11px;font-family:var(--mono)}
.trust-row i{font-style:normal;color:#6c98ff;margin-right:4px}
.hero-copy,.hero-visual{min-width:0}
.hero-visual{height:500px;position:relative;display:grid;place-items:center;isolation:isolate}
.hero-visual:before{content:"";position:absolute;inset:2%;background:radial-gradient(circle,rgba(24,83,232,.22),transparent 62%)}
.orbit{position:absolute;border:1px solid rgba(59,111,231,.18);border-radius:50%}
.orbit-one{width:360px;height:360px}
.orbit-two{width:460px;height:460px;border-style:dashed;animation:orbit-spin 42s linear infinite}
.orbit-three{width:270px;height:270px;box-shadow:0 0 70px rgba(11,72,218,.13) inset}
.orbit:after{content:"";position:absolute;width:5px;height:5px;border-radius:50%;background:#2d6bff;box-shadow:0 0 14px #2d6bff;top:50%;left:-3px}
.orbit-two:after{top:9%;left:76%}
.core-mark{width:180px;height:210px;background:linear-gradient(145deg,rgba(14,34,78,.95),rgba(4,13,31,.95));border:1px solid rgba(79,124,227,.28);clip-path:polygon(50% 0,95% 24%,95% 76%,50% 100%,5% 76%,5% 24%);display:flex;flex-direction:column;align-items:center;justify-content:center;z-index:2;box-shadow:0 0 70px rgba(16,80,238,.2)}
.eth-gem{height:70px;width:48px;position:relative;margin-bottom:5px}
.eth-gem span,.eth-gem i{position:absolute;inset:0;clip-path:polygon(50% 0,100% 68%,50% 96%,0 68%);background:linear-gradient(135deg,#fff 0 50%,#8fa0c4 50%)}
.eth-gem i{top:48px;height:31px;background:linear-gradient(135deg,#2c73ff 0 50%,#0644ce 50%);transform:rotate(180deg)}
.core-mark strong{font-size:35px;letter-spacing:-.06em}
.core-mark small{font-family:var(--mono);font-size:8px;letter-spacing:.18em;color:#6980ab}
.float-card{position:absolute;z-index:3;display:flex;align-items:center;gap:11px;padding:12px 15px;background:rgba(5,16,39,.91);border:1px solid rgba(75,116,208,.25);border-radius:8px;box-shadow:0 18px 45px rgba(0,0,0,.3);min-width:160px;backdrop-filter:blur(12px);animation:float-card 6s ease-in-out infinite}
.float-card small{display:block;color:#667896;font:8px var(--mono);letter-spacing:.16em}
.float-card strong{font:700 11px var(--mono)}
.float-icon{display:grid;place-items:center;width:29px;height:29px;background:rgba(31,91,224,.14);border:1px solid rgba(51,107,238,.32);color:#6794ff;font:700 11px var(--mono)}
.float-icon.success{color:var(--green);border-color:rgba(57,217,138,.3);background:rgba(57,217,138,.08)}
.card-request{left:1%;top:18%}
.card-settle{right:-2%;bottom:19%;animation-delay:-2s}
.card-agent{left:3%;bottom:10%;animation-delay:-4s}
@keyframes orbit-spin{to{transform:rotate(360deg)}}
@keyframes float-card{0%,100%{transform:translateY(0)}50%{transform:translateY(-7px)}}
.metric-band{border-block:1px solid var(--line);background:rgba(5,14,33,.72)}
.metric-grid{display:grid;grid-template-columns:repeat(5,1fr)}
.metric-grid>div{padding:25px 28px;border-right:1px solid var(--line)}
.metric-grid>div:first-child{border-left:1px solid var(--line)}
.metric-kicker,.metric-grid small{display:block;color:#65728d;font:9px var(--mono);letter-spacing:.12em}
.metric-grid strong{display:block;font-size:24px;letter-spacing:-.04em;margin:7px 0 1px}
.section{padding-block:110px}
.section-heading{display:grid;grid-template-columns:1.35fr .65fr;gap:80px;align-items:end;margin-bottom:48px}
.section-heading.compact{grid-template-columns:1fr auto}
.section-heading h2{font-size:clamp(34px,4vw,52px);line-height:1.08;letter-spacing:-.05em;margin:12px 0 0}
.section-heading p{color:var(--muted);font-size:16px;margin:0}
.feature-grid{display:grid;grid-template-columns:repeat(3,1fr);gap:14px}
.feature-card{position:relative;min-height:330px;padding:32px;background:linear-gradient(145deg,rgba(10,25,57,.84),rgba(4,13,31,.85));border:1px solid var(--line);border-radius:12px;overflow:hidden;transition:border-color .2s ease,transform .2s ease,background .2s ease}
.feature-card:hover{transform:translateY(-3px);border-color:var(--line-strong);background:linear-gradient(145deg,rgba(12,31,70,.95),rgba(5,15,37,.92))}
.feature-card:after{content:"";position:absolute;width:160px;height:160px;border:1px solid rgba(46,95,204,.14);border-radius:50%;right:-80px;bottom:-80px}
.feature-number{position:absolute;right:24px;top:22px;color:#374766;font:11px var(--mono)}
.feature-icon{display:grid;place-items:center;width:50px;height:50px;border:1px solid rgba(67,117,227,.3);color:#719aff;background:rgba(24,75,191,.1);font:700 15px var(--mono);margin-bottom:55px}
.feature-card h3{font-size:20px;margin:0 0 12px}
.feature-card p{color:#8795ae;margin:0 0 25px}
.feature-tag{color:#4f6fbd;font:9px var(--mono);letter-spacing:.14em}
.flow-section{padding-top:30px}
.protocol-flow{display:grid;grid-template-columns:1fr auto 1fr auto 1fr auto 1fr;border:1px solid var(--line);background:rgba(4,13,31,.72);align-items:center}
.protocol-flow>div{padding:28px}
.protocol-flow>span{color:#315aa7}
.protocol-flow b{display:block;color:#3c69c8;font:10px var(--mono);margin-bottom:18px}
.protocol-flow strong{display:block}
.protocol-flow small{display:block;color:#75839e;margin-top:5px}
.open-section{display:grid;grid-template-columns:.9fr 1.1fr;gap:70px;align-items:center;padding-top:55px}
.open-copy h2{font-size:clamp(34px,4vw,52px);line-height:1.08;letter-spacing:-.05em;margin:12px 0 22px}
.open-copy>p{color:var(--muted);font-size:16px;max-width:570px}
.proof-list{display:flex;flex-wrap:wrap;gap:9px;margin:26px 0}
.proof-list span{padding:7px 10px;border:1px solid rgba(71,120,231,.24);border-radius:999px;background:rgba(17,48,112,.22);color:#93adE8;font:9px var(--mono);letter-spacing:.08em;text-transform:uppercase}
.open-actions{display:flex;align-items:center;gap:22px;margin-top:30px}
.capability-window{overflow:hidden;border:1px solid var(--line-strong);border-radius:12px;background:rgba(3,11,28,.94);box-shadow:0 35px 100px rgba(0,0,0,.3),0 0 70px rgba(22,93,255,.08)}
.window-bar{height:48px;display:flex;align-items:center;gap:7px;padding:0 17px;border-bottom:1px solid var(--line);background:#08142b}
.window-bar>span{width:7px;height:7px;border-radius:50%;background:#344663}
.window-bar>span:first-child{background:#4d86ff}.window-bar>span:nth-child(2){background:#39d98a}.window-bar>span:nth-child(3){background:#ffbd59}
.window-bar b{margin-left:auto;color:#71809a;font:9px var(--mono);letter-spacing:.12em}
.capability-window pre{margin:0;padding:30px 32px;overflow:auto;color:#a9b9d5;font:13px/1.9 var(--mono)}
.capability-window code{font:inherit}.capability-window code>b{color:#7ca3ff}.capability-window code>i{color:#55dda0;font-style:normal}
.window-foot{display:flex;justify-content:space-between;gap:18px;padding:13px 17px;border-top:1px solid var(--line);color:#566784;font:8px var(--mono);letter-spacing:.1em}
.window-foot>span:first-child{color:#8aa2cf}.window-foot i{display:inline-block;width:6px;height:6px;margin-right:5px;border-radius:50%;background:var(--green);box-shadow:0 0 9px var(--green)}
.merchant-showcase{padding-top:45px}
.text-link{color:#7fa5ff;font-size:13px}
.leaderboard-preview{border-top:1px solid var(--line)}
.merchant-row{display:grid;grid-template-columns:auto 1fr auto auto;gap:18px;align-items:center;padding:18px 12px;border-bottom:1px solid var(--line)}
.merchant-avatar{display:grid;place-items:center;width:42px;height:42px;border:1px solid rgba(68,118,229,.3);background:linear-gradient(135deg,#122b61,#07152f);color:#83a8ff;font-weight:800}
.merchant-identity strong,.merchant-identity small,.merchant-count strong,.merchant-count small{display:block}
.merchant-identity small,.merchant-count small{color:#697793;font-size:11px}
.merchant-count{text-align:right}
.merchant-count strong{font:700 20px var(--mono)}
.row-link{display:grid;place-items:center;width:34px;height:34px;border:1px solid var(--line);color:#759aff}
.empty-showcase{display:grid;grid-template-columns:auto 1fr auto;gap:28px;align-items:center;padding:34px;border:1px solid var(--line);background:rgba(6,17,40,.65)}
.empty-showcase h3{margin:0 0 5px}
.empty-showcase p{color:var(--muted);margin:0;max-width:680px}
.empty-orbit{width:55px;height:55px;border:1px solid #245bce;border-radius:50%;position:relative;box-shadow:0 0 30px rgba(22,93,255,.2)}
.empty-orbit:after{content:"";position:absolute;width:8px;height:8px;background:#4b82ff;border-radius:50%;right:1px;top:8px;box-shadow:0 0 12px #4b82ff}
.empty-orbit.unavailable{border-color:#58657d;box-shadow:none}
.empty-orbit.unavailable:after{background:#8290aa;box-shadow:none}
.cta{margin-bottom:100px;padding:55px 60px;background:linear-gradient(105deg,#155dff,#09379f);display:flex;align-items:center;justify-content:space-between;position:relative;overflow:hidden;border-radius:14px;box-shadow:0 30px 90px rgba(5,47,155,.25)}
.cta:after{content:"";position:absolute;width:360px;height:360px;border:1px solid rgba(255,255,255,.16);border-radius:50%;right:15%;top:-180px}
.cta .overline{color:#c3d5ff}
.cta h2{font-size:38px;letter-spacing:-.05em;margin:7px 0}
.cta p{margin:0;color:#c3d5ff}
.cta .button{z-index:1}
.site-footer{min-height:130px;border-top:1px solid var(--line);display:grid;grid-template-columns:auto 1fr auto auto;align-items:center;gap:35px;color:#697793;font-size:12px}
.brand-small{font-size:16px;color:#fff}
.brand-small .brand-mark{transform:scale(.78)}
.site-footer p{margin:0}
.site-footer>div{display:flex;gap:20px}
.site-footer a:hover{color:#fff}
.inner-page{background-position:center top}
.explore-main{padding-block:90px 120px}
.page-intro{position:relative;padding:40px 0 70px;max-width:820px}
.page-intro h1{font-size:clamp(48px,6vw,76px);line-height:1;letter-spacing:-.06em;margin:14px 0 22px}
.page-intro p{font-size:18px;color:#94a2bc;max-width:650px}
.status-pill{display:inline-flex;align-items:center;gap:9px;margin-top:12px;padding:8px 12px;border:1px solid var(--line);color:#92a1bd;font:10px var(--mono);text-transform:uppercase;letter-spacing:.13em}
.status-pill.status-degraded .live-dot{background:#ffbd59;box-shadow:0 0 12px #ffbd59}
.status-pill.status-outage .live-dot{background:var(--red);box-shadow:0 0 12px var(--red)}
.status-pill.status-unknown .live-dot{background:#8290aa;box-shadow:none}
.stats-grid{display:grid;grid-template-columns:repeat(3,1fr);gap:1px;background:var(--line);border:1px solid var(--line);margin-bottom:70px}
.stats-grid article{background:rgba(5,15,36,.94);padding:28px}
.stats-grid small,.stats-grid span{display:block;color:#687792;font:9px var(--mono);letter-spacing:.1em}
.stats-grid strong{display:block;font-size:34px;margin:14px 0 4px;letter-spacing:-.05em}
.network-panel{border:1px solid var(--line);background:rgba(5,15,36,.8)}
.panel-heading{padding:28px 30px;border-bottom:1px solid var(--line);display:flex;align-items:center;justify-content:space-between}
.panel-heading h2{margin:5px 0 0;font-size:24px}
.privacy-label{padding:7px 9px;border:1px solid rgba(57,217,138,.22);color:#52d995;background:rgba(57,217,138,.06);font:9px var(--mono);letter-spacing:.12em}
.leaderboard-head,.leaderboard article{display:grid;grid-template-columns:1.5fr .8fr .35fr 36px;align-items:center;gap:20px;padding:14px 24px}
.leaderboard-head{color:#5f6e8b;font:9px var(--mono);letter-spacing:.12em}
.leaderboard article{border-top:1px solid var(--line);color:#8492ac;font-size:12px}
.leaderboard article>strong{font:700 18px var(--mono);color:#fff}
.rank{display:flex;align-items:center;gap:14px}
.rank>b{width:26px;color:#516281;font:10px var(--mono)}
.rank strong{color:#fff}
.rank .merchant-avatar{width:36px;height:36px}
.network-empty{margin:28px;border:0}
.privacy-note{color:#64738f;font-size:12px;max-width:820px;margin:22px auto;text-align:center}
.privacy-note a{color:#769bfa}

.verify-page{min-height:100vh;display:grid;place-items:center;padding:24px;background-image:radial-gradient(circle at 50% 0,rgba(22,93,255,.2),transparent 48%),linear-gradient(rgba(36,76,150,.045) 1px,transparent 1px),linear-gradient(90deg,rgba(36,76,150,.045) 1px,transparent 1px);background-size:auto,54px 54px,54px 54px}
.verify-card{width:min(520px,100%);padding:38px;box-shadow:0 28px 90px rgba(0,0,0,.35)}
.verify-card .brand{margin-bottom:46px}
.verify-card h1{font-size:36px;line-height:1.08;letter-spacing:-.045em;margin:10px 0 14px}
.verify-card p{color:#8d9ab5}
.verify-card code{display:block;margin-top:8px;color:#dbe5ff;font-size:12px}
.verify-card form,.verify-card>.button{margin-top:24px}
.verify-symbol{display:grid;place-items:center;width:48px;height:48px;margin-bottom:24px;border:1px solid currentColor;font-size:24px;font-weight:850}
.verify-symbol.ok{color:var(--green);background:rgba(57,217,138,.07)}
.verify-symbol.bad{color:var(--red);background:rgba(255,93,114,.07)}

.app-shell{min-height:100vh;background:#030918}
.app-header{height:72px;border-bottom:1px solid var(--line);display:flex;align-items:center;justify-content:space-between;padding:0 28px;background:rgba(3,9,24,.92);position:sticky;top:0;z-index:20;backdrop-filter:blur(15px)}
.app-header-actions{display:flex;align-items:center;gap:10px}
.app-main{width:min(1220px,calc(100% - 40px));margin:auto;padding:48px 0 80px}
.auth-intro{text-align:center;max-width:690px;margin:30px auto 40px}
.auth-intro h1{font-size:48px;letter-spacing:-.055em;line-height:1.05;margin:12px 0}
.auth-intro p{color:#8e9bb4;font-size:16px}
.auth-grid{display:grid;grid-template-columns:.82fr 1.18fr;gap:16px;max-width:980px;margin:auto}
.app-card{background:linear-gradient(145deg,rgba(10,24,55,.92),rgba(5,14,33,.94));border:1px solid var(--line);border-radius:12px;padding:28px;box-shadow:0 18px 55px rgba(0,0,0,.12)}
.app-card h2,.app-card h3{margin:0 0 8px}
.app-card>p{color:#8190aa;margin-top:0}
.view>.app-card+.app-card{margin-top:14px}
.auth-card{display:flex;flex-direction:column}
.auth-card.featured{border-color:rgba(52,106,229,.42);box-shadow:0 22px 70px rgba(0,0,0,.18),0 0 55px rgba(22,93,255,.055)}
.auth-trust{display:flex;align-items:center;justify-content:center;gap:0;margin-top:28px;color:#7f8da7;font:10px var(--mono);letter-spacing:.06em;text-transform:uppercase}
.auth-trust span{display:flex;align-items:center;gap:8px;padding:0 17px;border-right:1px solid var(--line)}
.auth-trust span:last-child{border-right:0}.auth-trust i{display:grid;place-items:center;width:24px;height:24px;border:1px solid rgba(70,119,230,.3);border-radius:50%;color:#75a0ff;font-size:8px;font-style:normal}
.auth-assurance{display:grid;gap:3px;margin-top:auto;padding-top:34px;color:#64738e;font-size:11px}
.auth-assurance strong{color:#9eabc1;font-size:11px}.auth-assurance span{max-width:300px}
.field{margin-top:18px}
.field label{display:flex;justify-content:space-between;margin-bottom:7px;color:#b4bfd2;font-size:12px;font-weight:650}
.field input,.field textarea{width:100%;border:1px solid rgba(116,143,199,.2);background:#030b1c;color:#fff;padding:12px 13px;border-radius:8px;outline:none;transition:border-color .2s ease,box-shadow .2s ease,background .2s ease}
.field input:focus,.field textarea:focus{border-color:#346fe9;box-shadow:0 0 0 3px rgba(22,93,255,.1)}
.field-help{font-size:10px;color:#596984}
.form-button{width:100%;margin-top:22px}
.check-row{display:flex;gap:10px;align-items:flex-start;margin-top:18px;color:#8693aa;font-size:11px}
.check-row input{margin-top:3px}
.notice{min-height:22px;text-align:center;margin:12px 0;color:#69e0a4}
.notice.bad{color:#ff7b8c}
.notice.info{color:#8fa0ba}
.notice:empty{margin-block:0;min-height:0}
.dashboard-layout{display:grid;grid-template-columns:230px 1fr;gap:26px}
.dash-sidebar{position:sticky;top:98px;height:max-content}
.merchant-summary{padding:18px;border:1px solid var(--line);border-radius:10px;background:#071126;margin-bottom:12px}
.merchant-summary strong,.merchant-summary small{display:block}
.merchant-summary small{color:#687792;margin-top:4px;overflow-wrap:anywhere}
.status-chip{display:inline-flex;margin-top:12px;padding:5px 8px;border:1px solid rgba(57,217,138,.22);color:#53d995;background:rgba(57,217,138,.06);font:9px var(--mono);letter-spacing:.1em;text-transform:uppercase}
.dash-nav{display:grid;gap:4px}
.dash-nav button{border:0;border-radius:7px;background:transparent;color:#7f8da8;text-align:left;padding:11px 13px}
.dash-nav button:hover,.dash-nav button.active{background:#0b1935;color:#fff}
.dash-nav button.active{border-left:2px solid var(--blue)}
.dash-content{min-width:0}
.dash-top{display:flex;justify-content:space-between;align-items:end;margin-bottom:28px}
.dash-top h1{font-size:34px;letter-spacing:-.045em;margin:5px 0}
.dash-top p{color:#72809b;margin:0}
.wallet-badge{font:10px var(--mono);color:#6e9bff;padding:8px 10px;border:1px solid rgba(65,113,221,.3)}
.view{display:none}
.view.active{display:block}
.dash-grid{display:grid;grid-template-columns:repeat(2,1fr);gap:14px}
.dash-grid.three{grid-template-columns:repeat(3,1fr)}
.metric-card{padding:22px;border:1px solid var(--line);border-radius:10px;background:#071126}
.metric-card small{display:block;color:#65738e;font:9px var(--mono);letter-spacing:.12em}
.metric-card strong{display:block;font-size:31px;letter-spacing:-.045em;margin-top:10px}
.metric-card span{color:#64728b;font-size:11px}
.wide-card{grid-column:1/-1}
.setting-row{display:flex;align-items:flex-start;justify-content:space-between;gap:30px;padding:20px 0;border-top:1px solid var(--line)}
.setting-row:first-of-type{margin-top:20px}
.setting-copy h3{font-size:14px;margin:0 0 4px}
.setting-copy p{color:#74829c;font-size:12px;margin:0;max-width:600px}
.toggle{position:relative;min-width:46px;width:46px;height:26px;border-radius:20px;border:1px solid #324160;background:#111d35;padding:0}
.toggle:after{content:"";position:absolute;width:18px;height:18px;border-radius:50%;left:3px;top:3px;background:#71809b;transition:.2s}
.toggle.on{background:#1557df;border-color:#3975f4}
.toggle.on:after{left:23px;background:#fff}
.toggle:disabled{opacity:.55}
.key-form{display:flex;gap:8px;margin:18px 0}
.key-form input{flex:1;border:1px solid var(--line);background:#030b1c;color:#fff;padding:11px}
.key-list{list-style:none;padding:0;margin:0}
.key-list li{display:flex;justify-content:space-between;align-items:center;padding:15px 0;border-top:1px solid var(--line)}
.key-list small{color:#64728c;display:block}
.button-danger{background:transparent;border-color:rgba(255,93,114,.25);color:#ff7a8c;box-shadow:none}
.button-secondary{background:transparent;border-color:var(--line);box-shadow:none}
.verification-banner{display:flex;justify-content:space-between;align-items:center;gap:24px;border:1px solid rgba(73,119,225,.35);background:linear-gradient(100deg,rgba(15,45,110,.55),rgba(6,18,44,.8));padding:24px;margin-bottom:18px}
.verification-banner p{color:#91a0ba;margin:4px 0 0}
.secret-card{border-color:rgba(57,217,138,.3);margin-bottom:18px}
.secret-value{display:block;background:#020917;border:1px solid var(--line);padding:14px;margin:15px 0;overflow-wrap:anywhere;color:#75e5ad;font:11px var(--mono)}
.hidden{display:none!important}
.row-actions{display:flex;gap:8px;flex-wrap:wrap}
.muted{color:#73819a}
.code{font-family:var(--mono)}

@media(max-width:900px){.site-nav{display:none}
.mobile-nav{display:flex}
.hero{grid-template-columns:1fr;padding-top:70px}
.hero-visual{height:430px;width:100%;overflow:hidden}
.metric-grid{grid-template-columns:repeat(2,1fr)}
.metric-grid>div{border:1px solid var(--line)}
.section-heading{grid-template-columns:1fr;gap:20px}
.feature-grid{grid-template-columns:1fr}
.feature-card{min-height:auto}.feature-icon{margin-bottom:36px}
.protocol-flow{grid-template-columns:1fr}
.protocol-flow>span{display:none}
.open-section{grid-template-columns:1fr;gap:40px}
.stats-grid{grid-template-columns:repeat(2,1fr)}
.auth-grid,.dashboard-layout{grid-template-columns:1fr}
.dash-sidebar{position:static}
.dash-nav{grid-template-columns:repeat(4,1fr)}
.dash-nav button{text-align:center}
.site-footer{grid-template-columns:1fr 1fr;padding:30px 0}
.site-footer p{display:none}
}

@media(max-width:620px){.shell,.app-main{width:min(100% - 24px,1180px)}
.site-header{height:68px}
.site-header>.button{display:none}
.mobile-nav{gap:11px;font-size:11px}
.mobile-nav a:nth-child(2){display:none}
.hero{padding-block:55px 30px;overflow:hidden}
.hero h1{font-size:clamp(42px,12.3vw,48px);letter-spacing:-.06em}
.hero-lead{font-size:16px}
.hero-actions{flex-direction:column}
.hero-visual{height:390px;margin-top:10px}
.orbit-one{width:260px;height:260px}
.orbit-two{width:330px;height:330px}
.orbit-three{width:210px;height:210px}
.core-mark{width:145px;height:172px}
.eth-gem{transform:scale(.8);margin-bottom:0}
.card-request{left:0;top:12%}
.card-settle{right:0;bottom:9%}
.card-agent{display:none}
.metric-grid{grid-template-columns:1fr 1fr}
.metric-grid>div{padding:18px}
.section{padding-block:75px}
.section-heading h2{font-size:36px}
.open-section{padding-top:30px}.open-actions{align-items:flex-start;flex-direction:column;gap:14px}.capability-window pre{padding:24px 20px;font-size:11px}.window-foot{display:grid}
.empty-showcase{grid-template-columns:1fr}
.cta{padding:34px 25px;display:block}
.cta .button{margin-top:24px}
.site-footer{grid-template-columns:1fr}
.stats-grid{grid-template-columns:1fr 1fr}
.leaderboard-head{display:none}
.leaderboard article{grid-template-columns:1fr auto}
.leaderboard article>span:nth-child(2){display:none}
.leaderboard article>.row-link{display:none}
.auth-intro h1{font-size:38px}
.auth-trust{align-items:flex-start}.auth-trust span{display:grid;justify-items:center;text-align:center;padding:0 9px;line-height:1.4}
.auth-grid{grid-template-columns:1fr}
.dash-nav{grid-template-columns:1fr 1fr}
.dash-grid,.dash-grid.three{grid-template-columns:1fr}
.setting-row{gap:12px;flex-wrap:wrap}
.setting-row>.button,.setting-row>.toggle,.setting-row>.status-chip{flex:0 0 auto}
.setting-copy{min-width:0;flex:1 1 220px}
.key-list li{gap:12px}
.key-list li>div{min-width:0;overflow-wrap:anywhere}
.verification-banner{display:block}
.verification-banner .button{margin-top:16px}
.app-header{padding:0 14px}
.app-header .button-small{padding:8px}
.app-main{padding-top:28px}
}

@media(max-width:420px){
.metric-grid{grid-template-columns:1fr}
.stats-grid{grid-template-columns:1fr}
.app-header-actions>a{display:none}
}

@media(prefers-reduced-motion:reduce){html{scroll-behavior:auto}
*,*:before,*:after{scroll-behavior:auto!important;transition-duration:.01ms!important;animation-duration:.01ms!important;animation-iteration-count:1!important}
}

`
