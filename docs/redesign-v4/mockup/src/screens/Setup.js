var Screen={state:{token:'',username:'',password:'',confirm:'',tokenErr:false,confirmErr:false},vals:function(c,s){
 var expectedToken='HM-7Q2K-9MZP';
 var field=function(key){return function(e){var patch={};patch[key]=e.target.value;c.setState(patch)}};
 var submit=function(e){
  var tokenOk=c.state.token===expectedToken;
  var matchOk=c.state.password===c.state.confirm;
  if(tokenOk&&matchOk){c.setState({tokenErr:false,confirmErr:false});return;}
  if(e&&e.preventDefault)e.preventDefault();
  c.setState({tokenErr:!tokenOk,confirmErr:!matchOk});
 };
 return {
  token:s.token,username:s.username,password:s.password,confirm:s.confirm,
  tokenErrOn:s.tokenErr,confirmErrOn:s.confirmErr,
  tokenCls:s.tokenErr?'err':'',confirmCls:s.confirmErr?'err':'',
  setToken:field('token'),setUsername:field('username'),setPassword:field('password'),setConfirm:field('confirm'),
  submit:submit
 };
}};
