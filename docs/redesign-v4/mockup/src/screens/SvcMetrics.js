var Screen={state:{range:'24h',view:'ok'},vals:function(c,s){
 var R={'1h':{n:12,x:['60 min ago','45 min','30 min','15 min','now']},'6h':{n:72,x:['6 h ago','4 h','2 h','now']},'24h':{n:288,x:['24 h ago','18 h','12 h','6 h','now']},'72h':{n:864,x:['72 h ago','48 h','24 h','now']}}[s.range];
 var k={'1h':1,'6h':2,'24h':3,'72h':4}[s.range],n=R.n;
 var ch=function(name,seed,base,sw,o){return H.chart(Object.assign({series:[{name:name,color:'s1',values:H.series(seed*k,n,base,sw),area:true}],x:R.x,w:560,h:190},o||{}))};
 var tcount=Array.apply(null,Array(n)).map(function(_,i){return i>n*0.62&&i<n*0.66?4:3});
 var ok=s.view==='ok';
 return {
  rangeSeg:H.seg(c,'range',[['1h','1h'],['6h','6h'],['24h','24h'],['72h','72h']]),
  stateSeg:H.seg(c,'view',[['ok','Normal'],['off','Insights off'],['err','Error'],['empty','No data yet']]),
  insightsOff:s.view==='off',hasError:s.view==='err',retry:H.set(c,{view:'ok'}),
  showGrid:s.view==='ok'||s.view==='empty',showCharts:ok,noData:s.view==='empty',stale:'Updated 40 s ago',
  nowCpu:'now 44% of 1024 units',nowMem:'now 61% of 2048 MiB',nowRpm:'now 1.2k',nowP95:'now 184 ms',nowNet:'now 412 KB in, 186 KB out',nowCnt:'now 3, desired 3',
  cpu:ch('CPU',2,44,6,{unit:'%',max:100}),mem:ch('Memory',3,61,3,{unit:'%',max:100}),
  rpm:ch('Requests',4,1200,160),p95:ch('p95',5,184,18,{unit:' ms'}),
  net:H.chart({series:[{name:'Received',color:'s1',values:H.series(6*k,n,412,60)},{name:'Sent',color:'s2',values:H.series(7*k,n,186,30)}],x:R.x,unit:' KB/s',w:560,h:190}),
  cnt:H.chart({series:[{name:'Running',color:'s1',values:tcount}],x:R.x,max:5,w:560,h:190})
 };
}};
