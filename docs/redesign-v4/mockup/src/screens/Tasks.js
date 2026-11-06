var Screen={state:{seg:'problems',q:'',group:true,open:{},colsOpen:false,hide:{},view:'default',pv:'normal',sel:null,stopId:null,stoppedIds:{},hiddenNote:true},vals:function(c,s){
 var CAUSE={'Replaced by a deployment':'deploy','Spot interruption':'spot','Out of memory (exit 137)':'oom','Failed load balancer health checks':'health','Stopped by a user':'user','Scaled in':'scale'};
 var base=D.tasks.map(function(t){return Object.assign({},t)});
 var extra=[['2c91d7e05fb1','7 min ago','24 min ago'],['58a0e3c4d612','31 min ago','37 min ago'],['e7b2640fa9c3','44 min ago','50 min ago'],['1d5f8a92b0e7','52 min ago','58 min ago'],['93c4e1d07a5b','2 h ago','1 h ago']].map(function(x,i){return {id:x[0],svc:'image-resizer',cluster:'background-jobs',status:'bad',statusL:'Stopped (exit 137)',cause:null,health:'–',rev:6,ip:'10.20.2.'+(60+i),subnet:'private-1b',az:'us-east-1b',cap:'FARGATE',cpu:null,mem:null,started:x[1],stopped:x[2],reason:'Out of memory (exit 137)',raw:'OutOfMemoryError: Container killed due to memory usage'}});
 extra.push({id:'a8d03b6e91f4',svc:'search-indexer',cluster:'eu-apps',status:'bad',statusL:'Stopped (exit 1)',health:'–',rev:14,ip:'10.30.1.73',subnet:'private-1a',az:'eu-west-1a',cap:'FARGATE',cpu:null,mem:null,started:'1 h ago',stopped:'42 min ago',reason:'Container exited with an error',raw:'Essential container in task exited'});
 extra.push({id:'f40c92a7d5e8',svc:'email-sender',cluster:'background-jobs',status:'exp',statusL:'Stopped (exit 143)',health:'–',rev:12,ip:'10.20.1.29',subnet:'private-1a',az:'us-east-1a',cap:'FARGATE',cpu:null,mem:null,started:'9 d ago',stopped:'33 min ago',reason:'Scaled in',raw:'Scaling activity initiated by (deployment ecs-svc/7720)'});
 extra.push({id:'6be19c03a7d2',svc:'web',cluster:'staging-web',status:'exp',statusL:'Stopped (exit 143)',health:'–',rev:33,ip:'10.50.1.31',subnet:'private-1a',az:'us-east-1a',cap:'FARGATE_SPOT',cpu:null,mem:null,started:'9 h ago',stopped:'2 h ago',reason:'Spot interruption',raw:'Your Spot Task was interrupted.'});
 extra.push({id:'d15a7e8b2c60',svc:null,standalone:'db-migrate',cluster:'ci-tools',status:'mute',statusL:'Stopped',health:'–',rev:2,ip:'10.21.1.58',subnet:'tools-1a',az:'us-east-1a',cap:'FARGATE',cpu:null,mem:null,started:'3 h ago',stopped:'41 min ago',reason:null,raw:'Task stopped (no reason recorded)'});
 var all=base.concat(extra).map(function(t){
  var stopped=/^Stopped/.test(t.statusL)||!!s.stoppedIds[t.id];
  if(s.stoppedIds[t.id]){t=Object.assign({},t,{status:'exp',statusL:'Stopped (exit 0)',health:'–',reason:'Stopped by a user',raw:'Task stopped by user (pavel)',stopped:'just now'})}
  var running=!stopped&&t.statusL!=='Pending'&&t.statusL!=='Provisioning';
  var unhealthy=t.health==='Unhealthy';
  return Object.assign({},t,{stopped_:stopped,running:t.statusL==='Running',live:!stopped,problem:(stopped&&t.status==='bad')||unhealthy,unhealthy:unhealthy,cause:t.cause||CAUSE[t.reason]||null,region:D.cluster(t.cluster).region,execOn:t.id!=='3ab6e2f7c150'&&t.id!=='61e0c3a8f2d9'});
 });
 var q=s.q.toLowerCase();
 var counts={problems:all.filter(function(t){return t.problem}).length,running:all.filter(function(t){return t.live}).length,stopped:all.filter(function(t){return t.stopped_}).length,all:all.length};
 var inSeg=function(t){return s.seg==='all'||(s.seg==='problems'&&t.problem)||(s.seg==='running'&&t.live)||(s.seg==='stopped'&&t.stopped_)};
 var list=all.filter(function(t){return inSeg(t)&&(!q||[t.id,t.svc||'',t.standalone||'',t.cluster,t.ip].join(' ').toLowerCase().indexOf(q)>=0)});
 var run=all.filter(function(t){return t.live}).length,spot=all.filter(function(t){return t.live&&t.cap==='FARGATE_SPOT'}).length;
 var kpi={running:run,unhealthy:all.filter(function(t){return t.unhealthy&&t.live}).length,err:all.filter(function(t){return t.stopped_&&t.status==='bad'}).length,normal:all.filter(function(t){return t.stopped_&&t.status==='mute'}).length,exp:all.filter(function(t){return t.stopped_&&t.status==='exp'}).length,spot:spot,spotPct:Math.round(spot/run*100)};
 var term=function(t){return t.standalone?'TaskTerminal.dc.html':'SvcTerminal.dc.html'};
 var mk=function(t,indent){
  return {kind:'task',isTask:true,isGroup:false,id:t.id,svcName:t.svc||'Standalone',isSvc:!!t.svc,notSvc:!t.svc,sub:t.svc?t.cluster:'Standalone · started by '+t.standalone,cluster:t.cluster,status:t.status,statusL:t.statusL,cause:t.cause,hasCause:!!t.cause,causeTip:'Stop cause: '+t.cause,
   health:t.health,healthCls:t.health==='Healthy'?'st st-ok':t.health==='Unhealthy'?'st st-warn':'mut',rev:':'+t.rev,ip:t.ip,subnet:t.subnet,cap:t.cap,capCls:t.cap==='FARGATE_SPOT'?'pill pill-exp':'mut',cpu:t.cpu==null?'–':t.cpu+'%',mem:t.mem==null?'–':t.mem+'%',started:t.started,reason:t.reason||'–',
   stripe:t.unhealthy&&t.live?'stripe-warn':H.stripe(t.status),cellStyle:indent?'padding-left:30px':'',canStop:t.running,canTerm:t.running&&t.execOn,noTerm:t.running&&!t.execOn,termHref:term(t),
   open:function(){c.setState({sel:t.id})},stop:function(){c.setState({stopId:t.id})},ariaOpen:'Open details for task '+t.id};
 };
 var rows=[];
 var groupable={};
 if(s.group){list.forEach(function(t){if(t.stopped_&&t.reason){var k=t.svc+'|'+t.reason;(groupable[k]=groupable[k]||[]).push(t)}})}
 var done={};
 list.forEach(function(t){
  var k=t.svc+'|'+t.reason,g=groupable[k];
  if(s.group&&t.stopped_&&t.reason&&g&&g.length>1){
   if(done[k])return;done[k]=1;
   var isOpen=!!s.open[k];
   rows.push({kind:'group',isTask:false,isGroup:true,id:'',svcName:t.svc||'Standalone',isSvc:!!t.svc,notSvc:!t.svc,sub:t.cluster,cluster:t.cluster,status:t.status,statusL:t.statusL.replace(/ \(exit \d+\)/,''),cause:t.cause,hasCause:!!t.cause,causeTip:'Stop cause: '+t.cause,health:'–',healthCls:'mut',rev:':'+t.rev,ip:'–',subnet:'',cap:t.cap,capCls:'mut',cpu:'–',mem:'–',started:g[g.length-1].started+' to '+g[0].started,reason:t.reason,
    stripe:H.stripe(t.status),cellStyle:'',canStop:false,canTerm:false,noTerm:false,termHref:'',gn:'×'+g.length+' tasks',gAria:(isOpen?'Collapse ':'Expand ')+g.length+' repeated stops of '+t.svc,gOpen:isOpen,gToggle:function(){var o=Object.assign({},s.open);o[k]=!o[k];c.setState({open:o})}});
   if(isOpen)g.forEach(function(x){rows.push(mk(x,true))});
  }else rows.push(mk(t,false));
 });
 var hide=s.hide,colDefs=[['health','Health'],['rev','Rev'],['ip','IP · Subnet'],['cap','Capacity'],['cpu','CPU'],['mem','Memory'],['started','Started'],['reason','Stopped reason']];
 var col={};colDefs.forEach(function(d){col[d[0]]=!hide[d[0]]});
 var span=3+colDefs.filter(function(d){return col[d[0]]}).length+1;
 var VIEWS={default:{seg:'problems',q:''},prod:{seg:'running',q:'core-services'},spot:{seg:'stopped',q:'spot'}};
 var sel=s.sel&&all.filter(function(t){return t.id===s.sel})[0];
 var drawer=null;
 if(sel){
  var svcRow=sel.svc?D.svc(sel.svc):null;
  var isStopped=sel.stopped_;
  drawer={id:sel.id,arn:'arn:aws:ecs:'+sel.region+':482109375512:task/'+sel.cluster+'/'+sel.id+'2f6a90c1b3d4e5a7',svcName:sel.svc||'Standalone',isSvc:!!sel.svc,sub:sel.svc?sel.cluster:'Started by '+sel.standalone,cluster:sel.cluster,region:sel.region,az:sel.az,rev:':'+sel.rev,family:(sel.svc||sel.standalone)+':'+sel.rev,cap:sel.cap,ip:sel.ip,subnet:sel.subnet,size:sel.cap==='FARGATE_SPOT'?'1 vCPU · 2 GiB':'0.5 vCPU · 1 GiB',status:sel.status,statusL:sel.statusL,cause:sel.cause,hasCause:!!sel.cause,causeTip:'Stop cause: '+sel.cause,health:sel.health,started:sel.started,hasStopped:isStopped,stopped:sel.stopped||'–',
   hasReason:!!sel.reason||isStopped,reason:sel.reason||'Not recorded',raw:sel.raw||'No stop message was recorded.',cpu:sel.cpu==null?'–':sel.cpu+'%',mem:sel.mem==null?'–':sel.mem+'%',
   canStop:sel.running,canTerm:sel.running&&sel.execOn,noTerm:sel.running&&!sel.execOn,termHref:term(sel),
   containers:[{name:sel.svc||sel.standalone,image:'registry.example.test/'+(sel.svc||sel.standalone)+':'+(svcRow?svcRow.image:'1.0.0'),state:isStopped?'Stopped':'Running',cls:isStopped?(sel.status==='bad'?'st st-bad':sel.status==='exp'?'st st-exp':'st st-mute'):'st st-ok',exit:isStopped?(/exit (\d+)/.exec(sel.statusL)||[0,'–'])[1]:'–',agent:sel.execOn&&!isStopped?'Running':'Not running'}].concat(sel.cluster==='core-services'?[{name:'otel-collector',image:'otel/opentelemetry-collector:0.108.0',state:isStopped?'Stopped':'Running',cls:isStopped?'st st-exp':'st st-ok',exit:isStopped?'0':'–',agent:isStopped?'Not running':'Running'}]:[]),
   logs:D.log.slice(0,5).map(function(l){return {t:l[0],lvl:l[1],mk:l[1]==='error'?'ERR':l[1]==='warn'?'WRN':'INF',msg:l[3]}}),
   stop:function(){c.setState({stopId:sel.id})}};
 }
 var stopT=s.stopId&&all.filter(function(t){return t.id===s.stopId})[0];
 var stopD=stopT?{id:stopT.id,sub:stopT.svc?stopT.svc+' will start a replacement to keep '+D.svc(stopT.svc).desired+' running.':'This task belongs to no service; nothing will replace it.'}:null;
 var pv=s.pv;
 return {noop:function(){},kpi:kpi,
  seg:H.seg(c,'seg',[['problems','Problems',counts.problems],['running','Running',counts.running],['stopped','Stopped',counts.stopped],['all','All',counts.all]]),
  q:s.q,onQ:function(e){c.setState({q:e.target.value})},groupOn:s.group,toggleGroup:H.toggle(c,'group'),
  rows:rows,col:col,span:span,showTable:pv==='normal'||pv==='truncated',isLoading:pv==='loading',isError:pv==='error',isTrunc:pv==='truncated',empty:pv!=='loading'&&pv!=='error'&&!rows.length,
  clear:H.set(c,{seg:'all',q:''}),retry:H.set(c,{pv:'normal'}),
  pvOpts:[['normal','Loaded'],['loading','Loading'],['error','Load error'],['truncated','Truncated list']].map(function(o){return {id:o[0],label:o[1]}}),pv:s.pv,onPv:function(e){c.setState({pv:e.target.value})},
  view:s.view,onView:function(e){var v=VIEWS[e.target.value];c.setState({view:e.target.value,seg:v.seg,q:v.q})},
  colsOpen:s.colsOpen,toggleCols:H.toggle(c,'colsOpen'),colOpts:colDefs.map(function(d){return {label:d[1],on:!hide[d[0]],toggle:function(){var h=Object.assign({},hide);h[d[0]]=!h[d[0]];c.setState({hide:h})}}}),
  resetCols:H.set(c,{hide:{}}),
  hiddenNote:s.hiddenNote,hideNote:H.set(c,{hiddenNote:false}),
  drawer:drawer,hasDrawer:!!drawer,closeDrawer:H.set(c,{sel:null}),
  stopD:stopD,hasStop:!!stopD,closeStop:H.set(c,{stopId:null}),
  confirmStop:function(){var m=Object.assign({},s.stoppedIds);m[s.stopId]=true;c.setState({stoppedIds:m,stopId:null});H.toast(c,'Stopping task '+s.stopId+'.')()}
 };
}};
