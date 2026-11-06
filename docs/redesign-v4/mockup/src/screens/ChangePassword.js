var Screen={state:{variant:'normal',current:'',newPassword:'',confirm:'',currentErr:false,confirmErr:false},vals:function(c,s){
 var expectedCurrent='demo-current';
 var forced=s.variant==='forced';
 var field=function(key){return function(e){var patch={};patch[key]=e.target.value;c.setState(patch)}};
 var submit=function(e){
  var currentOk=c.state.current===expectedCurrent;
  var matchOk=c.state.newPassword===c.state.confirm;
  if(currentOk&&matchOk){c.setState({currentErr:false,confirmErr:false});return;}
  if(e&&e.preventDefault)e.preventDefault();
  c.setState({currentErr:!currentOk,confirmErr:!matchOk});
 };
 return {
  variants:H.seg(c,'variant',[['normal','Normal'],['forced','Forced']]),
  sub:forced?'Your password is temporary. Set a new one to continue.':'Enter your current password and a new one.',
  current:s.current,newPassword:s.newPassword,confirm:s.confirm,
  currentErrOn:s.currentErr,confirmErrOn:s.confirmErr,
  currentCls:s.currentErr?'err':'',confirmCls:s.confirmErr?'err':'',
  setCurrent:field('current'),setNewPassword:field('newPassword'),setConfirm:field('confirm'),
  submit:submit
 };
}};
