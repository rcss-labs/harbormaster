var STD={};
STD.BASE=JSON.stringify({family:'payments-api',cpu:'1024',memory:'2048',networkMode:'awsvpc',requiresCompatibilities:['FARGATE'],containerDefinitions:[
 {name:'payments-api',image:'123456789012.dkr.ecr.us-east-1.amazonaws.com/payments-api:1.18.2',essential:true,
  environment:[{name:'LOG_LEVEL',value:'info'},{name:'STRIPE_API_BASE',value:'https://api.stripe.com'},{name:'DB_POOL_SIZE',value:'20'}],
  secrets:[{name:'DB_PASSWORD',valueFrom:'arn:aws:secretsmanager:us-east-1:123456789012:secret:payments/db-AbC123'}],
  healthCheck:{command:['CMD-SHELL','curl -f http://localhost:8080/health || exit 1'],interval:30,timeout:5,retries:3,startPeriod:30}},
 {name:'otel-collector',image:'otel/opentelemetry-collector-contrib:0.98.0',essential:false,command:['--config=/etc/otel/config.yaml'],
  environment:[{name:'OTLP_ENDPOINT',value:'tempo.observability.svc:4317'}],secrets:[]}]},null,2);
STD.REVS=[{rev:42,status:'Deployed',st:'ok',tag:'1.18.2',size:'1024 / 2048',when:'5 h ago',by:'dana'},{rev:41,status:'Previous',st:'mute',tag:'1.18.1',size:'1024 / 2048',when:'2 d ago',by:'dana'},
 {rev:40,status:'Previous',st:'mute',tag:'1.18.0',size:'1024 / 2048',when:'9 d ago',by:'omer'},{rev:39,status:'Previous',st:'mute',tag:'1.17.9',size:'512 / 1024',when:'3 w ago',by:'omer'},{rev:38,status:'Previous',st:'mute',tag:'1.17.8',size:'512 / 1024',when:'4 w ago',by:'terraform'}];
STD.diff=function(a,b){var A=a.split('\n'),B=b.split('\n'),n=A.length,m=B.length,L=[],i,j,out=[];
 for(i=0;i<=n;i++)L.push(new Array(m+1).fill(0));
 for(i=n-1;i>=0;i--)for(j=m-1;j>=0;j--)L[i][j]=A[i]===B[j]?L[i+1][j+1]+1:Math.max(L[i+1][j],L[i][j+1]);
 i=0;j=0;while(i<n&&j<m){if(A[i]===B[j]){out.push({t:' ',x:A[i]});i++;j++}else if(L[i+1][j]>=L[i][j+1]){out.push({t:'-',x:A[i]});i++}else{out.push({t:'+',x:B[j]});j++}}
 while(i<n)out.push({t:'-',x:A[i++]});while(j<m)out.push({t:'+',x:B[j++]});return out};
STD.rows=function(ops,ctx){var keep=ops.map(function(o,i){for(var k=Math.max(0,i-ctx);k<=Math.min(ops.length-1,i+ctx);k++)if(ops[k].t!==' ')return true;return false}),rows=[],gap=0;
 var flush=function(){if(gap){rows.push({line:'    … '+gap+' unchanged lines',st:'color:var(--muted)'});gap=0}};
 ops.forEach(function(o,i){if(!keep[i]){gap++;return}flush();rows.push({line:o.t+' '+o.x,st:o.t==='+'?'background:var(--ok-soft)':o.t==='-'?'background:var(--bad-soft)':''})});flush();return rows};

var Screen={state:{text:STD.BASE,baseText:STD.BASE,basedOn:42,revs:STD.REVS,view:'Admin',rev:false,dst:'normal',reason:''},
vals:function(c,s){
 var locked=s.view==='Viewer',next=s.basedOn+1,dirty=s.text!==s.baseText,parsed=null,perr='';
 try{parsed=JSON.parse(s.text);if(!parsed||typeof parsed!=='object'||Array.isArray(parsed))throw new Error('The task definition must be a JSON object.');if(!parsed.family||!parsed.containerDefinitions)throw new Error('The task definition needs "family" and "containerDefinitions".')}catch(e){perr=String(e.message||e)}
 var ops=STD.diff(s.baseText,s.text),lines=[],n=0;
 ops.forEach(function(o){if(o.t!=='-'){n++;lines.push({n:n,st:'height:19.2px;padding:0 8px;text-align:right;'+(o.t==='+'?'background:var(--ok-soft);box-shadow:inset 3px 0 0 var(--ok);color:var(--text)':'')})}});
 var changedN=ops.filter(function(o){return o.t==='+'}).length;
 var tag='–';if(parsed&&parsed.containerDefinitions&&parsed.containerDefinitions[0]&&parsed.containerDefinitions[0].image)tag=String(parsed.containerDefinitions[0].image).split(':').pop();
 var size=parsed?(parsed.cpu+' / '+parsed.memory):'–';
 var revs=s.revs.map(function(r){var isCur=r.status==='Deployed',newer=r.rev>42;return {rev:':'+r.rev,status:r.status,st:'st st-'+r.st,tag:r.tag,size:r.size,when:r.when,by:r.by,cur:isCur,canDeploy:!isCur&&newer&&!locked,canRoll:!isCur&&!newer&&!locked,lockedDeploy:!isCur&&newer&&locked,lockedRoll:!isCur&&!newer&&locked,aria:'revision '+r.rev}});
 var close=function(){c.setState({rev:false,dst:'normal'})};
 return {
  viewSeg:H.seg(c,'view',[['Admin','Admin'],['Viewer','Viewer']]),locked:locked,editable:!locked,
  text:s.text,onText:function(e){c.setState({text:e.target.value})},lines:lines,areaStyle:'height:'+(lines.length*19.2+16)+'px;min-height:240px;white-space:pre;overflow:auto;line-height:19.2px;padding:8px 10px;border:0;border-radius:0;resize:none;background:transparent',gutterStyle:'min-height:240px;padding:8px 0;border-right:1px solid var(--border);color:var(--muted);font:400 12px/19.2px var(--mono);user-select:none;min-width:44px',
  areaCls:perr?'textarea mono err':'textarea mono',invalid:!!perr,perr:perr,basedOn:':'+s.basedOn,next:':'+next,nextFull:'payments-api:'+next,changedN:changedN,
  dirty:dirty,clean:!dirty,discard:function(){c.setState({text:s.baseText})},cantReview:!dirty||!!perr||locked,openRev:function(){c.setState({rev:true,dst:'normal'})},
  revs:revs,revN:revs.length+' shown · newest first',
  revOpen:s.rev,closeRev:close,dstSeg:H.seg(c,'dst',[['normal','Normal'],['moved','Moved while editing'],['error','Registration error']]),dMoved:s.dst==='moved',dErr:s.dst==='error',
  rows:STD.rows(ops,2),reason:s.reason,onReason:function(e){c.setState({reason:e.target.value.slice(0,200)})},
  doReg:function(){var r={rev:next,status:'Not deployed',st:'info',tag:tag,size:size,when:'just now',by:'you'};
   c.setState({rev:false,dst:'normal',baseText:s.text,basedOn:next,revs:[r].concat(s.revs),reason:''});H.toast(c,'payments-api:'+next+' is registered. Deploy it from the Deploy tab when you are ready.')()}
 };
}};
