var Screen={state:{seg:'running',q:'',colsOpen:false,hide:{},sel:null,stopId:null,stoppedIds:{},hiddenNote:true,more:false,pv:'normal'},vals:function(c,s){
 var mkT=function(id,rev,ip,sub,az,started,stopped,reason,raw,status,statusL,cause){return {id:id,rev:rev,ip:ip,subnet:sub,az:az,started:started,stopped:stopped,reason:reason,raw:raw,status:status,statusL:statusL,cause:cause,health:stopped?'–':'Healthy',cpu:stopped?null:40,mem:stopped?null:60}};
 var D0=D.tasks.filter(function(t){return t.svc==='payments-api'});
 var base=[
  mkT('9f2e1a3b77c0',42,'10.20.1.21','private-1a','us-east-1a','5 h ago',null,null,null,'ok','Running',null),
  mkT('b81c7d40a2f6',42,'10.20.2.38','private-1b','us-east-1b','5 h ago',null,null,null,'ok','Running',null),
  mkT('e30a95d1c6b4',42,'10.20.1.67','private-1a','us-east-1a','5 h ago',null,null,null,'ok','Running',null),
  mkT('c72f08be1a43',41,'10.20.1.19','private-1a','us-east-1a','2 d ago','5 h ago','Replaced by a deployment','Scaling activity initiated by (deployment ecs-svc/8841)','exp','Stopped (exit 0)','deploy'),
  mkT('5d90b7e2a1c8',41,'10.20.2.14','private-1b','us-east-1b','2 d ago','5 h ago','Replaced by a deployment','Scaling activity initiated by (deployment ecs-svc/8841)','exp','Stopped (exit 0)','deploy'),
  mkT('a1f3c60d9e72',41,'10.20.1.55','private-1a','us-east-1a','2 d ago','5 h ago','Replaced by a deployment','Scaling activity initiated by (deployment ecs-svc/8841)','exp','Stopped (exit 0)','deploy')
 ];
 base[0].cpu=46;base[0].mem=60;base[1].cpu=42;base[1].mem=63;base[2].cpu=44;base[2].mem=59;
 var older=[
  mkT('7c4e0a91f3b5',40,'10.20.2.77','private-1b','us-east-1b','4 d ago','2 d ago','Replaced by a deployment','Scaling activity initiated by (deployment ecs-svc/8702)','exp','Stopped (exit 0)','deploy'),
  mkT('3e8b52d6c0a9',40,'10.20.1.32','private-1a','us-east-1a','4 d ago','2 d ago','Failed load balancer health checks','Health checks failed with these codes: [502]','bad','Stopped (exit 1)','health'),
  mkT('f26a1d84b7e0',39,'10.20.2.40','private-1b','us-east-1b','6 d ago','4 d ago','Stopped by a user','Task stopped by user (dana)','exp','Stopped (exit 143)','user')
 ];
 var all=base.concat(s.more?older:[]).map(function(t){
  if(s.stoppedIds[t.id])t=Object.assign({},t,{status:'exp',statusL:'Stopped (exit 0)',health:'–',reason:'Stopped by a user',raw:'Task stopped by user (pavel)',stopped:'just now',cause:'user',cpu:null,mem:null});
  var stopped=/^Stopped/.test(t.statusL);
  return Object.assign({},t,{stopped_:stopped,live:!stopped,running:t.statusL==='Running'});
 });
 var q=s.q.toLowerCase();
 var counts={running:all.filter(function(t){return t.live}).length,stopped:all.filter(function(t){return t.stopped_}).length,all:all.length};
 var list=all.filter(function(t){return (s.seg==='all'||(s.seg==='running')===t.live)&&(!q||[t.id,t.ip,t.subnet].join(' ').toLowerCase().indexOf(q)>=0)});
 var rows=list.map(function(t){return {id:t.id,status:t.status,statusL:t.statusL,cause:t.cause,hasCause:!!t.cause,causeTip:'Stop cause: '+t.cause,health:t.health,healthCls:t.health==='Healthy'?'st st-ok':'mut',rev:':'+t.rev,ip:t.ip,subnet:t.subnet,cap:'FARGATE',cpu:t.cpu==null?'–':t.cpu+'%',mem:t.mem==null?'–':t.mem+'%',started:t.started,reason:t.reason||'–',stripe:H.stripe(t.status),canStop:t.running,
  open:function(){c.setState({sel:t.id})},stop:function(){c.setState({stopId:t.id})},ariaOpen:'Open details for task '+t.id}});
 var hide=s.hide,colDefs=[['health','Health'],['rev','Revision'],['ip','Subnet / IP'],['cap','Capacity'],['cpu','CPU'],['mem','Memory'],['started','Started'],['reason','Stopped reason']];
 var col={};colDefs.forEach(function(d){col[d[0]]=!hide[d[0]]});
 var sel=s.sel&&all.filter(function(t){return t.id===s.sel})[0],drawer=null;
 if(sel){var st=sel.stopped_;drawer={id:sel.id,arn:'arn:aws:ecs:us-east-1:482109375512:task/core-services/'+sel.id+'2f6a90c1b3d4e5a7',status:sel.status,statusL:sel.statusL,cause:sel.cause,hasCause:!!sel.cause,causeTip:'Stop cause: '+sel.cause,health:sel.health,rev:':'+sel.rev,ip:sel.ip,subnet:sel.subnet,az:sel.az,started:sel.started,stopped:sel.stopped||'–',hasReason:st,reason:sel.reason||'Not recorded',raw:sel.raw||'No stop message was recorded.',cpu:sel.cpu==null?'–':sel.cpu+'%',mem:sel.mem==null?'–':sel.mem+'%',canStop:sel.running,canTerm:sel.running,
  containers:[{name:'payments-api',image:'registry.example.test/payments-api:1.18.2',state:st?'Stopped':'Running',cls:st?(sel.status==='bad'?'st st-bad':'st st-exp'):'st st-ok',exit:st?(/exit (\d+)/.exec(sel.statusL)||[0,'–'])[1]:'–',agent:st?'Not running':'Running'},{name:'otel-collector',image:'otel/opentelemetry-collector:0.108.0',state:st?'Stopped':'Running',cls:st?'st st-exp':'st st-ok',exit:st?'0':'–',agent:st?'Not running':'Running'}],
  logs:D.log.slice(0,5).map(function(l){return {t:l[0],mk:l[1]==='error'?'ERR':l[1]==='warn'?'WRN':'INF',msg:l[3]}}),stop:function(){c.setState({stopId:sel.id})}}}
 var stopT=s.stopId&&all.filter(function(t){return t.id===s.stopId})[0];
 var pv=s.pv;
 return {seg:H.seg(c,'seg',[['running','Running',counts.running],['stopped','Stopped',counts.stopped],['all','All',counts.all]]),q:s.q,onQ:function(e){c.setState({q:e.target.value})},
  rows:rows,col:col,showTable:pv==='normal',isError:pv==='error',isLoading:pv==='loading',empty:pv==='normal'&&!rows.length,clear:H.set(c,{seg:'all',q:''}),retry:H.set(c,{pv:'normal'}),
  pvOpts:[['normal','Loaded'],['loading','Loading'],['error','Load error']].map(function(o){return {id:o[0],label:o[1]}}),pv:pv,onPv:function(e){c.setState({pv:e.target.value})},
  colsOpen:s.colsOpen,toggleCols:H.toggle(c,'colsOpen'),colOpts:colDefs.map(function(d){return {label:d[1],on:!hide[d[0]],toggle:function(){var h=Object.assign({},hide);h[d[0]]=!h[d[0]];c.setState({hide:h})}}}),resetCols:H.set(c,{hide:{}}),
  hiddenNote:s.hiddenNote,hideNote:H.set(c,{hiddenNote:false}),more:s.more,noMore:!s.more,loadMore:function(){c.setState({more:true});H.toast(c,'Loaded 3 older tasks.')()},
  drawer:drawer,hasDrawer:!!drawer,closeDrawer:H.set(c,{sel:null}),
  hasStop:!!stopT,stopId:stopT?stopT.id:'',stopSub:'payments-api will start a replacement to keep 3 running.',closeStop:H.set(c,{stopId:null}),
  confirmStop:function(){var m=Object.assign({},s.stoppedIds);m[s.stopId]=true;c.setState({stoppedIds:m,stopId:null});H.toast(c,'Stopping task '+s.stopId+'.')()}};
}};
