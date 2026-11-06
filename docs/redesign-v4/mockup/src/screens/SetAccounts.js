var Screen=(function(){
 var REGIONS=['us-east-1','us-east-2','us-west-1','us-west-2','ca-central-1','eu-west-1','eu-west-2','eu-west-3','eu-central-1','eu-north-1','ap-south-1','ap-southeast-1','ap-southeast-2','ap-northeast-1','ap-northeast-2','ap-northeast-3','sa-east-1'];
 var DEFAULT_ON=['us-east-1','eu-west-1','eu-central-1'];
 var NOT_ENABLED=function(r){return /^(ap-|sa-)/.test(r)};
 var clustersIn={'us-east-1':3,'eu-west-1':1,'eu-central-1':1};
 var base=[
  {name:'homelab-admin',id:'482109375512',role:'arn:aws:iam::482109375512:role/harbormaster-access',regions:['us-east-1','eu-west-1','eu-central-1'],clusters:5,status:'warn',statusL:'Connected',note:'Slowed by AWS throttling. Data is 40 s old.',last:'12 s ago'},
  {name:'staging-example',id:'731022849906',role:'arn:aws:iam::731022849906:role/harbormaster-access',regions:['us-east-1'],clusters:1,status:'bad',statusL:'Error',note:'Data is 4 min old. Harbormaster cannot read service data in us-east-1; the role is missing a permission.',last:'4 min ago',err:'ecs:DescribeServices: AccessDenied\nUser: arn:aws:sts::731022849906:assumed-role/harbormaster-access/hm is not authorized to perform: ecs:DescribeServices on resource: arn:aws:ecs:us-east-1:731022849906:service/staging-web/*'},
  {name:'sandbox-example',id:'905417263810',role:'arn:aws:iam::905417263810:role/harbormaster-access',regions:['us-east-1'],clusters:0,status:'mute',statusL:'Disabled',note:'Not polled. Edit the account to scan regions again.',last:'3 d ago'}
 ];
 function testFor(a){return a.regions.map(function(r){if(a.name==='staging-example')return {region:r,cls:'bad',text:'AccessDenied: ecs:DescribeServices'};return {region:r,cls:'ok',text:(clustersIn[r]||0)+' clusters'}})}
 function toastFor(a){return a.name+': '+testFor(a).map(function(t){return t.region+': '+(t.cls==='ok'?t.text:t.text)}).join('; ')}
 function setOf(arr){var o={};REGIONS.forEach(function(r){o[r]=arr.indexOf(r)>=0});return o}
 return {state:{view:'normal',accts:base,tested:{},dlg:'',target:'',cnName:'',cnArn:'',cnRegs:setOf(DEFAULT_ON),cnTried:false,cnRes:null,edName:'',edRegs:{},edTried:false},
 vals:function(c,s){
  var all=s.accts,rows=s.view==='empty'?[]:all;
  var rowsV=rows.map(function(a){var t=s.tested[a.name];return {name:a.name,id:a.id,role:a.role,regions:a.regions,clusters:a.clusters,status:a.status,statusL:a.statusL,note:a.note,last:t?'just now':a.last,stripe:H.stripe(a.status),hasErr:!!a.err,err:a.err||'',tested:!!t,results:t?testFor(a):[],
   test:function(){var m={};m[a.name]=true;c.setState({tested:Object.assign({},c.state.tested,m)});H.toast(c,toastFor(a))()},
   edit:function(){c.setState({dlg:'edit',target:a.name,edName:a.name,edRegs:setOf(a.regions),edTried:false})},
   disc:function(){c.setState({dlg:'disc',target:a.name})}}});
  var arnOk=/^arn:aws:iam::\d{12}:role\/[\w+=,.@\/-]+$/.test(s.cnArn),nameBad=s.cnTried&&!s.cnName.trim(),arnBad=s.cnTried&&!arnOk;
  var cnPicked=REGIONS.filter(function(r){return s.cnRegs[r]});
  var res=s.cnRes?s.cnRes.map(function(t){return {region:t.region,cls:t.cls,mark:t.cls==='ok'?'✓':'✗',text:t.cls==='ok'?'· '+t.n+' clusters':'· '+t.err}}):[];
  var regList=function(map,key,prefix){return REGIONS.map(function(r){return {id:r,cid:'cn-r-'+r,eid:'ed-r-'+r,on:!!map[r],toggle:function(){var o={};var m=Object.assign({},c.state[key]);m[r]=!m[r];o[key]=m;if(key==='cnRegs')o.cnRes=null;c.setState(o)}}})};
  return {
   viewSeg:H.seg(c,'view',[['normal','Accounts'],['empty','No accounts'],['user','Non-admin']]),isAdmin:s.view!=='user',noAccess:s.view==='user',
   hasRows:rowsV.length>0,noRows:s.view==='empty',rows:rowsV,countL:rowsV.length+(rowsV.length===1?' account':' accounts'),poll:30,
   tags:D.tags,
   openConnect:function(){c.setState({dlg:'connect',cnName:'',cnArn:'',cnRegs:setOf(DEFAULT_ON),cnTried:false,cnRes:null})},
   closeDlg:H.set(c,{dlg:''}),
   showConnect:s.dlg==='connect',showEdit:s.dlg==='edit',showDisc:s.dlg==='disc',target:s.target,
   cnName:s.cnName,cnArn:s.cnArn,onCnName:function(e){c.setState({cnName:e.target.value})},onCnArn:function(e){c.setState({cnArn:e.target.value})},
   cnNameErr:nameBad,cnNameErrCls:nameBad?'err':'',cnArnErr:arnBad,cnArnErrCls:arnBad?'err':'',cnRegErr:s.cnTried&&!cnPicked.length,
   copyExt:H.toast(c,'Copied the external ID.'),cnRegions:regList(s.cnRegs,'cnRegs'),
   hasCnResults:res.length>0,cnResults:res,cnHasFail:res.some(function(t){return t.cls==='bad'}),
   testConnect:function(){var st=c.state,ok=st.cnName.trim()&&/^arn:aws:iam::\d{12}:role\/[\w+=,.@\/-]+$/.test(st.cnArn),picked=REGIONS.filter(function(r){return st.cnRegs[r]});
    if(!ok||!picked.length){c.setState({cnTried:true});return}
    var r=picked.map(function(g){return NOT_ENABLED(g)?{region:g,cls:'bad',err:'Region is not enabled for this account'}:{region:g,cls:'ok',n:clustersIn[g]||0}});
    if(r.some(function(t){return t.cls==='bad'})){c.setState({cnRes:r,cnTried:true});return}
    var nm=st.cnName.trim(),n=r.reduce(function(a,t){return a+t.n},0);
    c.setState({dlg:'',accts:st.accts.concat([{name:nm,id:st.cnArn.split(':')[4],role:st.cnArn,regions:picked,clusters:n,status:'ok',statusL:'Connected',note:'Data is current.',last:'just now'}]),view:'normal'});H.toast(c,'Connected '+nm+'.')()},
   edName:s.edName,onEdName:function(e){c.setState({edName:e.target.value})},edNameErr:s.edTried&&!s.edName.trim(),edNameErrCls:s.edTried&&!s.edName.trim()?'err':'',edRegions:regList(s.edRegs,'edRegs'),
   saveEdit:function(){var st=c.state;if(!st.edName.trim()){c.setState({edTried:true});return}
    var picked=REGIONS.filter(function(r){return st.edRegs[r]});if(!picked.length)picked=['us-east-1'];
    c.setState({dlg:'',accts:st.accts.map(function(a){return a.name===st.target?Object.assign({},a,{name:st.edName.trim(),regions:picked}):a})});H.toast(c,'Saved '+st.edName.trim()+'.')()},
   doDisc:function(){var st=c.state;c.setState({dlg:'',accts:st.accts.filter(function(a){return a.name!==st.target})});H.toast(c,'Disconnected '+st.target+'.')()}
  };
 }};
})();
