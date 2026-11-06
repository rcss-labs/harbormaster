var Screen=(function(){
 /* [type, when, absolute time, actor, summary] newest first */
 var E=[
  ['user','2 min ago','2026-11-06 11:40','pavel','Set a temporary password for oncall.'],
  ['permission','9 min ago','2026-11-06 11:33','pavel','Gave oncall Terminal on Prod.'],
  ['sso','31 min ago','2026-11-06 11:11','pavel','Saved the single sign-on identity provider settings.'],
  ['user','37 min ago','2026-11-06 11:05','dana','Signed in with SSO.'],
  ['tag','1 h ago','2026-11-06 10:42','pavel','Tagged cluster staging-web as Staging (was Dev).'],
  ['cluster','1 h ago','2026-11-06 10:41','pavel','Tagged service artifact-proxy as Prod (was inheriting Dev).'],
  ['user','2 h ago','2026-11-06 09:50','omer','Signed in with SSO.'],
  ['permission','3 h ago','2026-11-06 08:57','pavel','Gave dana Deployer on Staging.'],
  ['account','5 h ago','2026-11-06 06:30','pavel','Edited AWS account homelab-admin: regions us-east-1, eu-west-1, eu-central-1.'],
  ['user','6 h ago','2026-11-06 05:12','pavel','Made pavel break-glass.'],
  ['settings','yesterday','2026-11-05 16:20','pavel','Set stopped tasks window to 60 minutes.'],
  ['user','yesterday','2026-11-05 15:02','pavel','Created user omer with Operator on Dev.'],
  ['permission','yesterday','2026-11-05 15:03','pavel','Gave omer Viewer on Staging.'],
  ['tag','2 d ago','2026-11-04 10:15','pavel','Created tag Prod.'],
  ['tag','2 d ago','2026-11-04 10:14','pavel','Created tag Staging.'],
  ['account','2 d ago','2026-11-04 09:40','pavel','Connected AWS account staging-example.'],
  ['user','3 d ago','2026-11-03 14:08','pavel','Created user dana with Full on Dev.'],
  ['cluster','3 d ago','2026-11-03 14:20','pavel','Tagged cluster core-services as Prod (was Dev).'],
  ['permission','3 d ago','2026-11-03 14:09','pavel','Gave dana View and Logs on Prod.'],
  ['user','4 d ago','2026-11-02 08:45','pavel','Removed global admin from sam.'],
  ['user','5 d ago','2026-11-01 17:30','pavel','Deleted user sam.'],
  ['sso','5 d ago','2026-11-01 17:00','pavel','Generated a new service provider certificate.'],
  ['account','6 d ago','2026-10-31 12:00','pavel','Connected AWS account homelab-admin.'],
  ['user','6 d ago','2026-10-31 11:55','pavel','Created user pavel as global admin.']
 ];
 var TYPES=['user','permission','tag','cluster','account','sso','settings'];
 return {state:{view:'admin',type:'',q:'',shown:12},
 vals:function(c,s){
  var q=s.q.trim().toLowerCase();
  var m=E.filter(function(e){return (!s.type||e[0]===s.type)&&(!q||(e[3]+' '+e[4]+' '+e[0]).toLowerCase().indexOf(q)>=0)});
  var vis=m.slice(0,s.shown);
  var counts={};E.forEach(function(e){counts[e[0]]=(counts[e[0]]||0)+1});
  return {about:H.toast(c,"Service actions are in each service's operations log."),viewSeg:H.seg(c,'view',[['admin','Global admin'],['user','Non-admin']]),isAdmin:s.view==='admin',noAccess:s.view!=='admin',
   types:[{v:'',label:'All types',sel:!s.type}].concat(TYPES.map(function(t){return {v:t,label:t+' ('+counts[t]+')',sel:s.type===t}})),
   q:s.q,onType:function(e){c.setState({type:e.target.value,shown:12})},onQ:function(e){c.setState({q:e.target.value,shown:12})},
   filtered:!!(s.type||q),clear:H.set(c,{type:'',q:'',shown:12}),
   rows:vis.map(function(e){return {type:e[0],when:e[1],abs:e[2],actor:e[3],text:e[4]}}),hasRows:vis.length>0,empty:!m.length,
   more:m.length>vis.length,loadMore:function(){c.setState({shown:c.state.shown+12})},countL:'Showing '+vis.length+' of '+m.length};
 }};
})();
