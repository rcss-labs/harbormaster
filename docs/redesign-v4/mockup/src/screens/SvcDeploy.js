var SDP={};
SDP.clone=function(o){return JSON.parse(JSON.stringify(o))};
SDP.kv=function(k,v){var o={};o[k]=v;return o};
SDP.ECR='123456789012.dkr.ecr.us-east-1.amazonaws.com/payments-api';
SDP.OTEL='otel/opentelemetry-collector-contrib';
SDP.INIT={tagP:'1.18.2',tagO:'0.98.0',
 envP:[{k:'LOG_LEVEL',v:'info'},{k:'STRIPE_API_BASE',v:'https://api.stripe.com'},{k:'DB_POOL_SIZE',v:'20'}],
 envO:[{k:'OTLP_ENDPOINT',v:'tempo.observability.svc:4317'}],
 secP:[{n:'DB_PASSWORD',a:'arn:aws:secretsmanager:us-east-1:123456789012:secret:payments/db-AbC123'},{n:'STRIPE_KEY',a:'arn:aws:secretsmanager:us-east-1:123456789012:secret:payments/stripe-Xy9QwE'}],
 secO:[],
 cmdP:[],epP:[],cmdO:['--config=/etc/otel/config.yaml'],epO:[],
 hcP:{type:'CMD-SHELL',cmd:'curl -f http://localhost:8080/health || exit 1',interval:'30',timeout:'5',retries:'3',start:'30'},hcO:null,
 cpu:'1024',mem:'2048',desired:'3',exec:false,cb:true,rb:true,minH:'100',maxP:'200',grace:'60',force:false,
 asOn:true,asMin:'3',asMax:'12',
 pol:[{name:'cpu-target',metric:'CPU',target:'60',tg:'',sin:'300',sout:'60',dis:false},
  {name:'alb-requests',metric:'ALB',target:'800',tg:'payments-api-tg',sin:'300',sout:'60',dis:false},
  {name:'burst-step-out',metric:'Step',target:'',tg:'',sin:'300',sout:'60',dis:true}],
 sched:[{name:'weekday-morning',kind:'Cron',cron:'0 7 ? * MON-FRI *',at:'',every:'',unit:'minutes',tz:'America/New_York',min:'6',max:'12'},
  {name:'night-scale-down',kind:'Cron',cron:'0 22 * * ? *',at:'',every:'',unit:'minutes',tz:'America/New_York',min:'3',max:''}]};
SDP.TAGS=[['1.19.0','38 min ago'],['1.18.4','3 h ago'],['1.18.3','1 d ago'],['1.18.2','5 d ago'],['1.18.1','9 d ago'],['1.18.0','2 w ago'],['1.17.9','3 w ago'],['1.17.8','4 w ago']];
SDP.CPUS=['256','512','1024','2048','4096'];
SDP.MEMS=['512','1024','2048','3072','4096','5120','6144','7168','8192','16384'];
SDP.TZ=['UTC','America/New_York','Europe/Dublin','Other'];
SDP.TAGRE=/^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$/;
SDP.M={tag:'Tag may contain letters, digits, . _ - and must start with a letter, digit or _ (128 characters at most).',arg:'Arguments cannot be empty.',
 as:'Minimum and maximum must be whole numbers with minimum at most maximum.'};
SDP.okSize=function(cpu,mem){cpu=+cpu;mem=+mem;if(cpu===256)return mem===512||mem===1024||mem===2048;
 var r={512:[1024,4096],1024:[2048,8192],2048:[4096,16384],4096:[8192,30720]}[cpu];return !!r&&mem>=r[0]&&mem<=r[1]&&mem%1024===0};
