var Screen={state:{who:'user'},vals:function(c,s){
 var isAdmin=s.who==='admin';
 var user=D.users.filter(function(u){return u.name==='dana'})[0];
 var has=function(tag,letter){
  if(isAdmin)return true;
  return (user.perms[tag]||'').indexOf(letter)>=0;
 };
 var rows=D.tags.map(function(t){
  var name=t.name;
  var view=has(name,'V');
  var yes=function(letter){return has(name,letter)&&view};
  var no=function(letter){return !yes(letter)};
  var services=view?D.services.filter(function(x){return x.tag===name}).length:0;
  return {
   tag:name,tagCls:'tag tag-'+name,
   view:view,noView:!view,
   logs:yes('L'),noLogs:no('L'),
   operate:yes('O'),noOperate:no('O'),
   deploy:yes('D'),noDeploy:no('D'),
   edit:yes('E'),noEdit:no('E'),
   terminal:yes('T'),noTerminal:no('T'),
   services:services
  };
 });
 return {
  sub:isAdmin?'You are a global admin: you can do everything, on every tag.':'What you can do on each tag. Ask a global admin to change it.',
  who:isAdmin?'Global admin':'Non-admin: dana',
  modes:H.seg(c,'who',[['user','Non-admin (dana)'],['admin','Global admin']]),
  rows:rows
 };
}};
