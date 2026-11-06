var Screen=(function(){
 var COLORS=[['neutral','Neutral','var(--text-2)'],['red','Red','var(--bad)'],['amber','Amber','var(--warn)'],['blue','Blue','var(--accent)'],['violet','Violet','var(--info)'],['green','Green','var(--ok)']];
 function cv(color){var f=COLORS.filter(function(c){return c[0]===color})[0];return f?f[2]:'var(--text-2)'}
 function chipStyle(color,inh){var v=cv(color);return 'color:'+v+';border-color:color-mix(in srgb,'+v+' 45%,var(--border));border-style:'+(inh?'dashed':'solid')}
 var PRESETS={'':'None','V':'Viewer','VLO':'Operator','VLOD':'Deployer','VLODET':'Full'};
 function presetName(l){return PRESETS.hasOwnProperty(l)?PRESETS[l]:'Custom '+l}
 function plural(n,one,many){return n+' '+(n===1?one:many)}
 var cTag={},sTag={};
 D.clusters.forEach(function(c){cTag[c.name]=c.tag});
 D.services.forEach(function(x){sTag[x.name]=x.inherited?'':x.tag});
 var exp0={'core-services':true};
 return {state:{view:'admin',tags:D.tags.map(function(t){return {name:t.name,color:t.color,def:!!t.def}}),cTag:cTag,sTag:sTag,open:exp0,page:0,dlg:'',tgMode:'create',tgOld:'',tgName:'',tgColor:'blue',tgTried:false,delName:'',edKind:'',edName:'',edCluster:'',edVal:''},
 vals:function(c,s){
  var colorOf=function(n){var t=s.tags.filter(function(x){return x.name===n})[0];return t?t.color:'neutral'};
  var effSvc=function(x){return s.sTag[x.name]||s.cTag[x.cluster]};
  var tagRows=s.tags.map(function(t){
   var cl=D.clusters.filter(function(x){return s.cTag[x.name]===t.name}).length,dir=D.services.filter(function(x){return s.sTag[x.name]===t.name}).length,eff=D.services.filter(function(x){return effSvc(x)===t.name}).length;
   var us=D.users.filter(function(u){return !u.admin&&u.perms&&u.perms[t.name]}).map(function(u){return {name:u.name,preset:presetName(u.perms[t.name])}});
   return {name:t.name,def:t.def,style:chipStyle(t.color),clusters:cl,direct:dir,effect:eff,users:us,hasUsers:us.length>0,noUsers:!us.length,lockTip:t.def?'Dev is the default tag':'',
    rename:function(){c.setState({dlg:'tag',tgMode:'rename',tgOld:t.name,tgName:t.name,tgColor:t.color,tgTried:false})},del:function(){c.setState({dlg:'del',delName:t.name})}}});
  /* assignments tree */
  var rows=[],accts=[];D.clusters.forEach(function(x){if(accts.indexOf(x.acct)<0)accts.push(x.acct)});
  var chipsOf=function(svcs){var seen=[];svcs.forEach(function(x){var n=effSvc(x);if(seen.indexOf(n)<0)seen.push(n)});return seen.map(function(n){return {name:n,style:chipStyle(colorOf(n))}})};
  var tagOpts=function(cur){return s.tags.map(function(t){return {v:t.name,label:t.name,sel:t.name===cur}})};
  accts.forEach(function(a){
   var aCl=D.clusters.filter(function(x){return x.acct===a}),aSv=D.services.filter(function(x){return x.acct===a});
   rows.push({key:'a:'+a,kind:'g',label:a,summary:plural(aCl.length,'cluster','clusters')+' · '+plural(aSv.length,'service','services'),pad:'',chips:chipsOf(aSv),rowCls:''});
   var regs=[];aCl.forEach(function(x){if(regs.indexOf(x.region)<0)regs.push(x.region)});
   regs.forEach(function(r){
    var rCl=aCl.filter(function(x){return x.region===r}),rSv=aSv.filter(function(x){return x.region===r});
    rows.push({key:'r:'+a+r,kind:'g',label:r,summary:plural(rCl.length,'cluster','clusters')+' · '+plural(rSv.length,'service','services'),pad:'padding-left:28px',chips:chipsOf(rSv),rowCls:''});
    rCl.forEach(function(cl){
     var svs=D.services.filter(function(x){return x.cluster===cl.name}),open=!!s.open[cl.name];
     rows.push({key:'c:'+cl.name,kind:'c',label:cl.name,summary:plural(svs.length,'service','services'),pad:'padding-left:44px',open:open,tag:s.cTag[cl.name],inh:false,cluster:cl.name});
     if(open)svs.forEach(function(x){rows.push({key:'s:'+x.name,kind:'s',label:x.name,summary:s.sTag[x.name]?'Tagged directly':'Inherits from '+cl.name,pad:'padding-left:92px',tag:effSvc(x),inh:!s.sTag[x.name],direct:s.sTag[x.name]||'',cluster:cl.name})});
    })})});
  var PER=15,pages=Math.max(1,Math.ceil(rows.length/PER)),pg=Math.min(s.page,pages-1),slice=rows.slice(pg*PER,pg*PER+PER);
  function applyTag(kind,name,cluster,val){
   if(kind==='c'){var m=Object.assign({},c.state.cTag);m[name]=val;c.setState({cTag:m});H.toast(c,name+' is now '+val)()}
   else{var q=Object.assign({},c.state.sTag);q[name]=val;c.setState({sTag:q});H.toast(c,name+' is now '+(val||c.state.cTag[cluster])+(val?'':' (inherited)'))()}}
  function editTarget(r){return function(){c.setState({dlg:'edit',edKind:r.kind,edName:r.label,edCluster:r.cluster,edVal:r.kind==='c'?r.tag:(r.direct||'')})}}
  var pageRows=slice.map(function(r){
   var isG=r.kind==='g',isC=r.kind==='c',isS=r.kind==='s';
   return {label:r.label,summary:r.summary,pad:r.pad,isGroup:isG,isCluster:isC,isSvc:isS,rowCls:isG?'':'',chips:r.chips||[],hasSelect:!isG,hasChip:!isG,
    open:!!r.open,closed:isC&&!r.open,toggleLabel:(r.open?'Collapse ':'Expand ')+r.label,toggle:function(){var o=Object.assign({},c.state.open);o[r.label]=!o[r.label];c.setState({open:o})},
    selId:'as-'+r.key.replace(/[^\w-]/g,'-'),
    opts:isC?tagOpts(r.tag):[{v:'',label:'Inherit ('+s.cTag[r.cluster]+')',sel:!r.direct}].concat(s.tags.map(function(t){return {v:t.name,label:t.name,sel:t.name===r.direct}})),
    onTag:function(e){applyTag(r.kind,r.label,r.cluster,e.target.value)},
    chipName:r.tag,chipStyle:r.tag?chipStyle(colorOf(r.tag),r.inh):'',inhL:r.inh?' (inherited)':'',editChip:editTarget(r)}});
  var tgNameTrim=s.tgName.trim(),dup=s.tags.some(function(t){return t.name.toLowerCase()===tgNameTrim.toLowerCase()&&t.name!==s.tgOld});
  var okName=/^[A-Za-z][A-Za-z0-9 _-]{0,23}$/.test(tgNameTrim),tgBad=s.tgTried&&(!okName||dup);
  var delTag=s.delName,dCl=D.clusters.filter(function(x){return s.cTag[x.name]===delTag}).length,dSv=D.services.filter(function(x){return s.sTag[x.name]===delTag}).length;
  var dUs=D.users.filter(function(u){return !u.admin&&u.perms&&u.perms[delTag]}).map(function(u){return u.name});
  var etCluster=s.edCluster,etOpts=(s.edKind==='s'?[{id:'et-inherit',on:s.edVal==='',v:'',chip:s.cTag[etCluster]||'',style:chipStyle(colorOf(s.cTag[etCluster]),true),text:'Inherit from cluster '+etCluster}]:[]).concat(s.tags.map(function(t){return {id:'et-'+t.name.replace(/[^\w-]/g,'-'),on:s.edVal===t.name,v:t.name,chip:t.name,style:chipStyle(t.color),text:t.def?'Default tag':''}}));
  etOpts=etOpts.map(function(o){return {id:o.id,on:o.on,chip:o.chip,style:o.style,text:o.text,pick:function(){c.setState({edVal:o.v})}}});
  return {
   viewSeg:H.seg(c,'view',[['admin','Global admin'],['user','Non-admin']]),isAdmin:s.view==='admin',noAccess:s.view!=='admin',
   tagRows:tagRows,pageRows:pageRows,
   expandAll:H.set(c,{open:D.clusters.reduce(function(o,x){o[x.name]=true;return o},{}),page:0}),collapseAll:H.set(c,{open:{},page:0}),
   rangeL:'Showing '+(pg*PER+1)+' to '+Math.min(rows.length,pg*PER+PER)+' of '+rows.length+' rows',pageL:'Page '+(pg+1)+' of '+pages,noPrev:pg<=0,noNext:pg>=pages-1,
   prev:function(){c.setState({page:Math.max(0,pg-1)})},next:function(){c.setState({page:Math.min(pages-1,pg+1)})},
   closeDlg:H.set(c,{dlg:''}),
   openCreate:H.set(c,{dlg:'tag',tgMode:'create',tgOld:'',tgName:'',tgColor:'blue',tgTried:false}),
   showTag:s.dlg==='tag',tgTitle:s.tgMode==='create'?'Create tag':'Rename tag '+s.tgOld,
   tgSub:s.tgMode==='create'?'New tags start with no users. Only global admins can see resources with it until you give permissions.':'Clusters, services and permissions keep pointing at it.',
   tgName:s.tgName,onTgName:function(e){c.setState({tgName:e.target.value})},tgErr:tgBad,tgErrCls:tgBad?'err':'',
   tgErrText:dup?'A tag named '+tgNameTrim+' already exists. Pick another name.':'Use 1 to 24 characters, starting with a letter. Letters, digits, spaces, underscores and hyphens are allowed.',
   tgBtn:s.tgMode==='create'?'Create tag':'Save',
   colors:COLORS.map(function(k){return {id:'tg-color-'+k[0],label:k[1],on:s.tgColor===k[0],style:chipStyle(k[0]),pick:function(){c.setState({tgColor:k[0]})}}}),
   saveTag:function(){var st=c.state,nm=st.tgName.trim(),d=st.tags.some(function(t){return t.name.toLowerCase()===nm.toLowerCase()&&t.name!==st.tgOld});
    if(!/^[A-Za-z][A-Za-z0-9 _-]{0,23}$/.test(nm)||d){c.setState({tgTried:true});return}
    if(st.tgMode==='create'){c.setState({dlg:'',tags:st.tags.concat([{name:nm,color:st.tgColor,def:false}])});H.toast(c,'Created tag '+nm+'. Assign it on this page.')();return}
    var ren=function(o){var r={};Object.keys(o).forEach(function(k){r[k]=o[k]===st.tgOld?nm:o[k]});return r};
    c.setState({dlg:'',tags:st.tags.map(function(t){return t.name===st.tgOld?{name:nm,color:st.tgColor,def:t.def}:t}),cTag:ren(st.cTag),sTag:ren(st.sTag)});H.toast(c,'Tag saved.')()},
   showDel:s.dlg==='del',delName:delTag,
   delImpact:[plural(dCl,'cluster becomes','clusters become')+' Dev.',plural(dSv,'service goes','services go')+" back to inheriting their cluster's tag.",plural(dUs.length,'user loses','users lose')+' the permissions they had on '+delTag+(dUs.length?' ('+dUs.join(', ')+').':'.')],
   doDelete:function(){var st=c.state,nm=st.delName,m1={},m2={};Object.keys(st.cTag).forEach(function(k){m1[k]=st.cTag[k]===nm?'Dev':st.cTag[k]});Object.keys(st.sTag).forEach(function(k){m2[k]=st.sTag[k]===nm?'':st.sTag[k]});
    c.setState({dlg:'',tags:st.tags.filter(function(t){return t.name!==nm}),cTag:m1,sTag:m2});H.toast(c,'Deleted tag '+nm+'.')()},
   showEdit:s.dlg==='edit',etName:s.edName,etOpts:etOpts,
   saveEdit:function(){var st=c.state;applyTag(st.edKind,st.edName,st.edCluster,st.edVal);c.setState({dlg:''})}
  };
 }};
})();
