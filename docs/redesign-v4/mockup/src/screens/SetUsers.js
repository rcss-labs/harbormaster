var Screen=(function(){
 var ORDER='VLODET';
 var ACTS=[['V','View'],['L','Logs'],['O','Operate'],['D','Deploy'],['E','Edit task definition'],['T','Terminal']];
 var PRESETS=[['','None'],['V','Viewer'],['VLO','Operator'],['VLOD','Deployer'],['VLODET','Full']];
 var FAKE_TEMP='FAKE-temp-pass-0000';
 function norm(str){return ORDER.split('').filter(function(k){return str.indexOf(k)>=0}).join('')}
 function presetName(l){for(var i=0;i<PRESETS.length;i++)if(PRESETS[i][0]===l)return PRESETS[i][1];return 'Custom'}
 function toggleLetter(cur,k){var on=cur.indexOf(k)>=0;if(k==='V')return on?'':'V';return norm(on?cur.replace(k,''):cur+k+'V')}
 var users=[
  {name:'pavel',email:'pavel@example.test',admin:true,pw:true,bg:true,link:'none',you:true,last:'just now',perms:{}},
  {name:'dana',email:'dana@example.test',admin:false,pw:false,bg:false,link:'linked',last:'37 min ago',perms:{Dev:'VLODET',Staging:'VLODE',Prod:'VL'}},
  {name:'omer',email:'omer@example.test',admin:false,pw:false,bg:false,link:'expired',last:'3 min ago',perms:{Dev:'VLO',Staging:'V',Prod:''}},
  {name:'oncall',email:'oncall@example.test',admin:false,pw:false,bg:false,link:'pending',last:'2 h ago',perms:{Dev:'',Staging:'VLOT',Prod:'VLOT'}}
 ];
 return {state:{users:users,view:'admin',sso:true,pop:'',menu:'',dlg:'',cfKind:'',cfUser:'',auName:'',auAdmin:false,auTried:false,auStep:1,auCreated:'',lkUser:'',lkValue:'',lkTried:false,tempUser:'',tempCode:''},
 vals:function(c,s){
  var tags=D.tags;
  function upd(name,fn){c.setState({users:c.state.users.map(function(u){return u.name===name?fn(u):u})})}
  function setPerm(name,tag,letters){upd(name,function(u){var p=Object.assign({},u.perms);p[tag]=letters;return Object.assign({},u,{perms:p})})}
  var matrix=s.users.map(function(u){
   var cells=tags.map(function(t,ti){var l=u.perms[t.name]||'',key=u.name+'|'+t.name,open=s.pop===key,pn=presetName(l),isCustom=pn==='Custom';
    var aria=u.name+' on '+t.name+': '+pn+(l?' ('+l+')':'')+'. Edit permissions';
    var presets=PRESETS.map(function(p){return {v:p[0],label:p[1],sel:p[0]===l,dis:false}});
    if(isCustom)presets.push({v:'__custom',label:'Custom ('+l+')',sel:true,dis:true});
    return {user:u.name,tag:t.name,preset:pn,letters:l?' '+l:'',aria:aria,open:open,selId:'pm-'+u.name+'-'+t.name,presets:presets,
     popStyle:ti===tags.length-1&&tags.length>2?'right:0':'left:0',hint:'Any action also turns on View. Turning View off clears the rest.',
     toggle:function(){c.setState({pop:c.state.pop===key?'':key})},close:H.set(c,{pop:''}),
     onPreset:function(e){if(e.target.value==='__custom')return;setPerm(u.name,t.name,e.target.value)},
     acts:ACTS.map(function(a){return {k:a[0],name:a[1],on:l.indexOf(a[0])>=0,toggle:function(){var cur=(c.state.users.filter(function(z){return z.name===u.name})[0].perms[t.name])||'';setPerm(u.name,t.name,toggleLetter(cur,a[0]))}}})}});
   return {name:u.name,role:u.admin?'Global admin':'User',admin:u.admin,notAdmin:!u.admin,cells:cells}});
  var cfs={
   reset:function(u){return {title:u.pw?'Reset the password of '+u.name+'?':'Set a password for '+u.name+'?',sub:u.pw?'They get a new temporary password and must choose their own at next sign-in. Their sessions end now.':'They get a temporary password and must choose their own at next sign-in.',label:u.pw?'Reset password':'Set password',cls:'btn-p',go:function(){upd(u.name,function(z){return Object.assign({},z,{pw:true})});c.setState({dlg:'',tempUser:u.name,tempCode:FAKE_TEMP})}}},
   admin:function(u){return {title:u.admin?'Remove global admin from '+u.name+'?':'Make '+u.name+' a global admin?',sub:u.admin?'They keep only the per-tag permissions in the matrix.':'Global admins can do everything on every tag, and manage users, tags and AWS accounts.',label:u.admin?'Remove global admin':'Make global admin',cls:'btn-p',go:function(){upd(u.name,function(z){return Object.assign({},z,{admin:!z.admin})});c.setState({dlg:''});H.toast(c,(u.admin?'Removed global admin from ':'Made global admin: ')+u.name+'.')()}}},
   bg:function(u){return {title:u.bg?'Remove break-glass from '+u.name+'?':'Make '+u.name+' a break-glass account?',sub:'Break-glass accounts can sign in with a password even when single sign-on is on. Keep this to a few global admins.',label:u.bg?'Remove break-glass':'Make break-glass',cls:'btn-p',go:function(){upd(u.name,function(z){return Object.assign({},z,{bg:!z.bg})});c.setState({dlg:''});H.toast(c,u.bg?'Removed break-glass from '+u.name+'.':u.name+' is now break-glass.')()}}},
   unlink:function(u){return {title:'Unlink '+u.name+' from SSO?',sub:u.pw?'Their SSO sessions end now. They keep their password.':'Their SSO sessions end now. They get a temporary password and must choose their own at next sign-in.',label:'Unlink',cls:'btn-p',go:function(){upd(u.name,function(z){return Object.assign({},z,{link:'none',pw:true})});c.setState(u.pw?{dlg:''}:{dlg:'',tempUser:u.name,tempCode:FAKE_TEMP});if(u.pw)H.toast(c,'Unlinked '+u.name+'.')()}}},
   del:function(u){return {title:'Delete '+u.name+'?',sub:'Their sessions end now. Their entries in operations and audit logs stay.',label:'Delete user',cls:'btn-dp',go:function(){c.setState({users:c.state.users.filter(function(z){return z.name!==u.name}),dlg:''});H.toast(c,'Deleted '+u.name+'.')()}}}
  };
  var cfUser=s.users.filter(function(u){return u.name===s.cfUser})[0];
  var cf=cfUser&&cfs[s.cfKind]?cfs[s.cfKind](cfUser):{title:'',sub:'',label:'',cls:'',go:function(){}};
  function ask(kind,u){return function(){c.setState({dlg:'confirm',cfKind:kind,cfUser:u.name,menu:''})}}
  function openLink(u){return function(){c.setState({dlg:'link',lkUser:u.name,lkValue:u.link==='none'||u.link==='linked'?'':u.name+'@example.test',lkTried:false,menu:''})}}
  var userRows=s.users.map(function(u){
   var pills=[];if(u.pw)pills.push({cls:'mute',label:'Password'});
   if(s.sso){if(u.link==='linked')pills.push({cls:'ok',label:'SSO linked'});if(u.link==='pending')pills.push({cls:'info',label:'Link pending'});if(u.link==='expired')pills.push({cls:'warn',label:'Link expired'})}
   if(u.bg)pills.push({cls:'warn',label:'Break-glass'});
   var see=u.admin?[]:tags.filter(function(t){return u.perms[t.name]}).map(function(t){return t.name});
   var items=[];
   items.push({label:u.pw?'Reset password':'Set password',click:ask('reset',u)});
   if(s.sso)items.push({label:u.bg?'Remove break-glass':'Make break-glass',dis:!u.pw&&!u.bg,reason:!u.pw&&!u.bg?'Set a password for '+u.name+' first.':'',click:ask('bg',u)});
   if(s.sso&&(u.link==='none'))items.push({label:'Link SSO…',click:openLink(u)});
   if(s.sso&&(u.link==='pending'||u.link==='expired')){items.push({label:'Change pending link…',click:openLink(u)});items.push({label:'Clear pending link',click:function(){upd(u.name,function(z){return Object.assign({},z,{link:'none'})});c.setState({menu:''});H.toast(c,'Cleared the pending link for '+u.name+'.')()}})}
   if(s.sso&&u.link==='linked')items.push({label:'Unlink SSO…',click:ask('unlink',u)});
   items.push({label:u.admin?'Remove global admin':'Make global admin',click:ask('admin',u)});
   items.push({label:'Delete',cls:'c-bad',dis:!!u.you,reason:u.you?'You cannot delete yourself':'',click:ask('del',u)});
   items=items.map(function(m){return {label:m.label,cls:m.cls||'',dis:!!m.dis,reason:m.reason||'',click:m.click}});
   return {name:u.name,email:u.email,youL:u.you?'(you)':'',pills:pills,roleL:u.admin?'Global admin':'User',roleCls:u.admin?'info':'mute',
    seeText:u.admin?'Everything':(see.length?'':'Nothing yet'),seeCls:u.admin?'':'mut',see:see,last:u.last||'never',stripe:u.link==='expired'&&s.sso?'stripe-warn':'',
    menuOpen:s.menu===u.name,toggleMenu:function(){c.setState({menu:c.state.menu===u.name?'':u.name,pop:''})},closeMenu:H.set(c,{menu:''}),items:items}});
  var auBad=s.auTried&&!/^[a-z0-9._-]{2,32}$/.test(s.auName);
  var lkBad=s.lkTried&&!s.lkValue.trim();
  return {
   modeSeg:[{label:'SSO on',pressed:s.sso,pick:H.set(c,{sso:true,menu:''})},{label:'SSO off',pressed:!s.sso,pick:H.set(c,{sso:false,menu:''})}],
   viewSeg:H.seg(c,'view',[['admin','Global admin'],['user','Non-admin']]),isAdmin:s.view==='admin',noAccess:s.view!=='admin',sso:s.sso,
   intro:s.sso?'Global admins can do everything. Every other user gets permissions per tag. Sign-in is single sign-on; break-glass accounts can also use a password.':'Global admins can do everything. Every other user gets permissions per tag. Sign-in is username and password.',
   tagCols:tags,tagN:tags.length,matrix:matrix,legend:ACTS.map(function(a){return {k:a[0],name:a[1]}}),
   userRows:userRows,userCount:s.users.length+(s.users.length===1?' user':' users'),
   hasTemp:!!s.tempCode&&s.dlg!=='add',tempUser:s.tempUser,tempCode:s.tempCode,dismissTemp:H.set(c,{tempCode:'',tempUser:''}),copyTemp:H.toast(c,'Copied the temporary password.'),
   openAdd:H.set(c,{dlg:'add',auName:'',auAdmin:false,auTried:false,auStep:1,menu:'',pop:''}),closeDlg:function(){c.setState({dlg:'',tempCode:c.state.dlg==='add'?'':c.state.tempCode})},
   showAdd:s.dlg==='add',auStep1:s.auStep===1,auStep2:s.auStep===2,auName:s.auName,auAdmin:s.auAdmin,auErr:auBad,auErrCls:auBad?'err':'',auCreated:s.auCreated,
   onAuName:function(e){c.setState({auName:e.target.value})},onAuAdmin:function(e){c.setState({auAdmin:e.target.checked})},
   addUser:function(){var st=c.state;if(!/^[a-z0-9._-]{2,32}$/.test(st.auName)){c.setState({auTried:true});return}
    c.setState({users:st.users.concat([{name:st.auName,email:st.auName+'@example.test',admin:st.auAdmin,pw:true,bg:false,link:'none',last:'never',perms:{}}]),auStep:2,auCreated:st.auName,tempCode:FAKE_TEMP,tempUser:st.auName})},
   showConfirm:s.dlg==='confirm'&&!!cfUser,cf:cf,
   showLink:s.dlg==='link',lkUser:s.lkUser,lkValue:s.lkValue,lkErr:lkBad,lkErrCls:lkBad?'err':'',onLk:function(e){c.setState({lkValue:e.target.value})},
   setLink:function(){var st=c.state;if(!st.lkValue.trim()){c.setState({lkTried:true});return}
    c.setState({users:st.users.map(function(u){return u.name===st.lkUser?Object.assign({},u,{link:'pending'}):u}),dlg:''});H.toast(c,'Pending link set for '+st.lkUser+'. It expires in 14 days.')()}
  };
 }};
})();
