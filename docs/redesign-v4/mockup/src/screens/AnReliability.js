/* Integer split of total into n buckets, deterministic by seed. */
function dist(total,n,seed){var w=[],sum=0,x=(seed*7919)%1000/1000+.13,i;for(i=0;i<n;i++){x=(x*9301+49297)%233280/233280;w.push(.15+x);sum+=w[i]}var a=w.map(function(v){return Math.floor(total*v/sum)}),left=total-a.reduce(function(p,q){return p+q},0),ord=w.map(function(v,k){return [total*v/sum-a[k]%1,k]}).sort(function(p,q){return q[0]-p[0]});for(i=0;left>0;i++,left--)a[ord[i%n][1]]++;return a}
var Screen={state:{disp:false,src:false},vals:function(c,s){
 var sum=function(a){return a.reduce(function(p,q){return p+q},0)},r1=function(n){return Math.round(n*10)/10},r2=function(n){return Math.round(n*100)/100};
 var N=7,i,xs7=['7 d ago','3 d','now'];
 /* Availability, 6-hourly points; the dip on the last day is image-resizer running out of memory. */
 var avail=H.series(2,28,99.95,.06).map(function(v){return Math.min(100,v)});[99.3,98.4,98.9,99.4].forEach(function(v,k){avail[24+k]=v});
 var mean=avail.reduce(function(p,q){return p+q},0)/avail.length,belowH=(100-mean)/100*15*168;
 var below=[];for(i=0;i<28;i++)below.push(0);below[10]=1;below[11]=1;below[17]=1;[24,25,26,27].forEach(function(k){below[k]=1});below[25]=2;
 var ref=function(n,v){var a=[];for(var k=0;k<n;k++)a.push(v);return a};
 /* Stop reasons per day. Out of memory and health check rows are the unexpected stops. */
 var oom=[0,0,0,0,1,1,4],lb=[0,1,1,0,1,0,0],spot=[0,0,1,0,0,1,0],scaled=[0,1,0,0,0,0,1],user=[0,0,0,0,0,1,0],dep=dist(15,N,5);
 var keys=[['Replaced by a deployment','mute',dep],['Scaled in','mute',scaled],['Stopped by a user','mute',user],['Spot interruption','mute',spot],['Out of memory (exit 137)','s2',oom],['Failed load balancer health checks','s4',lb]];
 var perDay=[];for(i=0;i<N;i++)perDay.push(keys.reduce(function(a,k){return a+k[2][i]},0));
 var c1=[0,0,1,0,0,1,1],c255=[0,0,0,1,0,0,0],cO=[0,1,0,0,0,0,1];
 var c143=perDay.map(function(p,k){return p-oom[k]-c1[k]-c255[k]-cO[k]});
 var stopsTotal=sum(perDay),unexp=sum(oom)+sum(lb);
 var kpis=[
  {k:'Availability',v:r2(mean)+'%',cls:mean<99.5?'c-warn':'',d:'5-min samples with running ≥ desired'},
  {k:'Time below desired',v:Math.floor(belowH)+'h '+Math.round((belowH%1)*60)+'m',d:'summed across services, 7 days'},
  {k:'Task stops',v:r1(stopsTotal/N)+'',d:'per day, '+stopsTotal+' in 7 days'},
  {k:'Unexpected stops',v:Math.round(unexp/stopsTotal*100)+'%',cls:'c-warn',d:'of stops, '+unexp+' in 7 days'},
  {k:'Out of memory',v:String(sum(oom)),cls:'c-bad',d:'exit 137 / OutOfMemoryError'},
  {k:'Placement failures',v:'4',d:'from ECS service events'}];
 /* 90 days of daily data, rollups started on 8 Aug 2026. */
 var a90=H.series(6,90,99.9,.12).map(function(v){return Math.min(100,v)}),b90=H.series(7,90,.9,.5);
 var tg=function(n){var v=D.svc(n);return {svc:n,cluster:v.cluster,tag:v.tag,tagCls:H.tagClass(v.tag,v.inherited)}};
 var W=[['image-resizer',99.92,96.4],['search-indexer',99.71,98.84],['catalog-api',99.95,99.41],['report-worker',99.98,99.8],['notifications',99.9,99.76]];
 var worse=W.map(function(w){var d=r2(w[2]-w[1]);return Object.assign(tg(w[0]),{prev:w[1].toFixed(2)+'%',now:w[2].toFixed(2)+'%',change:d.toFixed(2)+' pts',chCls:d<-1?'c-bad':'c-warn',stripe:d<-1?'stripe-bad':'stripe-warn'})});
 var L=[['image-resizer',98.2,'2h 55m',6,'Out of memory (exit 137)','s2'],['search-indexer',99.2,'1h 04m',0,'Container exited with an error','s6'],['catalog-api',99.5,'0h 34m',3,'Failed load balancer health checks','s4'],['report-worker',99.9,'0h 10m',0,'Replaced by a deployment','mute'],['notifications',99.93,'0h 07m',0,'Replaced by a deployment','mute']];
 var least=L.map(function(l){return Object.assign(tg(l[0]),{avail:l[1].toFixed(l[1]>=99.9?2:1)+'%',bar:H.meterStyle(l[1]),cls:l[1]<99?'bad':l[1]<99.5?'warn':'',stripe:l[1]<99?'stripe-bad':l[1]<99.5?'stripe-warn':'',below:l[2],stops:l[3],reason:l[4],sty:'background:var(--'+l[5]+')'})});
 var kept7='7 days',kept90='90 days';
 return {disp:s.disp,src:s.src,toggleDisp:H.toggle(c,'disp'),toggleSrc:H.toggle(c,'src'),kpis:kpis,since:'8 Aug 2026',sinceSrc:'service_daily',
  avail:H.chart({series:[{name:'Availability',color:'s1',values:avail,area:true},{name:'99.5% reference',color:'mute',values:ref(28,99.5),dash:true}],x:xs7,w:560,h:170,unit:'%',max:100}),
  below:H.chart({series:[{name:'Services below desired',color:'s2',values:below,area:true}],x:xs7,w:560,h:170,max:3}),
  stops:H.bars({keys:keys.map(function(k){return {name:k[0],color:k[1]}}),rows:perDay.map(function(_,k){return keys.map(function(q){return q[2][k]})}),x:xs7,w:1140,h:190,tip:function(k){return (N-1-k)+' d ago'}}),
  stopTotals:keys.map(function(k){return {name:k[0],n:sum(k[2]),sty:'background:var(--'+k[1]+')'}}),
  zones:H.chart({series:[{name:'us-east-1a',color:'s1',values:[0,0,0,0,1,1,4]},{name:'eu-west-1a',color:'s3',values:[0,1,0,0,1,0,0]},{name:'eu-west-1b',color:'s5',values:[0,0,1,0,0,0,0]}],x:['7 d ago','3 d','now'],w:560,h:170,max:5}),
  exits:H.bars({keys:[{name:'1',color:'s6'},{name:'137',color:'s2'},{name:'143',color:'mute'},{name:'255',color:'s5'},{name:'Other',color:'s3'}],rows:perDay.map(function(_,k){return [c1[k],oom[k],c143[k],c255[k],cO[k]]}),x:xs7,w:560,h:170,tip:function(k){return (N-1-k)+' d ago'}}),
  avail90:H.chart({series:[{name:'Availability, daily',color:'s1',values:a90,area:true},{name:'99.5% reference',color:'mute',values:ref(90,99.5),dash:true}],x:['90 d ago','45 d','now'],w:560,h:170,unit:'%',max:100}),
  below90:H.chart({series:[{name:'Hours below desired',color:'s2',values:b90,area:true}],x:['90 d ago','45 d','now'],w:560,h:170,unit:' h'}),
  worse:worse,least:least,noLeast:!least.length,
  used:[{t:'service_samples',what:'5-minute samples of running and desired task counts',kept:kept7,setting:'Sample history'},{t:'stopped_tasks',what:'Stop reason, exit code and zone of every stopped task',kept:kept7,setting:'Stopped task history'},{t:'service_daily',what:'Daily availability and time below desired, rolled up',kept:kept90,setting:'Daily rollups'}]};
}};
