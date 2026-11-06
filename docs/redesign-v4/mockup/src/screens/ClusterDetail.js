var Screen={state:{sort:'status',dir:'desc',pinned:false,tagEdit:null,tagPick:null,clusterTag:'Prod',svcTags:{}},vals:function(c,s){
 var cl=D.cluster('core-services'),RANK={bad:3,warn:2,info:1,ok:0},mins=function(t){var m=/^(\d+)\s*(min|h|d)/.exec(t);return m?(+m[1])*({min:1,h:60,d:1440}[m[2]]):0};
 var pct=function(v){return v==null?'–':v+'%'};
 var svcs=D.services.filter(function(v){return v.cluster===cl.name}).map(function(v,i){var t=s.svcTags[v.name]||(v.inherited&&!s.svcTags.hasOwnProperty(v.name)?null:v.tag);return Object.assign({},v,{idx:i,own:s.svcTags.hasOwnProperty(v.name)?s.svcTags[v.name]:(v.inherited?null:v.tag)})});
 var effTag=function(v){return v.own||s.clusterTag},inh=function(v){return !v.own};
 var COLS=[['name','Service',function(v){return v.name}],['tag','Label',function(v){return effTag(v)}],['status','Status',function(v){return RANK[v.status]}],['td','Task definition',function(v){return v.name+v.rev}],['image','Image tag',function(v){return v.image}],['tasks','Tasks',function(v){return v.running}],['cpu','CPU',function(v){return v.cpu==null?-1:v.cpu},'num'],['h24','24 h',null],['mem','Memory',function(v){return v.mem==null?-1:v.mem},'num'],['last','Last change',function(v){return mins(v.last)}]];
 var sc=COLS.filter(function(k){return k[0]===s.sort})[0],dir=s.dir==='asc'?1:-1;
 var list=svcs.slice().sort(function(a,b){var p=sc[2](a),r=sc[2](b);return (p<r?-1:p>r?1:a.name<b.name?-1:1)*dir});
 var heads=COLS.map(function(k){var on=s.sort===k[0],srt=!!k[2];return {label:k[1],cls:k[3]||'',sortable:srt,plain:!srt,arrow:on?(s.dir==='asc'?' ↑':' ↓'):'',aria:on?(s.dir==='asc'?'ascending':'descending'):'none',pick:function(){c.setState({sort:k[0],dir:on&&s.dir==='asc'?'desc':'asc'})}}});
 var rows=list.map(function(v){return {name:v.name,tag:effTag(v),tagCls:H.tagClass(effTag(v),inh(v)),tagTip:inh(v)?'Inherited from cluster '+cl.name:'Tag set on this service',status:v.status,statusL:v.statusL,stripe:H.stripe(v.status),taskdef:v.name+':'+v.rev,image:v.image,tasks:v.running+'/'+v.desired,taskTone:v.running<v.desired?'c-bad':'',cpu:pct(v.cpu),mem:pct(v.mem),last:v.last,spark:H.spark(H.series(v.idx+20,24,v.cpu||20,4)),
  editTag:function(){c.setState({tagEdit:{kind:'svc',name:v.name},tagPick:v.own||'inherit'})}}});
 var running=cl.running,desired=cl.desired,miss=Math.max(0,desired-running);
 var maxT=Math.max.apply(null,svcs.map(function(v){return Math.max(v.running,v.desired)}));
 var byService=svcs.slice().sort(function(a,b){return b.desired-a.desired}).slice(0,3).map(function(v){return {name:v.name,running:v.running,desired:v.desired,cls:v.running<v.desired?'bad':'',bar:H.meterStyle(v.running/maxT*100),tick:'position:absolute;top:-3px;bottom:-3px;width:2px;background:var(--text);left:'+(v.desired/maxT*100)+'%'}});
 var normal=H.series(5,24,3,2).map(Math.round),errs=normal.map(function(_,i){return i===18?1:0});
 var te=s.tagEdit,isSvc=te&&te.kind==='svc',curVal=te?(isSvc?(svcs.filter(function(v){return v.name===te.name})[0].own||'inherit'):s.clusterTag):null;
 var opts=(isSvc?[{id:'inherit',name:'Inherit from cluster '+cl.name,cls:H.tagClass(s.clusterTag,true),tagL:s.clusterTag}]:[]).concat(D.tags.map(function(t){return {id:t.name,name:t.name,cls:H.tagClass(t.name,false)}}));
 var close=H.set(c,{tagEdit:null,tagPick:null}),same=!!te&&curVal===s.tagPick;
 var openClusterTag=function(){c.setState({tagEdit:{kind:'cluster',name:cl.name},tagPick:s.clusterTag})};
 var cpu=H.series(11,48,cl.cpu,5),mem=H.series(12,48,cl.mem,3);
 return {name:cl.name,tag:s.clusterTag,tagCls:H.tagClass(s.clusterTag,false),status:cl.status,statusL:cl.statusL,pinned:s.pinned,pinL:s.pinned?'Pinned':'Pin',togglePin:function(){c.setState({pinned:!s.pinned});H.toast(c,s.pinned?'Unpinned '+cl.name+'.':'Pinned '+cl.name+'.')()},
  acct:cl.acct,acctId:cl.acctId,region:cl.region,vpc:cl.vpc,fargate:running-cl.spot*0,spot:0,editClusterTag:openClusterTag,
  nSvc:svcs.length,nSteady:svcs.filter(function(v){return v.status==='ok'}).length,running:running,desired:desired,missL:miss?miss+' missing':'all running',missCls:miss?'c-bad':'c-ok',sTasks:H.spark(H.series(14,36,running,1.5)),
  vcpu:cl.vcpu,gib:cl.gib,cpuNow:cl.insights?cl.cpu+'%':'–',cpuNote:cl.insights?'of reserved CPU':'Insights off',memNow:cl.insights?cl.mem+'%':'–',memNote:cl.insights?'of reserved memory':'Insights off',
  insightsOff:!cl.insights,chartError:false,chartEmpty:false,openInsights:H.toast(c,'Opened Container Insights for '+cl.name+' in the AWS console.'),
  cpuChart:H.chart({series:[{name:'CPU',color:'s1',values:cpu,area:true},{name:'Memory',color:'s2',values:mem}],x:['24 h ago','12 h','now'],marks:[{at:46,label:':10'}],unit:'%',max:100,w:560,h:200,tip:function(i){return (24-Math.round(i/2))+' h ago'}}),
  byService:byService,hasMoreSvc:svcs.length>3,moreSvc:svcs.length-3,
  stops:H.bars({keys:[{name:'Normal',color:'s1'},{name:'Error exit',color:'s2'}],rows:normal.map(function(n,i){return [n,errs[i]]}),x:['24 h ago','12 h','now'],w:520,h:130,tip:function(i){return (24-i)+' h ago'}}),
  heads:heads,rows:rows,
  tagOpen:!!te,tagFor:te?te.name:'',tagSame:same,tagDiff:!same,closeTag:close,
  tagOpts:opts.map(function(o){return {name:o.name,cls:o.cls,note:o.tagL?'':(curVal===o.id?'Current':''),on:s.tagPick===o.id,mark:s.tagPick===o.id?'ok':'mute',pick:H.set(c,{tagPick:o.id})}}).map(function(o,i){if(opts[i].tagL){o.name='Inherit from cluster '+cl.name;o.note=(curVal==='inherit'?'Current, ':'')+'currently '+opts[i].tagL}return o}),
  saveTag:function(){var p={tagEdit:null,tagPick:null};if(isSvc){var m=Object.assign({},s.svcTags);m[te.name]=s.tagPick==='inherit'?null:s.tagPick;p.svcTags=m}else p.clusterTag=s.tagPick;c.setState(p);H.toast(c,'Tag saved.')()}};
}};
