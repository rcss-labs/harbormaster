/* Helpers available to every screen as H. Deterministic so screens always render the same mock. */
var H={
 pref:function(k,v){try{if(v===undefined)return localStorage.getItem('hm.'+k);localStorage.setItem('hm.'+k,v)}catch(e){}return null},
 /* seeded series: H.series(seed,count,base,swing) */
 series:function(seed,n,base,swing){var a=[],x=(seed*7919+13)%233280,v=base;for(var i=0;i<n;i++){x=(x*9301+49297)%233280;var r=x/233280;v+=(r-.5)*swing*1.6+(base-v)*.18;a.push(Math.max(0,Math.round(v*10)/10))}return a},
 /* sparkline path in a 0..100 x 0..24 box */
 spark:function(v){var mx=Math.max.apply(null,v),mn=Math.min.apply(null,v),r=(mx-mn)||1,p=v.map(function(y,i){return (i?'L':'M')+(i*100/(v.length-1)).toFixed(1)+' '+(22-(y-mn)/r*20).toFixed(1)}).join(' ');return {line:p,area:p+' L100 24 L0 24 Z'}},
 fmt:function(n){return n>=1000?(n/1000).toFixed(1)+'k':String(Math.round(n*10)/10)},
 /* line/area chart. H.chart({series:[{name,color:'s1',values,area:true}],max,unit,x:['24 h ago','12 h','now'],marks:[{at:index,label:':42'}],w,h}) */
 chart:function(o){var w=o.w||720,h=o.h||200,L=44,R=8,T=8,B=20,iw=w-L-R,ih=h-T-B,n=o.series[0].values.length,all=[].concat.apply([],o.series.map(function(s){return s.values})),mx=o.max||Math.ceil(Math.max.apply(null,all)*1.15)||1,u=o.unit||'';
  var X=function(i){return L+i*iw/(n-1)},Y=function(v){return T+ih-(v/mx)*ih};
  var series=o.series.map(function(s){var d=s.values.map(function(v,i){return (i?'L':'M')+X(i).toFixed(1)+' '+Y(v).toFixed(1)}).join(' ');return {name:s.name,color:'var(--'+s.color+')',line:d,area:s.area?d+' L'+X(n-1).toFixed(1)+' '+Y(0)+' L'+L+' '+Y(0)+' Z':'',last:s.values[n-1],lastL:H.fmt(s.values[n-1])+u,dash:s.dash?'4 3':'none',ex:X(n-1),ey:Y(s.values[n-1])}});
  var grid=[0,.5,1].map(function(f){return {y:Y(mx*f),label:H.fmt(mx*f)+u,tx:L-6,ty:Y(mx*f)+4,x1:L,x2:w-R}});
  var xs=(o.x||[]).map(function(t,i,arr){var px=L+i*iw/(arr.length-1);return {x:px,y:h-5,label:t,anchor:i===0?'start':i===arr.length-1?'end':'middle'}});
  var step=Math.max(1,Math.floor(n/48)),hits=[];for(var i=0;i<n;i+=step){hits.push({x:X(i)-(iw/(n-1))*step/2,y:T,w:Math.max(1,iw/(n-1)*step),h:ih,tip:(o.tip?o.tip(i)+': ':'')+o.series.map(function(s){return s.name+' '+H.fmt(s.values[i])+u}).join(' · ')})}
  var marks=(o.marks||[]).map(function(m){return {x:X(m.at),y1:T,y2:T+ih,label:m.label,ty:T+9,tx:X(m.at)-4}});
  var pc=function(v,t){return (v/t*100).toFixed(2)+'%'};
  series.forEach(function(x){x.dot='left:'+pc(x.ex,w)+';top:'+pc(x.ey,h)+';background:'+x.color});
  var labels=grid.map(function(g){return {t:g.label,s:'left:'+pc(g.tx,w)+';top:'+pc(g.y,h)+';transform:translate(-100%,-50%)'}}).concat(xs.map(function(x){return {t:x.label,s:'left:'+pc(x.x,w)+';bottom:0;transform:translateX('+(x.anchor==='start'?'0':x.anchor==='end'?'-100%':'-50%')+')'}})).concat(marks.map(function(m){return {t:m.label,s:'left:'+pc(m.x,w)+';top:0;transform:translateX(-110%);color:var(--text-2)'}}));
  hits.forEach(function(k){k.s='left:'+pc(k.x,w)+';top:'+pc(k.y,h)+';width:'+pc(k.w,w)+';height:'+pc(k.h,h)});
  return {box:'aspect-ratio:'+w+'/'+h,labels:labels,vb:'0 0 '+w+' '+h,grid:grid,series:series,x:xs,hits:hits,marks:marks,legend:o.series.map(function(s){return {name:s.name,style:'background:var(--'+s.color+')'}})}},
 /* stacked bars. H.bars({keys:[{name,color}],rows:[[a,b],...],x:[...],w,h,unit,tip}) */
 bars:function(o){var w=o.w||720,h=o.h||180,L=36,R=4,T=6,B=20,iw=w-L-R,ih=h-T-B,n=o.rows.length,tot=o.rows.map(function(r){return r.reduce(function(a,b){return a+b},0)}),mx=o.max||Math.ceil(Math.max.apply(null,tot)*1.1)||1,bw=iw/n,rects=[];
  o.rows.forEach(function(r,i){var y=T+ih;r.forEach(function(v,k){if(!v)return;var bh=Math.max(1.5,v/mx*ih);y-=bh;rects.push({x:L+i*bw+1,y:y+(k?1:0),w:Math.max(1,bw-2),h:Math.max(1,bh-(k?1:0)),fill:'var(--'+o.keys[k].color+')',tip:(o.tip?o.tip(i):'')+' '+o.keys[k].name+': '+v})})});
  var pc=function(v,t){return (v/t*100).toFixed(2)+'%'};rects.forEach(function(r){r.hs='left:'+pc(r.x,w)+';top:'+pc(r.y,h)+';width:'+pc(r.w,w)+';height:'+pc(r.h,h)});
  var grid=[0,.5,1].map(function(f){return {y:T+ih-f*ih,label:H.fmt(mx*f)+(o.unit||''),tx:L-6,x1:L,x2:w-R}});var xs=(o.x||[]).map(function(t,i,arr){return {x:L+i*iw/(arr.length-1),label:t,anchor:i===0?'start':i===arr.length-1?'end':'middle'}});
  var labels=grid.map(function(g){return {t:g.label,s:'left:'+pc(g.tx,w)+';top:'+pc(g.y,h)+';transform:translate(-100%,-50%)'}}).concat(xs.map(function(x){return {t:x.label,s:'left:'+pc(x.x,w)+';bottom:0;transform:translateX('+(x.anchor==='start'?'0':x.anchor==='end'?'-100%':'-50%')+')'}}));
  return {box:'aspect-ratio:'+w+'/'+h,labels:labels,vb:'0 0 '+w+' '+h,rects:rects,grid:grid,legend:o.keys.map(function(k){return {name:k.name,style:'background:var(--'+k.color+')'}})}},
 meterStyle:function(p){return 'width:'+Math.min(100,Math.max(0,p))+'%'},
 /* segmented control: H.seg(c,'stateKey',[['all','All',12],['bad','Failing',1]]) gives [{id,label,n,hasN,pressed,pick}] */
 seg:function(c,key,opts){return opts.map(function(o){return {id:o[0],label:o[1],n:o[2],hasN:o[2]!==undefined,pressed:c.state[key]===o[0],pick:function(){var p={};p[key]=o[0];c.setState(p)}}})},
 toggle:function(c,key){return function(){var p={};p[key]=!c.state[key];c.setState(p)}},
 set:function(c,patch){return function(){c.setState(patch)}},
 toast:function(c,msg){return function(){clearTimeout(c.__tt);c.setState({__toast:msg});c.__tt=setTimeout(function(){c.setState({__toast:null})},3500)}},
 stClass:function(s){return 'st st-'+s},stripe:function(s){return (s==='bad'||s==='warn'||s==='info'||s==='exp')?'stripe-'+s:''},
 tagClass:function(t,inh){return 'tag tag-'+t+(inh?' inh':'')},
 svcHref:function(){return 'SvcOverview.dc.html'}
};
