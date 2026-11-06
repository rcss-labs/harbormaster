var Screen={state:{container:'all',level:'all',q:'',tail:'live',st:'normal',open:null,older:false,wide:true},vals:function(c,s){
 var BASE='display:grid;grid-template-columns:66px 40px minmax(120px,230px) 1fr;gap:10px;width:100%;text-align:left;padding:2px 12px;border:0;border-bottom:1px solid var(--row-border);background:transparent;color:var(--text);font:400 12px/1.5 var(--mono);cursor:pointer;align-items:start;';
 var TASKS=['9f2e1a3b','b81c7d40','e30a95d1'];
 var MSG={
  info:['POST /v1/charges 201 143ms provider=stripe','GET /health 200 2ms','GET /api/v1/items 200 41ms','POST /v1/refunds 200 96ms provider=stripe','GET /v1/charges/ch_3Q8x 200 38ms','cache hit ratio 0.94 over 60s','worker pool idle=6 busy=2','POST /v1/webhooks/stripe 202 12ms'],
  warn:['slow query took 812ms: SELECT * FROM items WHERE owner_id = $1','upstream timeout after 5000ms (retrying 1/3)','connection pool at 90% (18/20)','retrying webhook delivery attempt=2 endpoint=billing-hooks'],
  error:['card_declined code=insufficient_funds charge=ch_3Q8x','upstream timeout after 5000ms (retries exhausted) service=ledger','panic recovered: nil pointer in refund handler request_id=9a41f2']
 };
 var OTEL=['exported 128 spans in 41ms','exported 64 metrics in 18ms'];
 var mk=function(k){
  var sec=11*3600+42*60+18-Math.round(k*7.3+((k*37)%5)),r=(k*7919)%100,lvl=r<6?'error':r<22?'warn':'info',otel=k%5===3;
  if(otel&&lvl==='error')lvl='warn';
  var arr=otel?OTEL:MSG[lvl],msg=arr[(k*13)%arr.length],task=TASKS[k%3];
  var hh=Math.floor(sec/3600),mm=Math.floor(sec%3600/60),ss=sec%60,p=function(n){return (n<10?'0':'')+n};
  return {k:k,sec:sec,min:hh*60+mm,t:p(hh)+':'+p(mm)+':'+p(ss),lvl:otel&&lvl==='error'?'warn':lvl,container:otel?'otel-collector':'payments-api',task:task,msg:msg,stream:(otel?'otel-collector':'payments-api')+'/'+task};
 };
 var N=s.older?64:40,lines=[];for(var i=0;i<N;i++)lines.push(mk(i));lines.reverse();
 var mark={info:'INF',warn:'WRN',error:'ERR'},q=s.q.toLowerCase();
 var base=lines.filter(function(l){return (s.container==='all'||l.container===s.container)&&(!q||(l.msg+' '+l.stream).toLowerCase().indexOf(q)>=0)});
 var shown=base.filter(function(l){return s.level==='all'||(s.level==='warn'&&l.lvl!=='info')||(s.level==='error'&&l.lvl==='error')});
 var emptyState=s.st==='empty';
 if(emptyState)shown=[];
 var rows=shown.map(function(l){
  var isOpen=s.open===l.k+':'+l.container;
  return {key:l.k,t:l.t,mk:mark[l.lvl],lvl:l.lvl,sty:BASE+({warn:'background:var(--warn-soft);',error:'background:var(--bad-soft);box-shadow:inset 3px 0 0 var(--bad);',info:''}[l.lvl]),mkSty:'font-weight:600;letter-spacing:.02em;color:'+({warn:'var(--warn)',error:'var(--bad)',info:'var(--muted)'}[l.lvl]),stream:l.stream,msg:l.msg,open:isOpen,aria:'Details for the line at '+l.t,exp:isOpen,
   pick:function(){c.setState({open:isOpen?null:l.k+':'+l.container})},
   d:{level:{info:'Info',warn:'Warning',error:'Error'}[l.lvl],group:'/ecs/payments-api',stream:'ecs/'+l.stream.replace('/','/'),task:l.task,container:l.container,time:'2026-11-06 '+l.t+'.'+(100+(l.k*37)%900)+' UTC',ingested:'+'+(0.4+(l.k%4)*.2).toFixed(1)+' s',full:'{"level":"'+l.lvl+'","msg":"'+l.msg.replace(/"/g,"'")+'","task":"'+l.task+'","rev":42}'}};
 });
 // histogram: per minute, stacked by level, uses all lines matching container and search
 var byMin={},mins=[];base.forEach(function(l){if(!byMin[l.min]){byMin[l.min]={info:0,warn:0,error:0};mins.push(l.min)}byMin[l.min][l.lvl]++});
 mins.sort(function(a,b){return a-b});
 var mx=Math.max.apply(null,mins.map(function(m){var b=byMin[m];return b.info+b.warn+b.error}).concat([1]));
 var colr={info:'var(--muted)',warn:'var(--warn)',error:'var(--bad)'},nm={info:'Info',warn:'Warnings',error:'Errors'};
 var pad=function(n){return (n<10?'0':'')+n};
 var hist=emptyState?[]:mins.map(function(m){var b=byMin[m],segs=['info','warn','error'].filter(function(k){return b[k]}).map(function(k){return {style:'height:'+Math.max(2,Math.round(b[k]/mx*56))+'px;background:'+colr[k]+';'+(k==='info'?'opacity:.55':''),title:nm[k]+': '+b[k]}});
  var lab=pad(Math.floor(m/60))+':'+pad(m%60);return {segs:segs,tip:lab+' · '+b.info+' info, '+b.warn+' warnings, '+b.error+' errors',label:lab}});
 var totals={info:0,warn:0,error:0};base.forEach(function(l){totals[l.lvl]++});
 var cont=s.container,STATES=[['normal','Normal'],['unsupported','Unsupported containers'],['idle','Live tail stopped after idle'],['session','Session changed'],['disconnected','Live tail disconnected'],['connecting','Connecting'],['empty','No log lines'],['error','Logs failed to load']];
 var stt=s.st,tailTxt=stt==='connecting'?'connecting':(stt==='idle'||stt==='disconnected')?'tail stopped':s.tail==='live'?'live tail':'paused';
 var hasSel=cont!=='all'&&cont;
 return {
  conts:[['all','All containers',false],['payments-api','payments-api',false],['otel-collector','otel-collector',false],['datadog-agent','datadog-agent (unsupported)',true],['log-router','log-router (unsupported)',true]].map(function(o){return {id:o[0],label:o[1],dis:o[2]}}),
  container:s.container,onContainer:function(e){c.setState({container:e.target.value,open:null})},
  level:H.seg(c,'level',[['all','All'],['warn','Warn+'],['error','Errors']]),
  tail:H.seg(c,'tail',[['live','Live'],['paused','Paused']]),
  q:s.q,onQ:function(e){c.setState({q:e.target.value})},
  status:'CloudWatch Logs · /ecs/payments-api · '+tailTxt,
  states:STATES.map(function(o){return {id:o[0],label:o[1]}}),st:s.st,onSt:function(e){c.setState({st:e.target.value})},
  bUnsup:stt==='unsupported',bIdle:stt==='idle',bSession:stt==='session',bDisc:stt==='disconnected',bErr:stt==='error',bConn:stt==='connecting',
  resume:H.set(c,{st:'normal',tail:'live'}),retry:H.set(c,{st:'normal'}),
  showLog:stt!=='error',rows:rows,none:!rows.length&&stt!=='error',hist:hist,hasHist:!!hist.length,
  sumInfo:totals.info,sumWarn:totals.warn,sumErr:totals.error,count:rows.length,
  older:s.older,loadOlder:function(){c.setState({older:true});H.toast(c,'Loaded 24 older lines.')()},noOlder:!s.older,
  clear:H.set(c,{q:'',level:'all',container:'all',st:'normal'})
 };
}};
