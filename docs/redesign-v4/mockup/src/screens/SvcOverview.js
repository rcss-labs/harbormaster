var Screen={state:{ro:'done',insightsOff:false,errFailed:true,dlg:null,desired:3,dDraft:'3',stopId:''},vals:function(c,s){
 var N=function(x){return Math.round(x)};
 var desired=s.desired,prog=s.ro==='prog',fail=s.ro==='fail',done=s.ro==='done';
 var healthy=prog?2:desired,missing=desired-healthy;
 var hs=[[healthy,'var(--ok)','Healthy targets'],[0,'var(--bad)','Unhealthy targets'],[missing,'var(--border-strong)','Missing tasks']];
 var ro={done:{pill:'ok',pillL:'Completed',revs:':41 to :42',when:'Finished 5 h ago',text:'Rolling update, new tasks first. Circuit breaker with rollback is on.',hasBar:false},
  prog:{pill:'info',pillL:'Rollout in progress',revs:':42 to :43',when:'Started 3 min ago',text:'1 new task on :43 · 2 old on :42 · 0 failed to start · circuit breaker with rollback is on',hasBar:true},
  fail:{pill:'bad',pillL:'Failed',revs:':42 to :43',when:'Rolled back 12 min ago',text:'Tasks on :43 failed to start: CannotPullContainerError for payments-api:1.19.0. ECS rolled the service back to :42.',hasBar:false}}[s.ro];
 ro.isFail=fail;ro.notFail=!fail;ro.newStyle='width:33%;background:var(--accent)';ro.oldStyle='width:67%;background:var(--border-strong)';ro.barAria='1 of 3 tasks on :43, 2 of 3 on :42';
 var ins=!s.insightsOff;
 var tasks=D.tasks.filter(function(t){return t.svc==='payments-api'&&t.status==='ok'}).map(function(t){return {id:t.id,ip:t.ip,az:t.az,cpu:t.cpu,mem:t.mem,started:t.started,stop:function(){c.setState({dlg:'stop',stopId:t.id})}}});
 var evs=[[0.2,'deploy',':42'],[2.7,'restart','restart'],[4.3,'deploy',':41'],[6.2,'restart','restart'],[7.9,'fail',':41 failed'],[10.4,'deploy',':40'],[12.6,'deploy',':39']].map(function(e,i){
  return {cls:e[1]==='fail'?'bad':e[1]==='restart'?'mute':'info',label:e[2],style:'position:absolute;left:'+((14-e[0])/14*100).toFixed(1)+'%;top:'+(i%2?58:12)+'px'}});
 var n=s.dDraft===''?NaN:Number(s.dDraft),valid=Number.isInteger(n)&&n>=0&&n<=10;
 var events=[['5 h ago','Harbormaster','dana','Deployed revision :42, image 1.18.2'],['5 h ago','ECS events','ECS','Service reached a steady state.'],['5 h ago','ECS events','ECS','Stopped 3 tasks (deployment ecs-svc/8841).'],['5 h ago','Harbormaster','dana','Set desired count to 3'],['2 d ago','CloudTrail','terraform-ci','Updated the load balancer health check path to /health'],['4 d ago','Harbormaster','omer','Restarted the service']].map(function(e){return {when:e[0],src:e[1],actor:e[2],text:e[3]}});
 var close=H.set(c,{dlg:null});
 return {
  roSeg:H.seg(c,'ro',[['done','Completed'],['prog','In progress'],['fail','Failed']]),insightsOff:s.insightsOff,insightsOn:ins,toggleInsights:H.toggle(c,'insightsOff'),
  showPending:done,running:3,desired:desired,tasksNote:prog?'1 pending':'all running',
  cpuV:ins?'44%':'–',cpuD:ins?'of 1024 units':'Container Insights off',memV:ins?'61%':'–',memD:ins?'of 2048 MiB':'Container Insights off',
  errV:s.errFailed?'–':'14',errD:s.errFailed?'Could not be read':'0.03% of requests',errToday:s.errFailed?'–':'14 today',errFailed:s.errFailed,errOk:!s.errFailed,retryErr:H.set(c,{errFailed:false}),
  cm:H.chart({series:[{name:'CPU',color:'s1',values:H.series(3,48,44,6),area:true},{name:'Memory',color:'s2',values:H.series(8,48,61,3)}],x:['24 h ago','18 h','12 h','6 h','now'],marks:[{at:20,label:'restart'},{at:38,label:':42'}],unit:'%',max:100,w:760,h:200}),
  healthy:healthy,healthSegs:hs.filter(function(h){return h[0]>0}).map(function(h){return {style:'flex:'+h[0]+' 1 0;background:'+h[1]}}),
  healthLegend:hs.map(function(h){return {label:h[2],n:h[0],dot:'width:10px;height:10px;border-radius:2px;background:'+h[1]+';margin-right:5px;display:inline-block;vertical-align:middle'}}),
  healthAria:healthy+' healthy, 0 unhealthy, '+missing+' missing of '+desired,
  ro:ro,restartOn:!prog,restartOff:prog,noop:function(){},openRestart:H.set(c,{dlg:'restart'}),
  openDesired:function(){c.setState({dlg:'desired',dDraft:String(c.state.desired)})},
  rpm:H.chart({series:[{name:'Requests',color:'s1',values:H.series(11,48,1200,160),area:true}],x:['24 h ago','12 h','now'],w:360,h:160}),
  p95:H.chart({series:[{name:'p95',color:'s1',values:H.series(14,48,184,18)}],x:['24 h ago','12 h','now'],unit:' ms',w:360,h:160}),
  err:H.bars({keys:[{name:'5xx',color:'s1'}],rows:H.series(5,24,1,2).map(function(v){return [Math.round(v)]}),x:['24 h ago','12 h','now'],w:360,h:160,tip:function(i){return (24-i)+' h ago'}}),
  tasks:tasks,
  cnt:H.chart({series:[{name:'Running',color:'s1',values:Array.apply(null,Array(72)).map(function(_,i){return i>=40&&i<=43?4:3})},{name:'Desired',color:'s2',values:Array.apply(null,Array(72)).map(function(){return 3}),dash:true}],x:['72 h ago','48 h','24 h','now'],max:5,w:400,h:170}),
  marks:evs,events:events,
  dlgRestart:s.dlg==='restart',dlgDesired:s.dlg==='desired',dlgStop:s.dlg==='stop',stopId:s.stopId,closeDlg:close,
  confirmRestart:function(){c.setState({dlg:null,ro:'prog'});H.toast(c,'Restart started.')()},
  confirmStop:function(){c.setState({dlg:null});H.toast(c,'Task '+c.state.stopId+' is stopping.')()},
  dDraft:s.dDraft,dValid:valid,dInvalid:!valid,dTitle:valid?'Set payments-api to '+n+' '+(n===1?'task':'tasks')+'?':'Set payments-api to a valid number of tasks?',
  dZero:valid&&n===0,dRange:valid&&(n<2||n>6),
  onDraft:function(e){c.setState({dDraft:e.target.value})},
  dDec:function(){c.setState({dDraft:String(Math.max(0,(Number(c.state.dDraft)||0)-1))})},dInc:function(){c.setState({dDraft:String(Math.min(10,(Number(c.state.dDraft)||0)+1))})},
  confirmDesired:function(){var v=Number(c.state.dDraft);if(v===c.state.desired){c.setState({dlg:null});H.toast(c,'Desired count is already '+v+'.')();return}c.setState({dlg:null,desired:v});H.toast(c,'Desired count set to '+v+'.')()}
 };
}};
