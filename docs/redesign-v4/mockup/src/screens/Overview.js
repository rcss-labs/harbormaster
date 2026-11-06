var Screen={state:{layout:H.pref('layout')||'triage',sev:'all',showAll:false,restart:null,group:'cluster',problems:true,q:'',collapsed:{}},vals:function(c,s){
 var ORDER={bad:0,warn:1,info:2,ok:3},SEV_L={bad:'Failing',warn:'Warning',info:'In progress'};
 var extra=[
  {sev:'bad',sevL:'Failing',kind:'acct',svc:'staging-example / us-east-1',cluster:'',title:'Service data is 4 min old.',why:'ecs:DescribeServices returned AccessDenied. Other data is current.',next:'Check the harbormaster-access role',nextHref:'SetAccounts.dc.html',age:'4 min ago',related:'first failed poll 4 min ago'},
  {sev:'warn',sevL:'Warning',kind:'svc',svc:'report-worker',cluster:'background-jobs',title:'CPU at 71% for 2 h.',why:'The only task runs above the 70% line and no scaling policy is set.',next:'Add a scaling policy',nextHref:'SvcScaling.dc.html',age:'2 h ago',related:'no change in the last 2 d'},
  {sev:'warn',sevL:'Warning',kind:'svc',svc:'jenkins-controller',cluster:'ci-tools',title:'Memory at 74%.',why:'Memory has risen since the task started 2 h ago.',next:'Raise task memory',nextHref:'SvcDeploy.dc.html',age:'2 h ago',related:'restarted by oncall 2 h earlier'}];
 var att=D.attention.map(function(a){return Object.assign({},a,{kind:'svc',nextHref:a.nextTab+'.dc.html'})}).concat(extra).sort(function(a,b){return ORDER[a.sev]-ORDER[b.sev]});
 var sevN=function(k){return att.filter(function(a){return k==='all'||a.sev===k}).length};
 var list=att.filter(function(a){return s.sev==='all'||a.sev===s.sev});
 var shown=s.showAll?list:list.slice(0,6);
 var closeDlg=H.set(c,{restart:null});
 var attRows=shown.map(function(a){var sv=a.kind==='svc'?D.svc(a.svc):null,blocked=a.sev==='info';
  return Object.assign({},a,{stripe:H.stripe(a.sev),isSvc:a.kind==='svc',isAcct:a.kind==='acct',restartDisabled:blocked,canRestart:!blocked,restartTitle:blocked?'A rollout is already running':'Restart '+a.svc,
   restart:function(){c.setState({restart:{name:a.svc,rev:sv?sv.rev:0,cluster:a.cluster,title:a.title}})}})});
 var cl=D.clusters,running=D.services.reduce(function(x,y){return x+y.running},0),desired=D.services.reduce(function(x,y){return x+y.desired},0);
 var missing=D.services.reduce(function(x,y){return x+Math.max(0,y.desired-y.running)},0)+D.tasks.filter(function(t){return t.health==='Unhealthy'}).length;
 var withIns=cl.filter(function(x){return x.insights});
 var avg=function(k){return Math.round(withIns.reduce(function(x,y){return x+y[k]},0)/withIns.length)};
 var need=D.services.filter(function(x){return x.status!=='ok'}).length;
 var normal=H.series(3,24,4,3).map(Math.round),errs=normal.map(function(_,i){return i===20?1:i===21?1:i>=22?2:0});
 var sev=function(list){return list.some(function(x){return x.status==='bad'})?'bad':list.some(function(x){return x.status==='warn'})?'warn':list.some(function(x){return x.status==='info'})?'info':'ok'};
 // Estate layout
 var q=s.q.toLowerCase(),byCluster=s.group==='cluster';
 var groups=byCluster?cl.map(function(x){return {key:x.name,title:x.name,hasTag:true,tag:x.tag,sub:x.acct+' · '+x.region,svcs:D.services.filter(function(v){return v.cluster===x.name})}}):D.accounts.map(function(a){var rg=[];D.clusters.forEach(function(x){if(x.acct===a.name&&rg.indexOf(x.region)<0)rg.push(x.region)});return {key:a.name,title:a.name,hasTag:false,tag:'',sub:rg.join(', '),svcs:D.services.filter(function(v){return v.acct===a.name})}});
 var rows=[],shownN=0,hiddenG=0;
 groups.forEach(function(g){
  var vis=g.svcs.filter(function(v){return (!s.problems||v.status!=='ok')&&(!q||(v.name+' '+v.cluster).toLowerCase().indexOf(q)>=0)});
  if(!vis.length){if(s.problems&&!q&&sev(g.svcs)==='ok')hiddenG++;return}
  var open=!s.collapsed[g.key],cnt=function(st){return g.svcs.filter(function(v){return v.status===st}).length},parts=[];
  if(cnt('bad'))parts.push(cnt('bad')+' failing');if(cnt('warn'))parts.push(cnt('warn')+' unhealthy');if(cnt('info'))parts.push(cnt('info')+' deploying');parts.push(g.svcs.length+' svc');
  rows.push({isGroup:true,isSvc:false,isNote:false,stripe:'',sev:sev(g.svcs),title:g.title,hasTag:g.hasTag,tag:g.tag,sub:g.sub,counts:parts.join(' · '),expanded:open,caret:open?'▾':'▸',toggleLabel:(open?'Collapse ':'Expand ')+g.title,
   toggle:function(){var m=Object.assign({},s.collapsed);m[g.key]=open;c.setState({collapsed:m})}});
  shownN+=vis.length;
  if(open){vis.forEach(function(v){rows.push({isGroup:false,isSvc:true,isNote:false,stripe:H.stripe(v.status),name:v.name,cluster:v.cluster,tasks:v.running+'/'+v.desired,status:v.status,statusL:v.statusL})});
   var hid=g.svcs.length-vis.length;if(s.problems&&hid>0&&!q)rows.push({isGroup:false,isSvc:false,isNote:true,stripe:'',text:hid+' steady services hidden'})}
 });
 var showAllSvcs=function(){c.setState({problems:false,q:''})};
 var lay=function(id,label){return {label:label,pressed:s.layout===id,pick:function(){H.pref('layout',id);c.setState({layout:id})}}};
 return {meta:D.accounts.length+' accounts · 3 regions · polled from AWS every 5 s · last poll 3 s ago',layoutSeg:[lay('triage','Triage'),lay('estate','Estate')],isTriage:s.layout==='triage',isEstate:s.layout==='estate',
  kClusters:cl.length,kClustersHealthy:cl.filter(function(x){return x.status==='ok'}).length,kServices:D.services.length,kSvcNote:need?need+' need attention':'all steady',kSvcCls:need?'c-warn':'c-ok',
  kRunning:running,kDesired:desired,kUnhealthy:missing,kCpu:avg('cpu'),kMem:avg('mem'),kErr:errs.reduce(function(a,b){return a+b},0),
  sTasks:H.spark(H.series(4,36,running,2)),sCpu:H.spark(H.series(6,36,36,5)),sMem:H.spark(H.series(7,36,55,4)),sErr:H.spark(errs),
  openN:att.length,sevSeg:H.seg(c,'sev',[['all','All',sevN('all')],['bad','Failing',sevN('bad')],['warn','Warning',sevN('warn')],['info','In progress',sevN('info')]]),
  attRows:attRows,attEmpty:!list.length,attMore:list.length>6,attShown:shown.length,attTotal:list.length,toggleAll:H.toggle(c,'showAll'),toggleAllL:s.showAll?'Show fewer':'Show all '+list.length,
  mapRows:D.accounts.map(function(a){var mine=cl.filter(function(x){return x.acct===a.name}),rg=[];mine.forEach(function(x){if(rg.indexOf(x.region)<0)rg.push(x.region)});var f=mine.filter(function(x){return x.status==='bad'}).length;
   return {name:a.name,meta:rg.length+(rg.length===1?' region · ':' regions · ')+mine.length+(mine.length===1?' cluster':' clusters'),hasFail:f>0,failL:f+' need'+(f===1?'s':'')+' attention',cells:mine.map(function(x){return {name:x.name,status:x.status,style:'background:var(--'+x.status+'-soft)',title:x.name+', '+x.statusL+', '+x.running+'/'+x.desired+' tasks, '+x.region}})}}),
  mapEmpty:!cl.length,mapLoading:false,
  stops:H.bars({keys:[{name:'Normal',color:'s1'},{name:'Error exit',color:'s2'}],rows:normal.map(function(n,i){return [n,errs[i]]}),x:['24 h ago','12 h','now'],w:520,h:170,tip:function(i){return (24-i)+' h ago'}}),
  busy:withIns.slice().sort(function(a,b){return b.cpu-a.cpu}).slice(0,5).map(function(x){return {name:x.name,sub:x.acct+' · '+x.region,cpu:x.cpu,bar:H.meterStyle(x.cpu),barCls:x.cpu>55?'warn':''}}),busyEmptyOff:!withIns.length,busyEmptyNone:false,
  reasons:D.stopReasons.map(function(r){return {label:r[0],n:r[1],bar:H.meterStyle(r[1]/15*100)+';background:var(--'+r[2]+')'}}),
  groupSeg:[{label:'Cluster',pressed:byCluster,pick:H.set(c,{group:'cluster'})},{label:'Account',pressed:!byCluster,pick:H.set(c,{group:'account'})}],problems:s.problems,toggleProblems:H.toggle(c,'problems'),q:s.q,onQ:function(e){c.setState({q:e.target.value})},
  estRows:rows,shownN:shownN,totalN:D.services.length,estEmpty:!shownN,estLoading:false,emptyText:s.problems&&!q?'No services have problems.':'No services match.',showAllSvcs:showAllSvcs,
  moreGroups:groups.length>25,groupN:groups.length,groupWord:byCluster?'clusters':'accounts',showMoreGroups:H.toast(c,'Showing more.'),hiddenClusters:hiddenG>0,hiddenClustersN:hiddenG,
  dlgRestart:!!s.restart,rs:s.restart||{name:'',rev:0,cluster:'',title:''},closeDlg:closeDlg,confirmRestart:function(){c.setState({restart:null});H.toast(c,'Restart started.')()}};
}};
