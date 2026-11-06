var Screen={state:{mode:'sso',pwOpen:false,pwErr:false,user:'',pw:''},vals:function(c,s){
 var showSso=s.mode!=='password';
 var showPwForm=!showSso||s.pwOpen;
 var submitPw=function(e){
  if(c.state.user&&c.state.pw){c.setState({pwErr:false});return;}
  if(e&&e.preventDefault)e.preventDefault();
  c.setState({pwErr:true});
 };
 return {
  modes:H.seg(c,'mode',[['password','Password'],['sso','SSO'],['signedout','Signed out'],['failed','Failed sign-on']]),
  showSso:showSso,showPwForm:showPwForm,showBreakGlass:showSso,pwOpen:s.pwOpen,
  infoOn:s.mode==='signedout',failedOn:s.mode==='failed',
  togglePw:H.toggle(c,'pwOpen'),
  pwErrOn:s.pwErr,userCls:s.pwErr&&!s.user?'err':'',pwCls:s.pwErr&&!s.pw?'err':'',
  user:s.user,pw:s.pw,
  setUser:function(e){c.setState({user:e.target.value})},
  setPw:function(e){c.setState({pw:e.target.value})},
  submitPw:submitPw
 };
}};
