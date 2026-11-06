var Screen={state:{disp:false,src:false},vals:function(c,s){
 var sum=function(a){return a.reduce(function(p,q){return p+q},0)},r1=function(n){return Math.round(n*10)/10};
 /* Task counts per cluster agree with the Networks screen: core 10, background 3, ci 3, eu 3, edge 3, staging 2. */
 var tasksBy={'core-services':10,'background-jobs':3,'ci-tools':3,'eu-apps':3,'edge-proxy':3,'staging-web':2},spotBy={'ci-tools':'1 of 3','background-jobs':'1 of 3','staging-web':'2 of 2'};
 var cl=D.clusters,col=['s1','s2','s3','s4','s5','s6'];
 var vcpu=sum(cl.map(function(x){return x.vcpu})),mem=sum(cl.map(function(x){return x.gib})),tasks=sum(cl.map(function(x){return tasksBy[x.name]}));
 var kpis=[
  {k:'Reserved vCPU',v:String(vcpu),d:'across '+cl.length+' clusters'},
  {k:'Reserved memory',v:mem+' GiB',d:'from task definitions'},
  {k:'Running tasks',v:String(tasks),d:'in 14 services'},
  {k:'Spot share',v:r1(4/tasks*100)+'%',d:'tasks on FARGATE_SPOT'},
  {k:'At scaling max',v:'2',cls:'c-warn',d:'services cannot add tasks'},
  {k:'Subnets low on IPs',v:'1',cls:'c-bad',d:'under 10% addresses free'}];
 /* 7 days, twice a day: each cluster ramps to its current reservation on the last row. */
 var ramps=cl.map(function(x,k){return H.series(k+11,14,.9,.12)});
 var res7=[];for(var i=0;i<14;i++)res7.push(cl.map(function(x,k){return i===13?x.vcpu:r1(x.vcpu*Math.min(1,ramps[k][i]))}));
 var acct={};cl.forEach(function(x){acct[x.acct]=(acct[x.acct]||0)+x.vcpu});
 var acctBar=Object.keys(acct).map(function(n,k){return {name:n,v:acct[n]+' vCPU · '+Math.round(acct[n]/vcpu*100)+'%',bar:'flex:'+acct[n]+' 1 0;background:var(--'+col[k===0?0:2]+')',sw:'background:var(--'+col[k===0?0:2]+')'}});
 var spotN=4,provBar=[{name:'FARGATE',v:(tasks-spotN)+' tasks · '+Math.round((tasks-spotN)/tasks*100)+'%',n:tasks-spotN,c:'s1'},{name:'FARGATE_SPOT',v:spotN+' tasks · '+Math.round(spotN/tasks*100)+'%',n:spotN,c:'s4'}].map(function(p){return {name:p.name,v:p.v,bar:'flex:'+p.n+' 1 0;background:var(--'+p.c+')',sw:'background:var(--'+p.c+')'}});
 var fix=function(a,last){a[a.length-1]=last;return a};
 var ref90=fix(H.series(3,90,43,1.2),vcpu),p95=fix(H.series(4,90,21,2),21.4),avg=fix(H.series(5,90,13,1.5),13.2),x90=['90 d ago','45 d','now'];
 var memRes=fix(H.series(6,90,89,2),mem),memP95=fix(H.series(7,90,52,3),53.4);
 /* [service, tasks, vCPU per task, CPU p95 (vCPU total), GiB per task, memory p95 (GiB total)] */
 var O=[['payments-api',3,1,1.4,2,3.9],['jenkins-controller',1,2,.6,8,5.9],['web-frontend',2,1,.7,2,2.1],['catalog-api',2,1,.8,2,2.7],['notifications',3,.5,.5,1,1.4],['search-indexer',1,2,1.1,8,6.4],['auth-service',2,.5,.3,1,1],['report-worker',1,2,1.9,4,3.1]];
 var over=O.map(function(o){var rv=o[1]*o[2],rm=o[1]*o[4],sv=D.svc(o[0]);return {svc:o[0],cluster:sv.cluster,tag:sv.tag,tagCls:H.tagClass(sv.tag,sv.inherited),u:rv-o[3],rv:rv.toFixed(1),cpu:o[3].toFixed(1)+' / '+rv.toFixed(1)+' ('+Math.round(o[3]/rv*100)+'%)',rm:rm+' GiB',mem:o[5].toFixed(1)+' / '+rm+' GiB ('+Math.round(o[5]/rm*100)+'%)',unused:(rv-o[3]).toFixed(1)}}).sort(function(a,b){return b.u-a.u});
 var clusters=cl.map(function(x,k){return {name:x.name,acct:x.acct,tag:x.tag,tagCls:H.tagClass(x.tag),tasks:tasksBy[x.name],vcpu:x.vcpu,mem:x.gib,share:Math.round(x.vcpu/vcpu*100)+'%',bar:H.meterStyle(x.vcpu/vcpu*100)+';background:var(--'+col[k]+')',sw:'background:var(--'+col[k]+')',spot:spotBy[x.name]||'0'}}).sort(function(a,b){return b.vcpu-a.vcpu});
 var atMax=[{svc:'payments-api',dm:'3 / 3',dur:'5 h'},{svc:'catalog-api',dm:'2 / 2',dur:'3 d'}];
 var low=[{name:'private-1a',id:'subnet-03f1a9c2',vpc:'homelab-main',zone:'us-east-1a',free:'19 (7.6%)',used:'232 of 251',bar:H.meterStyle(232/251*100)}];
 return {disp:s.disp,src:s.src,toggleDisp:H.toggle(c,'disp'),toggleSrc:H.toggle(c,'src'),kpis:kpis,since:'8 Aug 2026',vcpuTotal:vcpu,taskTotal:tasks,
  res7:H.bars({keys:cl.map(function(x,k){return {name:x.name,color:col[k]}}),rows:res7,x:['7 d ago','3 d','now'],w:1140,h:190,unit:'',tip:function(k){return k===13?'now':(13-k)/2+' d ago'}}),
  acctBar:acctBar,provBar:provBar,
  use90:H.chart({series:[{name:'Reserved',color:'s1',values:ref90},{name:'Used p95',color:'s3',values:p95},{name:'Used average',color:'s4',values:avg}],x:x90,w:1140,h:190,unit:' vCPU',max:50}),
  spot90:H.chart({series:[{name:'Spot share',color:'s4',values:fix(H.series(8,90,15,2.5),r1(spotN/tasks*100)),area:true}],x:x90,w:560,h:170,unit:'%',max:30}),
  mem90:H.chart({series:[{name:'Reserved',color:'s1',values:memRes},{name:'Used p95',color:'s3',values:memP95}],x:x90,w:560,h:170,unit:' GiB',max:120}),
  over:over,clusters:clusters,noClusters:!clusters.length,atMax:atMax,atMaxN:atMax.length,noAtMax:!atMax.length,low:low,noLow:!low.length,
  used:[{t:'task_counts',what:'Hourly task counts by service and capacity provider',kept:'90 days',setting:'Capacity history'},{t:'task_definitions',what:'CPU and memory reserved by each service',kept:'Latest revision',setting:'Not configurable'},{t:'task_metrics_daily',what:'Daily CPU and memory use, average and p95',kept:'90 days',setting:'Capacity history'},{t:'scaling_targets',what:'Auto-scaling min, max and desired',kept:'Latest poll',setting:'Not configurable'},{t:'subnets',what:'Free and used IP addresses per subnet',kept:'Latest poll',setting:'Not configurable'}]};
}};
