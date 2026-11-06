var Screen={state:{tag:'all',stat:'any',q:'',sort:'status',dir:'desc',tagEdit:null,tagPick:null,tagOv:{}},vals:function(c,s){
 var RANK={bad:3,warn:2,info:1,ok:0},mins=function(t){var m=/^(\d+)\s*(min|h|d)/.exec(t);return m?(+m[1])*({min:1,h:60,d:1440}[m[2]]):0};
 var all=D.clusters.map(function(x,i){var t=s.tagOv[x.name]||x.tag,ss=D.services.filter(function(v){return v.cluster===x.name});
  return Object.assign({},x,{tag:t,svcs:ss,idx:i})});
 var statOf=function(x){return x.status==='ok'?'ok':x.status==='info'?'info':'problems'};
 var q=s.q.toLowerCase();
 var textOk=function(x){return !q||(x.name+' '+x.acct+' '+x.region).toLowerCase().indexOf(q)>=0};
 var tagBase=all.filter(function(x){return textOk(x)&&(s.stat==='any'||statOf(x)===s.stat)});
 var statBase=all.filter(function(x){return textOk(x)&&(s.tag==='all'||x.tag===s.tag)});
 var COLS=[['name','Cluster',function(x){return x.name}],['tag','Env',function(x){return x.tag}],['status','Status',function(x){return RANK[x.status]}],['svcs','Services',function(x){return x.svcs.length}],['tasks','Tasks',function(x){return x.running}],['cpu','CPU 24h',function(x){return x.cpu==null?-1:x.cpu}],['mem','Memory 24h',function(x){return x.mem==null?-1:x.mem}],['vcpu','vCPU',function(x){return x.vcpu},'num'],['spot','Spot',function(x){return x.spot},'num'],['last','Last change',function(x){return mins(x.last)}]];
 var key=COLS.filter(function(k){return k[0]===s.sort})[0][2],dir=s.dir==='asc'?1:-1;
 var list=all.filter(function(x){return textOk(x)&&(s.tag==='all'||x.tag===s.tag)&&(s.stat==='any'||statOf(x)===s.stat)}).sort(function(a,b){var p=key(a),r=key(b);return (p<r?-1:p>r?1:a.name<b.name?-1:1)*dir});
 var rows=list.map(function(x){return {name:x.name,acct:x.acct,region:x.region,tag:x.tag,tagCls:H.tagClass(x.tag,false),status:x.status,statusL:x.statusL,stripe:H.stripe(x.status),
  preview:x.svcs.slice(0,2).map(function(v){return {name:v.name}}),hasMore:x.svcs.length>2,moreN:x.svcs.length-2,tasks:x.running+'/'+x.desired,taskTone:x.running<x.desired?'c-bad':'',
  insights:x.insights,noInsights:!x.insights,cpu:x.cpu,mem:x.mem,cpuSpark:H.spark(H.series(x.idx+3,24,x.cpu||20,4)),memSpark:H.spark(H.series(x.idx+9,24,x.mem||40,2)),vcpu:x.vcpu,spot:x.spot,last:x.last,
  editTag:function(){c.setState({tagEdit:x.name,tagPick:x.tag})}}});
 var heads=COLS.map(function(k){var on=s.sort===k[0];return {label:k[1],cls:k[3]||'',sortable:true,plain:false,arrow:on?(s.dir==='asc'?' ↑':' ↓'):'',aria:on?(s.dir==='asc'?'ascending':'descending'):'none',pick:function(){c.setState({sort:k[0],dir:on&&s.dir==='asc'?'desc':'asc'})}}});
 var tagN=function(t){return statBase.filter(function(x){return t==='all'||x.tag===t}).length};
 var cur=s.tagEdit?all.filter(function(x){return x.name===s.tagEdit})[0]:null;
 var close=H.set(c,{tagEdit:null,tagPick:null});
 var same=!!cur&&cur.tag===s.tagPick;
 return {tagSeg:H.seg(c,'tag',[['all','All',tagN('all')]].concat(D.tags.map(function(t){return [t.name,t.name,tagN(t.name)]}))),
  statSeg:H.seg(c,'stat',[['any','Any status',tagBase.length],['problems','Problems',tagBase.filter(function(x){return statOf(x)==='problems'}).length],['info','Deploying',tagBase.filter(function(x){return statOf(x)==='info'}).length],['ok','Healthy',tagBase.filter(function(x){return statOf(x)==='ok'}).length]]),
  q:s.q,onQ:function(e){c.setState({q:e.target.value})},heads:heads,rows:rows,empty:!rows.length,loadError:false,clear:H.set(c,{tag:'all',stat:'any',q:''}),
  rangeL:rows.length?'1–'+rows.length+' of '+rows.length:'0 of 0',
  tagOpen:!!cur,tagFor:s.tagEdit||'',tagSame:same,tagDiff:!same,closeTag:close,
  tagOpts:D.tags.map(function(t){return {name:t.name,cls:H.tagClass(t.name,false),note:cur&&cur.tag===t.name?'Current':'',on:s.tagPick===t.name,mark:s.tagPick===t.name?'ok':'mute',pick:H.set(c,{tagPick:t.name})}}),
  saveTag:function(){var m=Object.assign({},s.tagOv);m[s.tagEdit]=s.tagPick;c.setState({tagOv:m,tagEdit:null,tagPick:null});H.toast(c,'Tag saved.')()}};
}};