SDP.isInt=function(v,lo,hi){return /^\d+$/.test(String(v))&&+v>=lo&&+v<=hi};
SDP.METRIC={CPU:'CPU %',Memory:'Memory %',ALB:'ALB requests per target',Step:'Step scaling'};
SDP.polSum=function(p){return p.metric==='Step'?'Step scaling, cooldown in '+p.sin+' s':SDP.METRIC[p.metric]+' at '+p.target+(p.metric==='ALB'?'':'%')+', in '+p.sin+' s · out '+p.sout+' s'+(p.dis?', scale-in off':'')};
SDP.schedStr=function(a){return a.kind==='Cron'?'cron('+a.cron+')':a.kind==='At'?'at('+a.at+')':'rate('+a.every+' '+a.unit+')'};
SDP.actSum=function(a){return SDP.schedStr(a)+' '+a.tz+', min '+(a.min||'unchanged')+', max '+(a.max||'unchanged')};
SDP.hcSum=function(h){return h?h.type+' '+h.cmd+' every '+h.interval+' s, timeout '+h.timeout+' s, '+h.retries+' retries, start '+h.start+' s':'None'};
SDP.argSum=function(a){return a.length?a.join(' '):'Image default'};
SDP.onoff=function(v){return v?'On':'Off'};
SDP.byKey=function(cur,base,nk,name){var b=null,i;for(i=0;i<base.length;i++)if(base[i][nk]===name)b=base[i];
 var out=cur.filter(function(x){return x[nk]!==name});if(b)out.splice(Math.min(base.indexOf(b),out.length),0,SDP.clone(b));return out};
/* Compare cfg against base and return the list of pending changes with before and after text and a revert patch. */
SDP.changes=function(cfg,base){
 var out=[],same=function(a,b){return JSON.stringify(a)===JSON.stringify(b)};
 var sc=[['tagP','Image tag','Container payments-api',null],['tagO','Image tag','Container otel-collector',null],
  ['cpu','Task CPU','Task size',function(v){return v+' units'}],['mem','Task memory','Task size',function(v){return v+' MiB'}],
  ['desired','Desired count','Service',null],['exec','ECS Exec','Service',SDP.onoff],['cb','Circuit breaker','Service',SDP.onoff],['rb','Roll back on failure','Service',SDP.onoff],
  ['minH','Minimum healthy percent','Service',function(v){return v+'%'}],['maxP','Maximum percent','Service',function(v){return v+'%'}],
  ['grace','Health check grace period','Service',function(v){return v+' s'}],['force','Force new deployment','Service',SDP.onoff],
  ['asMin','Minimum tasks','Auto scaling',null],['asMax','Maximum tasks','Auto scaling',null]];
 sc.forEach(function(d){if(String(cfg[d[0]])!==String(base[d[0]])){var f=d[3]||function(v){return String(v)};out.push({id:d[0],grp:d[2],label:d[1],before:f(base[d[0]]),after:f(cfg[d[0]]),rv:SDP.kv(d[0],base[d[0]])})}});
 if(cfg.asOn!==base.asOn)out.push({id:'asOn',grp:'Auto scaling',label:'Auto scaling target',before:base.asOn?'Registered':'None',after:cfg.asOn?'Registered':'Removed',rv:{asOn:base.asOn}});
 var lists=function(key,nk,grp,labelf,sumf,idp){var names={};cfg[key].concat(base[key]).forEach(function(x){names[x[nk]]=1});
  Object.keys(names).forEach(function(n){if(!n)return;var a=base[key].filter(function(x){return x[nk]===n})[0],b=cfg[key].filter(function(x){return x[nk]===n})[0];
   if(!same(a,b)){var p={};p[key]=SDP.byKey(cfg[key],base[key],nk,n);out.push({id:idp+n,grp:grp,label:labelf(n),before:a?sumf(a):'Not set',after:b?sumf(b):'Removed',rv:p})}})};
 ['P','O'].forEach(function(X){var g='Container '+(X==='P'?'payments-api':'otel-collector');
  lists('env'+X,'k',g,function(n){return 'Environment '+n},function(x){return x.v},'env'+X);
  lists('sec'+X,'n',g,function(n){return 'Secret '+n},function(x){return x.a.split(':secret:')[1]||x.a},'sec'+X);
  ['cmd','ep'].forEach(function(k){if(!same(cfg[k+X],base[k+X]))out.push({id:k+X,grp:g,label:k==='cmd'?'Command':'Entry point',before:SDP.argSum(base[k+X]),after:SDP.argSum(cfg[k+X]),rv:SDP.kv(k+X,base[k+X])})});
  if(!same(cfg['hc'+X],base['hc'+X]))out.push({id:'hc'+X,grp:g,label:'Health check',before:SDP.hcSum(base['hc'+X]),after:SDP.hcSum(cfg['hc'+X]),rv:SDP.kv('hc'+X,base['hc'+X])})});
 lists('pol','name','Auto scaling',function(n){return 'Policy '+n},SDP.polSum,'pol');
 lists('sched','name','Auto scaling',function(n){return 'Scheduled action '+n},SDP.actSum,'sched');
 return out};
