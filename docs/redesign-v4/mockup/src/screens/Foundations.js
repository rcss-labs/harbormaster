var Screen={state:{seg:'all',tog:true},vals:function(c,s){
 var sw=function(n,d,bd){return {n:'--'+n,d:d,s:'height:36px;border-radius:4px;border:1px solid var(--border-strong);background:var(--'+n+')'}};
 return {spec:H.toast(c,'Component specimen: no action in this reference.'),toggleTheme:function(){var t=s.__theme==='dark'?'light':'dark';H.pref('theme',t);c.setState({__theme:t})},
 surf:[sw('bg','Page'),sw('chrome','Sidebar, top bar'),sw('surface','Panels'),sw('raised','Controls'),sw('hover','Row hover'),sw('border','Dividers'),sw('text','Primary text'),sw('text-2','Secondary text'),sw('muted','Labels, at least 4.5:1'),sw('accent','Links, active, primary')],
 stat:[['ok','Steady, healthy','Filled circle'],['warn','Warning, unhealthy','Triangle'],['bad','Failing, needs attention','Diamond'],['info','Deploying, in progress','Half circle'],['exp','Expected stop: Spot, deploy, scale-in','Ring'],['mute','Stopped, unknown','Dash']].map(function(x){return {k:x[0],l:x[1],d:x[2]}}),
 ser:['s1','s2','s3','s4','s5','s6'].map(function(n){return {n:'--'+n,s:'width:18px;height:10px;border-radius:2px;background:var(--'+n+')'}}),
 demo:H.chart({series:[{name:'CPU',color:'s1',values:H.series(3,48,40,5),area:true},{name:'Memory',color:'s2',values:H.series(7,48,60,3)}],unit:'%',max:100,x:['24 h ago','12 h','now'],marks:[{at:31,label:':42'}],w:560,h:150}),
 seg:H.seg(c,'seg',[['all','All',15],['bad','Failing',1],['warn','Warning',2]]),tog:!!s.tog,flip:H.toggle(c,'tog')};
}};
