var Screen={state:{view:'all',status:'any',q:'',sort:'status',dir:'asc',cols:{status:true,tag:true,cluster:true,acct:true,tasks:true,rev:true,image:true,cpu:true,mem:true,last:true},colsOpen:false,detail:null,restart:null},vals:function(c,s){
 var ORDER={bad:0,warn:1,info:2,ok:3},ST_L={bad:'Failing',warn:'Warning',info:'In progress',ok:'Steady'};
 var mins=function(t){var m=/^(\d+)\s*(min|h|d)/.exec(t);return m?(+m[1])*({min:1,h:60,d:1440}[m[2]]):0};
 var pct=function(v){return v==null?'–':v+'%'};
 var COLS=[
  {id:'status',label:'Status',key:function(v){return ORDER[v.status]},cell:function(v){return {kStatus:true,status:v.status,text:v.statusL}}},
  {id:'tag',label:'Tag',key:function(v){return v.tag},cell:function(v){return {kTag:true,tagCls:H.tagClass(v.tag,v.inherited),tagTip:v.inherited?'Inherited from cluster '+v.cluster:'Tag set on this service',text:v.tag}}},
  {id:'cluster',label:'Cluster',key:function(v){return v.cluster},cell:function(v){return {kLink:true,text:v.cluster}}},
  {id:'acct',label:'Account / region',key:function(v){return v.acct+v.region},cell:function(v){return {kTwo:true,cls:'two',text:v.acct,sub:v.region}}},
  {id:'tasks',label:'Tasks',num:true,key:function(v){return v.running/v.desired},cell:function(v){return {kMono:true,cls:'num',tone:v.running<v.desired?'c-bad':'',text:v.running+'/'+v.desired}}},
  {id:'rev',label:'Revision',num:true,key:function(v){return v.rev},cell:function(v){return {kMono:true,cls:'num',tone:'',text:':'+v.rev}}},
  {id:'image',label:'Image tag',key:function(v){return v.image},cell:function(v){return {kMono:true,tone:'',text:v.image}}},
  {id:'cpu',label:'CPU',num:true,key:function(v){return v.cpu==null?-1:v.cpu},cell:function(v){return {kMono:true,cls:'num',tone:v.cpu>70?'c-warn':'',text:pct(v.cpu)}}},
  {id:'mem',label:'Memory',num:true,key:function(v){return v.mem==null?-1:v.mem},cell:function(v){return {kMono:true,cls:'num',tone:v.mem>75?'c-warn':'',text:pct(v.mem)}}},
  {id:'last',label:'Last change',key:function(v){return mins(v.last)},cell:function(v){return {kText:true,text:v.last}}}];
 var VIEWS=[['all','All',function(){return true}],['problems','Problems',function(v){return v.status!=='ok'}],['pins','My pins',function(v){return !!v.pinned}],['prod','Prod',function(v){return v.tag==='Prod'}]];
 var q=s.q.toLowerCase(),all=D.services;
 var viewFn=VIEWS.filter(function(v){return v[0]===s.view})[0][2];
 var inView=all.filter(viewFn);
 var textOk=function(v){return !q||(v.name+' '+v.cluster+' '+v.acct+' '+v.region+' '+v.image+' '+v.tag).toLowerCase().indexOf(q)>=0};
 var base=inView.filter(textOk);
 var rowsRaw=base.filter(function(v){return s.status==='any'||v.status===s.status});
 var sortCol=s.sort==='name'?{key:function(v){return v.name}}:COLS.filter(function(x){return x.id===s.sort})[0];
 var dir=s.dir==='asc'?1:-1;
 rowsRaw=rowsRaw.slice().sort(function(a,b){var x=sortCol.key(a),y=sortCol.key(b);return (x<y?-1:x>y?1:a.name<b.name?-1:1)*dir});
 var sortBy=function(id){return function(){c.setState({sort:id,dir:s.sort===id&&s.dir==='asc'?'desc':'asc'})}};
 var arrow=function(id){return s.sort===id?(s.dir==='asc'?' ↑':' ↓'):''};
 var vis=COLS.filter(function(x){return s.cols[x.id]});
 var restartObj=function(v){return {name:v.name,rev:v.rev,cluster:v.cluster,tasks:v.running+'/'+v.desired}};
 var rows=rowsRaw.map(function(v){return {name:v.name,why:v.why||v.cluster,stripe:H.stripe(v.status),canRestart:v.status!=='info',noRestart:v.status==='info',
  cells:vis.map(function(x){return Object.assign({cls:'',kStatus:false,kTag:false,kLink:false,kTwo:false,kMono:false,kText:false,tagCls:'',tagTip:'',text:'',sub:'',tone:'',status:''},x.cell(v))}),
  open:function(){c.setState({detail:v.name})},restart:function(){c.setState({restart:restartObj(v)})}}});
 var dv=s.detail?D.svc(s.detail):null;
 var d=dv?{name:dv.name,status:dv.status,statusL:dv.statusL,hasWhy:!!dv.why,why:dv.why||'',cluster:dv.cluster,acct:dv.acct,region:dv.region,tag:dv.tag,tagCls:H.tagClass(dv.tag,dv.inherited),tagNote:dv.inherited?'Inherited from the cluster':'Set on this service',running:dv.running,desired:dv.desired,rev:dv.rev,image:dv.image,cpu:pct(dv.cpu),mem:pct(dv.mem),last:dv.last,restart:function(){c.setState({restart:restartObj(dv)})}}:{name:'',status:'mute',statusL:'',hasWhy:false,why:'',cluster:'',acct:'',region:'',tag:'',tagCls:'',tagNote:'',running:0,desired:0,rev:0,image:'',cpu:'',mem:'',last:'',restart:function(){}};
 var setCol=function(id){return function(){var m=Object.assign({},s.cols);m[id]=!m[id];c.setState({cols:m})}};
 var colN=vis.length,sortLabel=s.sort==='name'?'service':sortCol.label?sortCol.label.toLowerCase():s.sort;
 var stN=function(k){return base.filter(function(v){return k==='any'||v.status===k}).length};
 var closeAll=H.set(c,{detail:null,colsOpen:false});
 return {views:VIEWS.map(function(v){return {label:v[1],n:all.filter(v[2]).length,pressed:s.view===v[0],pick:H.set(c,{view:v[0]})}}),q:s.q,onQ:function(e){c.setState({q:e.target.value})},
  statusSeg:H.seg(c,'status',[['any','All',stN('any')],['bad','Failing',stN('bad')],['warn','Warning',stN('warn')],['info','In progress',stN('info')],['ok','Steady',stN('ok')]]),
  colsOpen:s.colsOpen,toggleCols:H.toggle(c,'colsOpen'),colsN:colN+1,colsTotal:COLS.length+1,colOpts:COLS.map(function(x){return {label:x.label,on:!!s.cols[x.id],pick:setCol(x.id)}}),
  resetCols:H.set(c,{cols:{status:true,tag:true,cluster:true,acct:true,tasks:true,rev:true,image:true,cpu:true,mem:true,last:true}}),
  minW:300+vis.length*115+260,svcSort:{arrow:arrow('name'),pick:sortBy('name')},
  heads:vis.map(function(x){return {label:x.label,cls:x.num?'num':'',arrow:arrow(x.id),aria:s.sort===x.id?(s.dir==='asc'?'ascending':'descending'):'none',pick:sortBy(x.id)}}),
  rows:rows,empty:!rows.length,total:all.length,sortL:sortLabel,clear:H.set(c,{view:'all',status:'any',q:''}),
  drawerOpen:!!s.detail,d:d,closeAll:closeAll,dlgRestart:!!s.restart,rs:s.restart||{name:'',rev:0,cluster:'',tasks:''},closeDlg:H.set(c,{restart:null}),
  confirmRestart:function(){c.setState({restart:null});H.toast(c,'Restart started.')()}};
}};