SDP.problems=function(cfg){
 var P=[],E={};var add=function(k,m,w){P.push({m:(w?w+': ':'')+m,raw:m});if(!E[k])E[k]=m};
 [['P','payments-api'],['O','otel-collector']].forEach(function(x){var X=x[0];
  if(!SDP.TAGRE.test(cfg['tag'+X]))add('tag'+X,SDP.M.tag,x[1]);
  if(cfg['env'+X].some(function(e){return !e.k.trim()}))add('env'+X,'Environment variable names cannot be empty.',x[1]);
  if(cfg['sec'+X].some(function(e){return !e.n.trim()||!e.a.trim()}))add('sec'+X,'Secrets need a name and a value from.',x[1]);
  ['cmd','ep'].forEach(function(k){if(cfg[k+X].some(function(a){return !a.trim()}))add(k+X,SDP.M.arg,x[1])});
  var h=cfg['hc'+X];if(h){if(!h.cmd.trim())add('hc'+X,'Health check command cannot be empty.',x[1]);
   if(!SDP.isInt(h.interval,5,300)||!SDP.isInt(h.timeout,2,60)||!SDP.isInt(h.retries,1,10)||!SDP.isInt(h.start,0,300))add('hc'+X,'Interval 5 to 300 s, timeout 2 to 60 s, retries 1 to 10, start period 0 to 300 s.',x[1])}});
 if(!SDP.okSize(cfg.cpu,cfg.mem))add('size',cfg.mem+' MiB is not available with '+cfg.cpu+' CPU units. Pick one of the listed sizes.');
 var top=cfg.asOn?+cfg.asMax:100;
 if(!SDP.isInt(cfg.desired,0,isNaN(top)?100:top))add('desired','Desired count must be a whole number between 0 and '+(isNaN(top)?100:top)+'.');
 if(!SDP.isInt(cfg.minH,0,100))add('minH','Minimum healthy percent must be between 0 and 100.');
 if(!SDP.isInt(cfg.maxP,100,200))add('maxP','Maximum percent must be between 100 and 200.');
 if(!SDP.isInt(cfg.grace,0,2147483647))add('grace','Health check grace period must be a whole number of seconds.');
 if(cfg.asOn&&!(SDP.isInt(cfg.asMin,0,10000)&&SDP.isInt(cfg.asMax,0,10000)&&+cfg.asMin<=+cfg.asMax))add('as',SDP.M.as);
 return {list:P,err:E}};
/* Task definition JSON from the editable config. */
SDP.td=function(cfg){
 var mk=function(X,name,image){var h=cfg['hc'+X],o={name:name,image:image+':'+cfg['tag'+X],essential:true};
  if(cfg['ep'+X].length)o.entryPoint=cfg['ep'+X];if(cfg['cmd'+X].length)o.command=cfg['cmd'+X];
  o.environment=cfg['env'+X].map(function(e){return {name:e.k,value:e.v}});o.secrets=cfg['sec'+X].map(function(e){return {name:e.n,valueFrom:e.a}});
  if(h)o.healthCheck={command:[h.type,h.cmd],interval:+h.interval,timeout:+h.timeout,retries:+h.retries,startPeriod:+h.start};return o};
 return JSON.stringify({family:'payments-api',cpu:cfg.cpu,memory:cfg.mem,networkMode:'awsvpc',requiresCompatibilities:['FARGATE'],
  containerDefinitions:[mk('P','payments-api',SDP.ECR),mk('O','otel-collector',SDP.OTEL)]},null,2)};
