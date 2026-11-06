var Screen=(function(){
 var STAGES={
  new:{idp:false,opts:false,tested:false,bg:false,on:false,testRes:''},
  nobg:{idp:true,opts:true,tested:true,bg:false,on:false,testRes:'pass'},
  ready:{idp:true,opts:true,tested:true,bg:true,on:false,testRes:'pass'},
  on:{idp:true,opts:true,tested:true,bg:true,on:true,testRes:'pass'}};
 var NOBG_TIP='Flag at least one global admin as break-glass before turning on single sign-on. Create one with harbormaster create-admin, or flag an existing admin who has a password.';
 var NAMEIDS=[['','Let the IdP choose'],['email','Email address'],['persistent','Persistent']];
 var SAVED={entity:'https://idp.example.test/metadata',sso:'https://idp.example.test/sso/redirect'};
 return {state:{view:'admin',idp:true,opts:true,tested:true,bg:false,on:false,testRes:'pass',outcome:'create',pw:2,keyBad:false,page:'loaded',
  mode:'url',url:'',urlTried:false,file:'',fileTried:false,entity:SAVED.entity,ssoUrl:SAVED.sso,cert:'',manTried:false,fetched:'2 min ago',
  optLabel:'Sign in with Example IdP',nameId:'email',optEmail:'email',optDisplay:'displayName',optUser:'',optSkew:'60',skewTried:false,dlg:'',replace:''},
 vals:function(c,s){
  var idp=s.idp;
  function stage(k){return function(){c.setState(STAGES[k])}}
  var cur=s.on?'on':(s.idp&&s.bg?'ready':(s.idp?'nobg':'new'));
  function doIdp(msg){c.setState({idp:true,fetched:'just now',dlg:'',replace:'',entity:c.state.entity||SAVED.entity,ssoUrl:c.state.ssoUrl||SAVED.sso,cert:''});H.toast(c,msg)()}
  function gate(kind){ /* Replacing an existing configuration needs a confirm */
   var st=c.state;if(st.idp){c.setState({dlg:'replace',replace:kind});return}run(kind)}
  function run(kind){var st=c.state;
   if(kind==='url'){if(!/^https:\/\/\S+/.test(st.url)){c.setState({urlTried:true,dlg:''});return}doIdp('Fetched the identity provider metadata.')}
   if(kind==='file'){if(!st.file){c.setState({fileTried:true,dlg:''});return}doIdp('Uploaded the identity provider metadata.')}
   if(kind==='manual'){var needCert=!st.idp&&!st.cert.trim();if(!st.entity.trim()||!st.ssoUrl.trim()||needCert){c.setState({manTried:true,dlg:''});return}doIdp('Saved the identity provider.')}}
  var entityChanged=s.idp&&s.mode==='manual'&&s.entity.trim()!==''&&s.entity.trim()!==SAVED.entity;
  var steps=[{label:'Identity provider',href:'#sso-idp',done:s.idp,hint:s.idp?'Metadata saved':'Fetch metadata or enter it by hand'},
   {label:'Service provider',href:'#sso-sp',done:s.idp,hint:s.idp?'Certificate created':'Created when you save the identity provider'},
   {label:'Sign-in options',href:'#sso-options',done:s.opts,hint:s.opts?'Saved':'Button label and attribute names'},
   {label:'Test sign-in',href:'#sso-test',done:s.tested,hint:s.tested?'Passed':'Check what a sign-in would do'},
   {label:'Turn on',href:'#sso-on',done:s.on,hint:s.on?'Single sign-on is on':'Needs a break-glass admin'}];
  var firstOpen=-1;steps.forEach(function(p,i){if(!p.done&&firstOpen<0)firstOpen=i});
  steps=steps.map(function(p,i){return {n:i+1,label:p.label,href:p.href,hint:p.hint,cls:p.done?'ok':(i===firstOpen?'info':'mute'),stateL:p.done?'Done':(i===firstOpen?'Next':'To do')}});
  var doneN=steps.filter(function(p){return p.stateL==='Done'}).length;
  var onDisabled=!s.bg||!s.idp,onTip=!s.bg?NOBG_TIP:(!s.idp?'Configure the identity provider first.':'');
  var warnings=[];if(!s.on&&s.pw>0)warnings.push(s.pw+' users have a password but are not break-glass. They can only sign in with SSO once it is on.');
  if(!s.on&&!s.bg)warnings.push('No global admin is flagged break-glass. If the identity provider fails, nobody can sign in.');
  var cf={title:'',sub:'',label:'',cls:'btn-p',go:function(){}};
  if(s.dlg==='on')cf={title:'Turn on single sign-on?',sub:s.pw>0?s.pw+' users have a password but are not break-glass; they can only sign in with SSO while it is on. Passwords are kept and work again if you turn it off.':'Only break-glass accounts can sign in with a password while single sign-on is on.',label:'Turn on',cls:'btn-p',go:function(){c.setState({on:true,dlg:''});H.toast(c,'Single sign-on is on.')()}};
  if(s.dlg==='off')cf={title:'Turn off single sign-on?',sub:'Everyone signed in with SSO is signed out now. Users with a password can sign in with it again; users without one need an admin to set one.',label:'Turn off',cls:'btn-dp',go:function(){c.setState({on:false,dlg:''});H.toast(c,'Single sign-on is off.')()}};
  if(s.dlg==='replace')cf={title:'Replace the identity provider configuration?',sub:'Changing the identity provider signs out every SSO user and detaches every linked and pending SSO identity. Users keep their permissions and must be linked again, or will be created again at their next sign-in.',label:'Continue',cls:'btn-p',go:function(){run(c.state.replace)}};
  if(s.dlg==='gen')cf={title:'Generate new certificate?',sub:'The current certificate keeps decrypting assertions for 7 days. Update your identity provider with the new SP metadata.',label:'Generate new certificate',cls:'btn-p',go:function(){c.setState({dlg:'',keyBad:false});H.toast(c,'New certificate generated.')()}};
  var skewBad=s.skewTried&&!(/^\d+$/.test(s.optSkew)&&Number(s.optSkew)<=300);
  var outcomes={create:'Would create user dana.',signin:'Would sign in dana.',link:'Would link dana (pending link).'};
  function inp(key){return function(e){var o={};o[key]=e.target.value;c.setState(o)}}
  return {
   viewSeg:H.seg(c,'view',[['admin','Global admin'],['user','Non-admin']]),isAdmin:s.view==='admin',noAccess:s.view!=='admin',
   stageSeg:[['new','Not started'],['nobg','Configured, no break-glass'],['ready','Ready to turn on'],['on','SSO on']].map(function(o){return {label:o[1],pressed:cur===o[0],pick:stage(o[0])}}),
   resSeg:[['','None'],['loading','Loading'],['pass','Passed'],['fail','Failed']].map(function(o){return {label:o[1],pressed:s.testRes===o[0],pick:H.set(c,{testRes:o[0],tested:o[0]==='pass'?true:c.state.tested})}}),
   outSeg:H.seg(c,'outcome',[['create','Create user'],['signin','Sign in'],['link','Link']]),
   pwSeg:H.seg(c,'pw',[[0,'0 users'],[2,'2 users']]),
   keySeg:[{label:'Readable',pressed:!s.keyBad,pick:H.set(c,{keyBad:false})},{label:'Undecryptable',pressed:s.keyBad,pick:H.set(c,{keyBad:true})}],
   pageSeg:H.seg(c,'page',[['loaded','Loaded'],['loading','Loading'],['error','Error']]),
   pgLoaded:s.page==='loaded',pgLoading:s.page==='loading',pgError:s.page==='error',reload:H.set(c,{page:'loaded'}),
   pillL:s.on?'SSO on':'SSO off',pillCls:s.on?'ok':'mute',doneN:doneN,progStyle:H.meterStyle(doneN*20),steps:steps,
   idp:idp,noIdp:!idp,
   modeSeg:H.seg(c,'mode',[['url','Metadata URL'],['file','Metadata file'],['manual','Manual']]),modeUrl:s.mode==='url',modeFile:s.mode==='file',modeManual:s.mode==='manual',
   url:s.url,onUrl:inp('url'),urlErr:s.urlTried&&!/^https:\/\/\S+/.test(s.url),urlErrCls:s.urlTried&&!/^https:\/\/\S+/.test(s.url)?'err':'',fetchUrl:function(){gate('url')},
   onFile:function(e){var f=e.target.files&&e.target.files[0];c.setState({file:f?f.name:'',fileTried:false})},fileErr:s.fileTried&&!s.file,uploadFile:function(){gate('file')},
   entity:s.entity,ssoUrl:s.ssoUrl,cert:s.cert,onEntity:inp('entity'),onSsoUrl:inp('ssoUrl'),onCert:inp('cert'),
   entErr:s.manTried&&!s.entity.trim(),entErrCls:s.manTried&&!s.entity.trim()?'err':'',ssoErr:s.manTried&&!s.ssoUrl.trim(),ssoErrCls:s.manTried&&!s.ssoUrl.trim()?'err':'',
   certErr:s.manTried&&!s.idp&&!s.cert.trim(),certErrCls:s.manTried&&!s.idp&&!s.cert.trim()?'err':'',saveManual:function(){gate('manual')},entityWarn:entityChanged,
   fetchedAgo:s.fetched,refresh:H.toast(c,'Refreshed the identity provider metadata.'),savedEntity:SAVED.entity,savedSsoUrl:SAVED.sso,
   idpCerts:[{subject:'CN=idp.example.test',fp:'SHA-256 3F:9A:C1:07:EE:52:B8:4D (mock)',exp:'Expires 2027-03-14',cls:'ok'},{subject:'CN=idp.example.test (previous)',fp:'SHA-256 A2:5B:90:1E:7C:D3:46:F8 (mock)',exp:'Expires 2026-12-01',cls:'warn'}],
   spBad:s.keyBad,spFp:'SHA-256 C4:18:7B:2A:90:EF:63:D1 (mock)',askGen:H.set(c,{dlg:'gen'}),genTip:idp?'':'Save the identity provider first.',
   optLabel:s.optLabel,onOptLabel:inp('optLabel'),optEmail:s.optEmail,onOptEmail:inp('optEmail'),optDisplay:s.optDisplay,onOptDisplay:inp('optDisplay'),optUser:s.optUser,onOptUser:inp('optUser'),optSkew:s.optSkew,onOptSkew:inp('optSkew'),
   nameIds:NAMEIDS.map(function(o){return {v:o[0],label:o[1],sel:o[0]===s.nameId}}),onNameId:inp('nameId'),skewErr:skewBad,skewErrCls:skewBad?'err':'',
   saveOpts:function(){var st=c.state;if(!(/^\d+$/.test(st.optSkew)&&Number(st.optSkew)<=300)){c.setState({skewTried:true});return}c.setState({opts:true,skewTried:false});H.toast(c,'Saved.')()},
   runTest:function(){c.setState({testRes:'loading'});clearTimeout(c.__tr);c.__tr=setTimeout(function(){c.setState({testRes:'pass',tested:true})},900)},
   resNone:s.testRes==='',resLoading:s.testRes==='loading',resPass:s.testRes==='pass',resFail:s.testRes==='fail',outcomeL:outcomes[s.outcome],
   copyNameId:H.toast(c,'Copied the NameID.'),dismissRes:H.set(c,{testRes:''}),
   attrs:[{k:'email',v:'dana@example.test'},{k:'displayName',v:'Dana Example'},{k:'groups',v:'platform, oncall'}],
   on:s.on,canShowOn:!s.on,onDisabled:onDisabled,onTip:onTip,warnings:warnings,askOn:H.set(c,{dlg:'on'}),askOff:H.set(c,{dlg:'off'}),
   hasDlg:!!s.dlg,cf:cf,closeDlg:H.set(c,{dlg:''})
  };
 }};
})();
