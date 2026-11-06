/* Integer split of total into n buckets, deterministic by seed. */
function dist(total,n,seed){var w=[],sum=0,x=(seed*7919)%1000/1000+.13,i;for(i=0;i<n;i++){x=(x*9301+49297)%233280/233280;w.push(.15+x);sum+=w[i]}var a=w.map(function(v){return Math.floor(total*v/sum)}),left=total-a.reduce(function(p,q){return p+q},0),ord=w.map(function(v,k){return [total*v/sum-a[k]%1,k]}).sort(function(p,q){return q[0]-p[0]});for(i=0;left>0;i++,left--)a[ord[i%n][1]]++;return a}
var Screen={state:{disp:false,src:false},vals:function(c,s){
 var sum=function(a){return a.reduce(function(p,q){return p+q},0)},W=13,xs=['13 wk ago','6 wk','now'];
 /* Operation type -> series colour. Restart, stop and terminal keep their colour in the hands-on chart. */
 var T=[['deploy',75,'s1',1],['rollback',7,'s5',2],['task_definition',61,'s3',3],['scale',38,'s4',4],['restart',17,'s2',5],['stop_task',9,'s6',6],['terminal',29,'mute',7]];
 var by={};T.forEach(function(t){by[t[0]]=dist(t[1],W,t[3])});
 var rows=[];for(var i=0;i<W;i++)rows.push(T.map(function(t){return by[t[0]][i]}));
 var total=sum(T.map(function(t){return t[1]}));
 var ratio=by.restart.map(function(r,k){return by.deploy[k]?Math.round(r/by.deploy[k]*1000)/10:0});
 var P=[['pavel',85,28,5,14,6,'just now'],['dana',79,31,8,15,3,'37 min ago'],['omer',42,14,1,7,0,'3 min ago'],['oncall',30,2,3,2,20,'2 h ago']];
 var R=[['image-resizer',6,3,'18 min ago'],['search-indexer',4,2,'1 d ago'],['catalog-api',3,2,'4 d ago'],['report-worker',2,1,'12 d ago'],['notifications',1,1,'9 d ago'],['email-sender',1,1,'31 d ago']];
 var kpis=[
  {k:'Operations',v:String(total),d:'by '+P.length+' people'},
  {k:'Revisions registered',v:'61',d:'new task definition revisions'},
  {k:'Scale changes',v:'38',d:'desired count or limits changed'},
  {k:'Restarts',v:'17',d:'force new deployment'},
  {k:'Tasks stopped by hand',v:'9',d:'stopped from Harbormaster'},
  {k:'Terminal sessions',v:'29',d:'opened with ECS Exec'}];
 return {disp:s.disp,src:s.src,toggleDisp:H.toggle(c,'disp'),toggleSrc:H.toggle(c,'src'),kpis:kpis,
  ops:H.bars({keys:T.map(function(t){return {name:t[0],color:t[2]}}),rows:rows,x:xs,w:1140,h:200,tip:function(k){return (W-1-k)+' wk ago'}}),
  opsTotals:T.map(function(t){return {name:t[0],n:t[1],sty:'background:var(--'+t[2]+')'}}),
  hands:H.bars({keys:[{name:'restart',color:'s2'},{name:'stop_task',color:'s6'},{name:'terminal',color:'mute'}],rows:rows.map(function(r){return [r[4],r[5],r[6]]}),x:xs,w:560,h:170,tip:function(k){return (W-1-k)+' wk ago'}}),
  ratio:H.chart({series:[{name:'Restarts per 100 deploys',color:'s2',values:ratio,area:true}],x:xs,w:560,h:170}),
  people:P.map(function(p){return {name:p[0],total:p[1],deploy:p[2],restart:p[3],scale:p[4],terminal:p[5],last:p[6]}}),noPeople:!P.length,
  restarted:R.map(function(r){return {svc:r[0],n:r[1],people:r[2],last:r[3]}}),noRestarted:!R.length,
  used:[{t:'audit_log',what:'Every deploy, rollback, scale, restart, stop and terminal action with its user',kept:'90 days',setting:'Audit log retention'},{t:'task_definition_revisions',what:'Revisions registered through Harbormaster',kept:'90 days',setting:'Audit log retention'}]};
}};