SDP.diff=function(a,b){var A=a.split('\n'),B=b.split('\n'),n=A.length,m=B.length,L=[],i,j,out=[];
 for(i=0;i<=n;i++)L.push(new Array(m+1).fill(0));
 for(i=n-1;i>=0;i--)for(j=m-1;j>=0;j--)L[i][j]=A[i]===B[j]?L[i+1][j+1]+1:Math.max(L[i+1][j],L[i][j+1]);
 i=0;j=0;while(i<n&&j<m){if(A[i]===B[j]){out.push({t:' ',x:A[i]});i++;j++}else if(L[i+1][j]>=L[i][j+1]){out.push({t:'-',x:A[i]});i++}else{out.push({t:'+',x:B[j]});j++}}
 while(i<n)out.push({t:'-',x:A[i++]});while(j<m)out.push({t:'+',x:B[j++]});return out};
SDP.rows=function(ops,ctx){var keep=ops.map(function(o,i){for(var k=Math.max(0,i-ctx);k<=Math.min(ops.length-1,i+ctx);k++)if(ops[k].t!==' ')return true;return false}),rows=[],gap=0;
 var flush=function(){if(gap){rows.push({line:'    … '+gap+' unchanged lines',st:'color:var(--muted)'});gap=0}};
 ops.forEach(function(o,i){if(!keep[i]){gap++;return}flush();rows.push({line:o.t+' '+o.x,st:o.t==='+'?'background:var(--ok-soft)':o.t==='-'?'background:var(--bad-soft)':''})});flush();return rows};

