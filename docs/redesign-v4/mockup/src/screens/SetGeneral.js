var Screen={state:{view:'admin',minutes:'60',saved:'60',tried:false},vals:function(c,s){
 var n=Number(s.minutes),valid=s.minutes!==''&&/^\d+$/.test(String(s.minutes))&&n>=1&&n<=1440;
 var human=!valid?'':(n<60?n+' min':n%60===0?(n/60)+' h':Math.floor(n/60)+' h '+(n%60)+' min');
 var hasErr=!valid&&(s.tried||s.minutes!=='');
 return {viewSeg:H.seg(c,'view',[['admin','Global admin'],['user','Non-admin']]),isAdmin:s.view==='admin',noAccess:s.view!=='admin',
  minutes:s.minutes,saved:s.saved,human:human,hasErr:hasErr,errCls:hasErr?'err':'',dirty:valid&&s.minutes!==s.saved,clean:!(valid&&s.minutes!==s.saved),
  onMinutes:function(e){c.setState({minutes:e.target.value,tried:false})},
  save:function(){var v=String(c.state.minutes),k=Number(v);if(v===''||!/^\d+$/.test(v)||k<1||k>1440){c.setState({tried:true});return}c.setState({saved:v,tried:false});H.toast(c,'Saved.')()}};
}};
