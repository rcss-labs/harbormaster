/* Integer split of total into n buckets, deterministic by seed. */
function dist(total,n,seed){var w=[],sum=0,x=(seed*7919)%1000/1000+.13,i;for(i=0;i<n;i++){x=(x*9301+49297)%233280/233280;w.push(.15+x);sum+=w[i]}var a=w.map(function(v){return Math.floor(total*v/sum)}),left=total-a.reduce(function(p,q){return p+q},0),ord=w.map(function(v,k){return [total*v/sum-a[k]%1,k]}).sort(function(p,q){return q[0]-p[0]});for(i=0;left>0;i++,left--)a[ord[i%n][1]]++;return a}
var Screen={state:{disp:false,src:false,all:false},vals:function(c,s){
 var DAYS=30,sum=function(a){return a.reduce(function(p,q){return p+q},0)};
 /* Reason -> series colour; shared with the Reliability stop-reason charts where the same cause appears. */
 var R={cb:['Circuit breaker','s6'],hc:['Health check','s4'],fs:['Failed to start','s3'],oom:['Out of memory','s2'],user:['Stopped by user','mute'],other:['Other','s1']};
 var order=['cb','hc','fs','oom','user','other'];
 /* Every failed rollout in the 30 day range (daysAgo drives the per-day chart). */
 var F=[
  {d:0,when:'18 min ago',svc:'image-resizer',rev:':5 → :6',r:'oom',detail:'OutOfMemoryError: Container killed due to memory usage',ran:'2m 40s'},
  {d:2,when:'2 d ago',svc:'search-indexer',rev:':13 → :14',r:'fs',detail:'ResourceInitializationError: unable to pull secrets',ran:'1m 05s'},
  {d:4,when:'4 d ago',svc:'catalog-api',rev:':22 → :23',r:'hc',detail:'Health checks failed with these codes: [503]',ran:'9m 50s'},
  {d:6,when:'6 d ago',svc:'image-resizer',rev:':4 → :5',r:'oom',detail:'OutOfMemoryError: Container killed due to memory usage',ran:'3m 12s'},
  {d:9,when:'9 d ago',svc:'notifications',rev:':8 → :9',r:'cb',detail:'Tasks failed to reach RUNNING; rolled back to :8',ran:'12m 30s'},
  {d:12,when:'12 d ago',svc:'report-worker',rev:':30 → :31',r:'user',detail:'Deployment stopped by pavel',ran:'4m 18s'},
  {d:15,when:'15 d ago',svc:'catalog-api',rev:':21 → :22',r:'hc',detail:'Health checks failed with these codes: [503]',ran:'10m 02s'},
  {d:19,when:'19 d ago',svc:'search-indexer',rev:':12 → :13',r:'cb',detail:'Tasks failed to reach RUNNING; rolled back to :12',ran:'14m 41s'},
  {d:23,when:'23 d ago',svc:'image-resizer',rev:':3 → :4',r:'cb',detail:'Tasks failed to reach RUNNING; rolled back to :3',ran:'11m 09s'}
 ];
 var failedDay=[],i;for(i=0;i<DAYS;i++)failedDay.push(0);
 var whyRows=order.map(function(){var a=[];for(var k=0;k<DAYS;k++)a.push(0);return a});
 F.forEach(function(f){var idx=DAYS-1-f.d;failedDay[idx]++;whyRows[order.indexOf(f.r)][idx]++});
 var completed=dist(124,DAYS,3),superseded=dist(9,DAYS,8);
 var rows=[];for(i=0;i<DAYS;i++)rows.push([completed[i],failedDay[i],superseded[i]]);
 var tot=sum(completed)+sum(failedDay)+sum(superseded);
 var cfrOver=function(a,b){var cp=sum(completed.slice(a,b)),fl=sum(failedDay.slice(a,b));return cp+fl?fl/(cp+fl)*100:0};
 var cfr=[];for(i=0;i<DAYS;i++)cfr.push(Math.round(cfrOver(Math.max(0,i-6),i+1)*10)/10);
 var overall=Math.round(sum(failedDay)/(sum(completed)+sum(failedDay))*1000)/10;
 var med=H.series(4,DAYS,4.3,.5),p90=H.series(9,DAYS,9.6,1.1);med[DAYS-1]=4.2;p90[DAYS-1]=9.7;
 var ref=[];for(i=0;i<DAYS;i++)ref.push(5);
 var xs=['30 d ago','20 d','10 d','now'],tag=function(n){return D.svc(n).tag};
 var kpis=[
  {k:'Rollouts',v:String(tot),d:(tot/DAYS).toFixed(1)+' per day'},
  {k:'Change failure rate',v:overall+'%',cls:overall>5?'c-warn':'',d:'failed ÷ (completed + failed), amber above 5%'},
  {k:'Median rollout',v:'4m 12s',d:'p90 9m 42s'},
  {k:'Time to recover',v:'11m',d:'median, failed → next completed'},
  {k:'Rollbacks',v:'4',d:'deploys to a lower revision'},
  {k:'Superseded',v:String(sum(superseded)),d:'replaced before finishing'}];
 /* per-service: [svc, rollouts, superseded, median, recover] */
 var S=[['image-resizer',9,1,'6m 20s','38m'],['catalog-api',12,0,'7m 05s','22m'],['search-indexer',8,0,'5m 48s','1 h 12m'],['notifications',21,3,'4m 30s','14m'],['report-worker',10,0,'3m 10s','9m'],['payments-api',24,2,'4m 55s','–'],['web-frontend',17,1,'3m 40s','–'],['auth-service',6,0,'2m 50s','–'],['email-sender',4,0,'2m 15s','–'],['jenkins-controller',19,2,'6m 02s','–'],['artifact-proxy',2,0,'1m 30s','–'],['web',4,0,'2m 05s','–'],['api',6,0,'2m 25s','–']];
 var svcAll=S.map(function(x){var sv=D.svc(x[0]),fl=F.filter(function(f){return f.svc===x[0]}),den=x[1]-x[2],rate=den?fl.length/den*100:0;
  return {svc:x[0],cluster:sv.cluster,tag:sv.tag,tagCls:H.tagClass(sv.tag,sv.inherited),rollouts:x[1],failed:fl.length,rateN:rate,rate:(Math.round(rate*10)/10)+'%',bar:H.meterStyle(Math.min(100,rate)),cls:rate>20?'bad':rate>10?'warn':'',stripe:rate>20?'stripe-bad':rate>10?'stripe-warn':'',median:x[3],recover:fl.length?x[4]:'–',last:fl.length?fl[0].when:'–'}})
  .sort(function(a,b){return b.failed-a.failed||b.rateN-a.rateN||b.rollouts-a.rollouts});
 var shown=s.all?svcAll:svcAll.slice(0,8);
 var kept='90 days';
 return {disp:s.disp,src:s.src,toggleDisp:H.toggle(c,'disp'),toggleSrc:H.toggle(c,'src'),kpis:kpis.map(function(k){return Object.assign({cls:''},k)}),
  roll:H.bars({keys:[{name:'Completed',color:'s1'},{name:'Failed',color:'s5'},{name:'Superseded',color:'mute'}],rows:rows,x:xs,w:1140,h:190,tip:function(k){return (DAYS-1-k)+' d ago'}}),
  cfr:H.chart({series:[{name:'Change failure rate, 7-day rolling',color:'s1',values:cfr,area:true},{name:'5% reference',color:'mute',values:ref,dash:true}],x:xs,w:560,h:170,unit:'%',max:30}),
  dur:H.chart({series:[{name:'Median',color:'s1',values:med},{name:'p90',color:'s4',values:p90}],x:xs,w:560,h:170,unit:' min',max:14}),
  why:H.bars({keys:order.map(function(k){return {name:R[k][0],color:R[k][1]}}),rows:rows.map(function(_,k){return whyRows.map(function(w){return w[k]})}),x:xs,w:1140,h:170,max:2,tip:function(k){return (DAYS-1-k)+' d ago'}}),
  whyTotals:order.map(function(k,j){return {name:R[k][0],n:sum(whyRows[j]),sty:'background:var(--'+R[k][1]+')'}}),
  failedN:F.length,failed:F.map(function(f){return Object.assign({},f,{reason:R[f.r][0],sty:'background:var(--'+R[f.r][1]+')'})}),noFailed:!F.length,
  svcRows:shown,noSvc:!svcAll.length,svcShown:shown.length,svcTotal:svcAll.length,canToggle:svcAll.length>8,allLabel:s.all?'Show top 8':'Show all',toggleAll:H.toggle(c,'all'),
  used:[{t:'service_deployments',what:'Rollout outcome, revisions, start and end times, rollbacks',kept:kept,setting:'Deployment history'},{t:'deployment_failures',what:'Failure reason and detail for each failed rollout',kept:kept,setting:'Deployment history'},{t:'service_events',what:'Service events used to find circuit breaker and health check causes',kept:kept,setting:'Event history'}]};
}};
