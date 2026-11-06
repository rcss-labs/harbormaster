var Screen={state:{conn:false,ended:false,task:'9f2e1a3b',container:'payments-api',cmd:'/bin/sh',st:'normal',sid:0},vals:function(c,s){
 var TASKS=[{id:'9f2e1a3b',ip:'10.20.1.21',az:'us-east-1a'},{id:'b81c7d40',ip:'10.20.2.38',az:'us-east-1b'},{id:'e30a95d1',ip:'10.20.1.67',az:'us-east-1a'}];
 var CONTS=[['payments-api','payments-api',true],['otel-collector','otel-collector · agent stopped',false]];
 var cur=TASKS.filter(function(t){return t.id===s.task})[0]||TASKS[0];
 var contOk=CONTS.filter(function(o){return o[0]===s.container})[0][2];
 var sid='pavel-0c71e5a9d3b2f48'+(s.sid%10);
 var P=s.cmd==='/bin/bash'?'bash-5.2# ':'/ # ';
 var session=[
  {k:'sys',t:'Starting session…'},{k:'sys',t:'Starting session with SessionId: '+sid},
  {k:'p',t:P,cmd:'hostname'},{k:'o',t:'ip-'+cur.ip.replace(/\./g,'-')+'.ec2.internal'},
  {k:'p',t:P,cmd:'ps -o pid,rss,etime,comm'},{k:'o',t:'  PID   RSS ELAPSED COMMAND'},{k:'o',t:'    1 61204   05:12:09 /app/payments-api --port 8080'},{k:'o',t:'   74  3180      00:03 sh'},{k:'o',t:'   81  1412      00:00 ps'},
  {k:'p',t:P,cmd:'curl -s localhost:8080/health'},{k:'o',t:'{"status":"ok","version":"1.18.2","db":"up","queue":"up"}'},
  {k:'p',t:P,cmd:'tail -n 3 /var/log/app.log'},{k:'o',t:'11:42:11 WARN slow query took 812ms'},{k:'o',t:'11:42:15 INFO POST /v1/charges 201 143ms'},{k:'o',t:'11:42:18 INFO GET /health 200 2ms'}
 ];
 var lines;
 var sysEnd={idle:'Disconnected after 20 minutes without input.',limit:'Session reached the 4 hour limit.',other:'Close another terminal to open a new one.'}[s.st];
 var showSession=s.conn||s.ended||!!sysEnd;
 if(!showSession)lines=[{k:'sys',t:'Pick a task and a container, then Connect.'}];
 else if(sysEnd&&s.st==='other')lines=[{k:'sys',t:'Starting session…'},{k:'sys',t:sysEnd}];
 else{lines=session.slice();
  if(sysEnd)lines.push({k:'sys',t:sysEnd});
  else if(s.ended)lines.push({k:'sys',t:'Exiting session with sessionId: '+sid+'.'});
  else lines.push({k:'p',t:P,cmd:''})}
 var SYS='opacity:.65;',rowS='min-height:19px;white-space:pre-wrap;overflow-wrap:anywhere;';
 lines=lines.map(function(l){return {pre:l.k==='p'?l.t:'',cmd:l.k==='p'?l.cmd:(l.k==='sys'?'':l.t),isP:l.k==='p',isSys:l.k==='sys',sysT:l.t,sty:rowS+(l.k==='sys'?SYS:'')}});
 var connected=s.conn&&!sysEnd;
 var live=connected;
 var STATES=[['normal','Ready to connect'],['off','ECS Exec is off'],['none','No running tasks'],['lock','Terminal permission missing'],['idle','Idle timeout reached'],['limit','4 hour limit reached'],['other','Another terminal is open']];
 var st=s.st,panelOn=st!=='off'&&st!=='none'&&st!=='lock';
 return {tasks:TASKS.map(function(t){return {id:t.id,label:t.id.slice(0,8)+' · '+t.ip+' · '+t.az}}),task:s.task,onTask:function(e){c.setState({task:e.target.value,conn:false,ended:false})},
  conts:CONTS.map(function(o){return {id:o[0],label:o[1]}}),container:s.container,onContainer:function(e){c.setState({container:e.target.value,conn:false,ended:false})},
  cmds:['/bin/sh','/bin/bash'].map(function(x){return {id:x,label:x}}),cmd:s.cmd,onCmd:function(e){c.setState({cmd:e.target.value})},
  lines:lines,connected:connected,notConnected:!connected,noConnect:!contOk,agentStopped:!contOk,
  connect:function(){c.setState({conn:true,ended:false,st:'normal',sid:s.sid+1})},
  disconnect:function(){c.setState({conn:false,ended:true});H.toast(c,'Terminal session closed.')()},
  statusCls:connected?'st st-ok':'st st-mute',statusL:connected?'Connected':sysEnd?'Disconnected':s.ended?'Ended':'Not connected',
  sessionId:connected||s.ended||sysEnd?sid:'–',curTask:cur.id,curIp:cur.ip,curAz:cur.az,
  states:STATES.map(function(o){return {id:o[0],label:o[1]}}),st:st,onSt:function(e){c.setState({st:e.target.value,conn:false,ended:false})},
  panelOn:panelOn,sOff:st==='off',sNone:st==='none',sLock:st==='lock',sNoExec:st==='noexec',sDown:st==='down',reset:H.set(c,{st:'normal'})};
}};