var Screen={state:{cfg:SDP.clone(SDP.INIT),base:SDP.clone(SDP.INIT),view:'Admin',cont:'payments-api',ro:'none',tagOpen:false,tagF:false,pe:null,ae:null,rev:false,dst:'normal',reason:'',rbOpen:false,chip:false},
vals:function(c,s){
 var cfg=s.cfg,base=s.base,X=s.cont==='otel-collector'?'O':'P',locked=s.view==='Viewer',kv=SDP.kv,clone=SDP.clone;
 var upd=function(p){c.setState({cfg:Object.assign({},s.cfg,p)})};
 var chg=SDP.changes(cfg,base),prob=SDP.problems(cfg),E=prob.err;
 var ecr=X==='P',repo=ecr?SDP.ECR:SDP.OTEL;
 var inp=function(key){return function(e){upd(kv(key,e.target.value))}};
 var tgl=function(key){return function(){upd(kv(key,!s.cfg[key]))}};
 var step=function(key,d,lo,hi){return function(){var n=parseInt(s.cfg[key],10);n=isNaN(n)?0:n;upd(kv(key,String(Math.max(lo,Math.min(hi,n+d)))))}};
 var arrRows=function(key,idp){return cfg[key+X].map(function(a,i){return {idk:idp+X+i,val:a,aria:'Remove argument '+(i+1),
  on:function(e){var l=clone(s.cfg[key+X]);l[i]=e.target.value;upd(kv(key+X,l))},rm:function(){var l=clone(s.cfg[key+X]);l.splice(i,1);upd(kv(key+X,l))}}})};
 var addArg=function(key){return function(){upd(kv(key+X,s.cfg[key+X].concat(['']))) }};
 var env=cfg['env'+X].map(function(r,i){return {ik:'sd-env-k-'+X+i,iv:'sd-env-v-'+X+i,k:r.k,v:r.v,aria:'Remove variable '+r.k,
  onK:function(e){var l=clone(s.cfg['env'+X]);l[i].k=e.target.value;upd(kv('env'+X,l))},onV:function(e){var l=clone(s.cfg['env'+X]);l[i].v=e.target.value;upd(kv('env'+X,l))},
  rm:function(){var l=clone(s.cfg['env'+X]);l.splice(i,1);upd(kv('env'+X,l))}}});
 var sec=cfg['sec'+X].map(function(r,i){return {ik:'sd-sec-n-'+X+i,iv:'sd-sec-a-'+X+i,n:r.n,a:r.a,aria:'Remove secret '+r.n,
  onN:function(e){var l=clone(s.cfg['sec'+X]);l[i].n=e.target.value;upd(kv('sec'+X,l))},onA:function(e){var l=clone(s.cfg['sec'+X]);l[i].a=e.target.value;upd(kv('sec'+X,l))},
  rm:function(){var l=clone(s.cfg['sec'+X]);l.splice(i,1);upd(kv('sec'+X,l))}}});
 var hc=cfg['hc'+X];
 var setHc=function(k){return function(e){var h=clone(s.cfg['hc'+X]);h[k]=e.target.value;upd(kv('hc'+X,h))}};
 var hcf=hc?[['Interval (s)','interval','5 to 300'],['Timeout (s)','timeout','2 to 60'],['Retries','retries','1 to 10'],['Start period (s)','start','0 to 300']].map(function(f){return {id:'sd-hc-'+f[1],label:f[0],hint:f[2],v:hc[f[1]],on:setHc(f[1]),bad:!SDP.isInt(hc[f[1]],+f[2].split(' to ')[0],+f[2].split(' to ')[1])?'input err':'input'}}):[];
 /* image tag picker */
 var tag=cfg['tag'+X],q=s.tagF?tag.toLowerCase():'',deployed=base['tag'+X];
 var matches=SDP.TAGS.filter(function(t){return !q||t[0].toLowerCase().indexOf(q)>=0});
 var tagOpts=matches.slice(0,6).map(function(t){return {t:t[0],age:'pushed '+t[1],newest:t[0]===SDP.TAGS[0][0],deployed:t[0]===deployed,sel:t[0]===tag,pick:function(){c.setState({cfg:Object.assign({},s.cfg,kv('tag'+X,t[0])),tagOpen:false,tagF:false})}}});
 var tagFoot=q?matches.length+' of '+SDP.TAGS.length+' tags match · newest '+tagOpts.length+' shown':tagOpts.length+' of '+SDP.TAGS.length+' tags';
 /* task size */
 var cpuOpts=SDP.CPUS.map(function(v){return {v:v}}),memOpts=SDP.MEMS.map(function(v){return {v:v}});
 var oom=s.ro==='failed'&&SDP.okSize(cfg.cpu,cfg.mem)&&+cfg.mem<=2048;
 var oomNext=SDP.MEMS.filter(function(m){return +m>+cfg.mem&&SDP.okSize(cfg.cpu,m)})[1]||SDP.MEMS.filter(function(m){return +m>+cfg.mem&&SDP.okSize(cfg.cpu,m)})[0]||'4096';
 var dn=parseInt(cfg.desired,10),outside=cfg.asOn&&!E.desired&&(dn<+cfg.asMin||dn>+cfg.asMax)&&!E.as,clampTo=Math.max(+cfg.asMin,Math.min(+cfg.asMax,dn));
 /* policies and actions */
 var pol=cfg.pol.map(function(p,i){return {name:p.name,metric:SDP.METRIC[p.metric],target:p.metric==='Step'?'Step adjustments':p.target+(p.metric==='ALB'?' req':'%'),cool:'in '+p.sin+' s · out '+p.sout+' s',sin:p.dis?'Disabled':'On',
  edit:p.metric!=='Step',aria:p.name,ed:function(){c.setState({pe:Object.assign({idx:i},clone(s.cfg.pol[i]))})},rm:function(){upd({pol:s.cfg.pol.filter(function(x,j){return j!==i})})}}});
 var sch=cfg.sched.map(function(a,i){return {name:a.name,sched:SDP.schedStr(a),tz:a.tz,min:a.min||'unchanged',max:a.max||'unchanged',
  ed:function(){c.setState({ae:Object.assign({idx:i},clone(s.cfg.sched[i]))})},rm:function(){upd({sched:s.cfg.sched.filter(function(x,j){return j!==i})})}}});
 var pe=s.pe,ae=s.ae;
 var setPe=function(k){return function(e){c.setState({pe:Object.assign({},s.pe,kv(k,e.target.value))})}};
 var setAe=function(k){return function(e){c.setState({ae:Object.assign({},s.ae,kv(k,e.target.value))})}};
 var peErr='';if(pe){if(cfg.pol.some(function(p,i){return p.name===pe.name&&i!==pe.idx}))peErr='A policy named '+pe.name+' already exists.';
  else if(pe.metric!=='ALB'&&!(+pe.target>0&&+pe.target<=100))peErr='CPU and memory targets are percentages above 0 up to 100.';
  else if(pe.metric==='ALB'&&!pe.tg)peErr='Choose the target group that receives the requests.'}
 var aeErr='';if(ae){if(cfg.sched.some(function(p,i){return p.name===ae.name&&i!==ae.idx}))aeErr='A scheduled action named '+ae.name+' already exists.';}
 var aeNone=!!ae&&!ae.min&&!ae.max;
 /* review */
 var tdOld=SDP.td(base),tdNew=SDP.td(cfg),tdChanged=tdOld!==tdNew,tdRows=SDP.rows(SDP.diff(tdOld,tdNew),2);
 var svcRows=chg.filter(function(x){return x.grp==='Service'});
 if(chg.some(function(x){return x.id==='exec'||x.id==='force'}))svcRows=svcRows.concat([{label:'Restart tasks',before:'No',after:'Yes'}]);
 var asRows=chg.filter(function(x){return x.grp==='Auto scaling'});
 var parts=[];if(tdChanged)parts.push('registers revision :43 and deploys it to payments-api');if(svcRows.length)parts.push('changes the service settings');if(asRows.length)parts.push('changes auto scaling');
 var sum=parts.length?(parts.length>1?parts.slice(0,-1).join(', ')+' and '+parts[parts.length-1]:parts[0]):'';sum=sum.charAt(0).toUpperCase()+sum.slice(1)+'.';
 var primary=tdChanged?'Register and deploy :43':'Apply changes';
 var closeRev=function(){c.setState({rev:false,dst:'normal'})};
 var doDeploy=function(){var m=tdChanged?'Deploying payments-api to :43.':'Applied the changes to payments-api.';
  c.setState({rev:false,dst:'normal',base:clone(s.cfg),reason:'',ro:tdChanged?'running':s.ro,pe:null,ae:null});H.toast(c,m)()};
 var roName=({running:'Rollout in progress',failed:'Failed',done:'Completed'})[s.ro]||'';
 var roPill=({running:'pill pill-info',failed:'pill pill-bad',done:'pill pill-ok'})[s.ro]||'pill';
 var items=chg.map(function(x){return {grp:x.grp,label:x.label,before:x.before,after:x.after,aria:'Revert '+x.label,rv:function(){upd(x.rv)}}});
 var roBar=s.ro==='done'?100:s.ro==='failed'?33:33;
 var hint=function(id,t){return t};
 return {
  v:{exec:cfg.exec,cb:cfg.cb,rb:cfg.rb,force:cfg.force,desired:cfg.desired,minH:cfg.minH,maxP:cfg.maxP,grace:cfg.grace,asMin:cfg.asMin,asMax:cfg.asMax,cpu:cfg.cpu,mem:cfg.mem,rbOff:!cfg.cb||locked},
  fn:{exec:tgl('exec'),cb:function(){var p={cb:!s.cfg.cb};if(s.cfg.cb)p.rb=false;upd(p)},rb:tgl('rb'),force:tgl('force'),desired:inp('desired'),minH:inp('minH'),maxP:inp('maxP'),grace:inp('grace'),asMin:inp('asMin'),asMax:inp('asMax'),
   cpu:inp('cpu'),mem:inp('mem'),dDec:step('desired',-1,0,10000),dInc:step('desired',1,0,10000),
   asRm:function(){upd({asOn:false,pol:[],sched:[]})},asKeep:function(){upd({asOn:true,pol:clone(s.base.pol),sched:clone(s.base.sched)})},asAdd:function(){upd({asOn:true})},
   addEnv:function(){upd(kv('env'+X,s.cfg['env'+X].concat([{k:'',v:''}])))},addSec:function(){upd(kv('sec'+X,s.cfg['sec'+X].concat([{n:'',a:''}])))},
   addCmd:addArg('cmd'),addEp:addArg('ep'),
   addHc:function(){upd(kv('hc'+X,{type:'CMD-SHELL',cmd:'',interval:'30',timeout:'5',retries:'3',start:'0'}))},rmHc:function(){upd(kv('hc'+X,null))},
   hcShell:function(){var h=clone(s.cfg['hc'+X]);h.type='CMD-SHELL';upd(kv('hc'+X,h))},hcCmd:function(){var h=clone(s.cfg['hc'+X]);h.type='CMD';upd(kv('hc'+X,h))},hcCmdIn:setHc('cmd'),
   tag:function(e){c.setState({cfg:Object.assign({},s.cfg,kv('tag'+X,e.target.value)),tagOpen:true,tagF:true})},tagToggle:function(){c.setState({tagOpen:!s.tagOpen,tagF:false})},
   newPol:function(){c.setState({pe:{idx:-1,name:'',metric:'CPU',target:'60',tg:'',sin:'300',sout:'60',dis:false}})},
   savePol:function(){var p={name:s.pe.name,metric:s.pe.metric,target:s.pe.target,tg:s.pe.tg,sin:s.pe.sin,sout:s.pe.sout,dis:s.pe.dis},l=clone(s.cfg.pol);if(s.pe.idx<0)l.push(p);else l[s.pe.idx]=p;c.setState({cfg:Object.assign({},s.cfg,{pol:l}),pe:null})},
   cancelPol:function(){c.setState({pe:null})},
   newAct:function(){c.setState({ae:{idx:-1,name:'',tz:'UTC',kind:'Cron',cron:'',at:'',every:'1',unit:'hours',min:'',max:''}})},
   saveAct:function(){var a=Object.assign({},s.ae);delete a.idx;delete a.tzc;if(s.ae.tz==='Other')a.tz=s.ae.tzc||'Region/City';var l=clone(s.cfg.sched);if(s.ae.idx<0)l.push(a);else l[s.ae.idx]=a;c.setState({cfg:Object.assign({},s.cfg,{sched:l}),ae:null})},
   cancelAct:function(){c.setState({ae:null})}},
  locked:locked,editable:!locked,lockTip:locked?'You need Edit on Prod':'',
  viewSeg:H.seg(c,'view',[['Admin','Admin'],['Viewer','Viewer']]),roSeg:H.seg(c,'ro',[['none','None'],['running','In progress'],['failed','Failed'],['done','Completed']]),
  contSeg:H.seg(c,'cont',[['payments-api','payments-api'],['otel-collector','otel-collector']]),
  cont:s.cont,repo:repo,ecr:ecr,notEcr:!ecr,tag:tag,tagOpen:s.tagOpen&&ecr,tagOpts:tagOpts,tagNone:!tagOpts.length,tagFoot:tagFoot,tagErr:E['tag'+X]||'',tagBad:E['tag'+X]?'input mono err':'input mono',
  env:env,envEmpty:!env.length,envErr:E['env'+X]||'',sec:sec,secEmpty:!sec.length,secErr:E['sec'+X]||'',
  cmd:arrRows('cmd','sd-cmd-'),ep:arrRows('ep','sd-ep-'),cmdEmpty:!cfg['cmd'+X].length,epEmpty:!cfg['ep'+X].length,cmdErr:E['cmd'+X]||'',epErr:E['ep'+X]||'',
  hcOn:!!hc,hcOff:!hc,hc:hc||{type:'',cmd:''},hcShellP:!!hc&&hc.type==='CMD-SHELL',hcCmdP:!!hc&&hc.type==='CMD',hcf:hcf,hcErr:E['hc'+X]||'',
  cpuOpts:cpuOpts,memOpts:memOpts,sizeErr:E.size||'',oom:oom,oomText:'Tasks of this service stop with OutOfMemoryError (exit 137) at '+cfg.mem+' MiB. Try '+oomNext+' MiB.',
  desiredErr:E.desired||'',outside:outside,outsideText:'Auto scaling keeps this service between '+cfg.asMin+' and '+cfg.asMax+'. It will move '+cfg.desired+' back into that range ('+clampTo+').',
  minHErr:E.minH||'',maxPErr:E.maxP||'',graceErr:E.grace||'',noGrace:false,
  execWarn:cfg.exec,
  asOn:cfg.asOn,asOff:!cfg.asOn,keepable:!cfg.asOn&&base.asOn,asErr:E.as||'',asMove:cfg.asOn&&!E.as&&!E.desired&&(dn<+cfg.asMin||dn>+cfg.asMax),asMoveText:'Auto scaling would move '+cfg.desired+' back into '+cfg.asMin+'–'+cfg.asMax+'.',
  pol:pol,sch:sch,
  peOpen:!!pe,pe:pe||{},peAlb:!!pe&&pe.metric==='ALB',peTitle:pe&&pe.idx>=0?'Edit policy':'Add policy',peErr:peErr,peNo:!(!!pe&&!peErr&&!!pe.name.trim()),peNm:setPe('name'),peMetric:setPe('metric'),peTarget:setPe('target'),peTg:setPe('tg'),peSin:setPe('sin'),peSout:setPe('sout'),peDis:function(){c.setState({pe:Object.assign({},s.pe,{dis:!s.pe.dis})})},
  aeOpen:!!ae,ae:ae||{},aeTitle:ae&&ae.idx>=0?'Edit scheduled action':'Add scheduled action',aeErr:aeErr,aeNone:aeNone,aeNo:!(!!ae&&!aeErr&&!aeNone&&!!ae.name.trim()),aeOther:!!ae&&ae.tz==='Other',
  aeCron:!!ae&&ae.kind==='Cron',aeAt:!!ae&&ae.kind==='At',aeRate:!!ae&&ae.kind==='Rate',
  aeKind:['Cron','At','Rate'].map(function(k){return {label:k,pressed:!!s.ae&&s.ae.kind===k,pick:function(){c.setState({ae:Object.assign({},s.ae,{kind:k})})}}}),
  aeNm:setAe('name'),aeTz:setAe('tz'),aeTzc:setAe('tzc'),aeCronIn:setAe('cron'),aeAtIn:setAe('at'),aeEvery:setAe('every'),aeUnit:setAe('unit'),aeMin:setAe('min'),aeMax:setAe('max'),
  tz:SDP.TZ.map(function(z){return {v:z}}),
  /* pending changes */
  items:items,n:chg.length,nLabel:chg.length+(chg.length===1?' change':' changes'),none:!chg.length,hasItems:chg.length>0,
  problems:prob.list,hasProb:prob.list.length>0,
  chipOn:s.chip,chipDismiss:function(){c.setState({chip:false})},loadRev:function(){c.setState({chip:true})},
  discard:function(){c.setState({cfg:clone(s.base),pe:null,ae:null,tagOpen:false})},
  canReview:chg.length>0&&!prob.list.length&&!locked,cantReview:!(chg.length>0&&!prob.list.length&&!locked),
  openRev:function(){c.setState({rev:true,dst:'normal'})},
  /* rollout */
  roOn:s.ro!=='none',roName:roName,roPill:roPill,roRunning:s.ro==='running',roFailed:s.ro==='failed',roDone:s.ro==='done',roBar:'width:'+roBar+'%',roCount:s.ro==='done'?'3 new tasks on :43 · 0 old on :42':'1 new task on :43 · 2 old on :42',roAge:s.ro==='done'?'finished 12 s ago':'started 2 min ago',
  roFailTxt:'2 failed to start',roReason:'Task failed container health checks: essential container payments-api exited with code 1 (config key STRIPE_API_BASE missing).',
  openRb:function(){c.setState({rbOpen:true})},closeRb:function(){c.setState({rbOpen:false})},rbOpen:s.rbOpen,
  doRb:function(){c.setState({rbOpen:false,ro:'none'});H.toast(c,'Rolling back payments-api to :42.')()},
  /* review dialog */
  revOpen:s.rev,closeRev:closeRev,doDeploy:doDeploy,primary:primary,sum:sum,tdChanged:tdChanged,tdRows:tdRows,svcRows:svcRows,hasSvc:svcRows.length>0,asRows:asRows,hasAs:asRows.length>0,
  dstSeg:H.seg(c,'dst',[['normal','Normal'],['moved','Moved while editing'],['partial','Partial failure']]),
  dMoved:s.dst==='moved',dPartial:s.dst==='partial',dNotPartial:s.dst!=='partial',
  reason:s.reason,reasonN:s.reason.length+' / 200',onReason:function(e){c.setState({reason:e.target.value.slice(0,200)})},
  retry:function(){c.setState({dst:'normal'});H.toast(c,'Retrying the remaining steps.')()}
 };
}};
