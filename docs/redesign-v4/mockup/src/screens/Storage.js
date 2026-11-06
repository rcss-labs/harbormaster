var Screen={state:{seg:'all',q:''},vals:function(c,s){
 var all=D.efs,q=s.q.toLowerCase(),rows=all.filter(function(f){return (s.seg==='all'||(s.seg==='on')===(f.mounted.length>0))&&(!q||JSON.stringify(f).toLowerCase().indexOf(q)>=0)}).map(function(f){return Object.assign({},f,{stripe:f.mounted.length?'':'stripe-warn',isMounted:f.mounted.length>0,notMounted:!f.mounted.length})});
 var un=all.filter(function(f){return !f.mounted.length});
 return {total:all.length,stored:Math.round(all.reduce(function(a,f){return a+f.gb},0)*10)/10,mountedN:all.length-un.length,unmountedN:un.length,unmountedGb:un.reduce(function(a,f){return a+f.gb},0),
  seg:H.seg(c,'seg',[['all','All',all.length],['on','Mounted',all.length-un.length],['off','Not mounted',un.length]]),rows:rows,empty:!rows.length,q:s.q,onQ:function(e){c.setState({q:e.target.value})},clear:H.set(c,{seg:'all',q:''}),
  trend:H.chart({series:[{name:'Stored, GB',color:'s1',values:H.series(5,30,330,3),area:true}],x:['30 d ago','15 d','now'],w:1100,h:170,unit:' GB'})};
}};
