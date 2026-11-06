var Screen={state:{q:'',sel:'vpc-0a41c7e29b3d',all:false,exp:{}},vals:function(c,s){
 /* Screen-local network detail per VPC id. Service tuple: [name, tasks here, unhealthy]. */
 var N={
  'vpc-0a41c7e29b3d':{nat:['nat-0c41d8e2a7b3f190e (us-east-1a)','nat-05e7b2a9c4d18f362 (us-east-1b)'],natMode:'one per zone',igw:'igw-0b93a1c7d5e2f4086',vpce:['vpce-0a8e3f21c9d7b4561 com.amazonaws.us-east-1.s3','vpce-04d29b7e1a3c8f650 com.amazonaws.us-east-1.ecr.dkr'],standalone:{'private-1a':'db-migrate · background-jobs · 1 task'},
   subnets:[
    {n:'private-1a',id:'subnet-03f1a9c2',type:'Private',z:'us-east-1a',cidr:'10.20.1.0/24',used:232,total:251,extra:1,cl:[['core-services',[['payments-api',2,0],['web-frontend',1,0],['auth-service',1,0],['notifications',2,0]]],['background-jobs',[['report-worker',1,0]]]]},
    {n:'private-1b',id:'subnet-0b77e410',type:'Private',z:'us-east-1b',cidr:'10.20.2.0/24',used:118,total:251,cl:[['core-services',[['payments-api',1,0],['web-frontend',1,0],['auth-service',1,0],['notifications',1,0]]],['background-jobs',[['email-sender',1,0]]]]},
    {n:'public-1a',id:'subnet-09c2d5ee',type:'Public',z:'us-east-1a',cidr:'10.20.101.0/24',used:9,total:251,cl:[]}]},
  'vpc-07de19a4c011':{nat:['nat-0f2a6c91d3b8e7054 (us-east-1a)'],natMode:'single zone',igw:'igw-03c8e5a1f7b2d9460',vpce:['vpce-09b1d4f6e2a7c3850 com.amazonaws.us-east-1.s3'],note:'1 task without a subnet not shown',noSubnet:'release-build · ci-tools · 1 task',
   subnets:[
    {n:'tools-1a',id:'subnet-0c5e7a31',type:'Private',z:'us-east-1a',cidr:'10.21.1.0/24',used:71,total:251,cl:[['ci-tools',[['jenkins-controller',1,0],['artifact-proxy',1,0]]]]},
    {n:'tools-1b',id:'subnet-0d2f8b64',type:'Private',z:'us-east-1b',cidr:'10.21.2.0/24',used:12,total:251,cl:[]}]},
  'vpc-0f3392bb71e2':{nat:['nat-0a7c3e92b1d6f4850 (eu-west-1a)','nat-0e19d5b8a3c7f2046 (eu-west-1b)'],natMode:'one per zone',igw:'igw-0d6a2f8c1e4b97350',vpce:['vpce-0c3f7a1e9b5d28464 com.amazonaws.eu-west-1.s3'],
   subnets:[
    {n:'private-1a',id:'subnet-0a9d4c58',type:'Private',z:'eu-west-1a',cidr:'10.30.1.0/24',used:64,total:251,cl:[['eu-apps',[['catalog-api',1,1],['search-indexer',1,0]]]]},
    {n:'private-1b',id:'subnet-0e61b2f7',type:'Private',z:'eu-west-1b',cidr:'10.30.2.0/24',used:48,total:251,cl:[['eu-apps',[['catalog-api',1,0]]]]},
    {n:'public-1a',id:'subnet-0b45e8d1',type:'Public',z:'eu-west-1a',cidr:'10.30.101.0/24',used:6,total:251,cl:[]}]},
  'vpc-03b1d7f0c5a8':{nat:[],natMode:'none',igw:'igw-0e8b4c2a6d1f93705',vpce:[],
   subnets:[{n:'public-1a',id:'subnet-04b8e1d9',type:'Public',z:'eu-central-1a',cidr:'10.40.1.0/24',used:22,total:251,cl:[['edge-proxy',[['edge-nginx',2,0],['cert-renewer',1,0]]]]}]},
  'vpc-0b18e6cd2a91':{nat:['nat-06d3a8f1c2e9b7450 (us-east-1a)'],natMode:'single zone',igw:'igw-07a5c1e9d3b8f2640',vpce:['vpce-0f7d2b9a4c1e83650 com.amazonaws.us-east-1.s3'],
   subnets:[
    {n:'private-1a',id:'subnet-0f37c2a8',type:'Private',z:'us-east-1a',cidr:'10.50.1.0/24',used:31,total:251,cl:[['staging-web',[['web',1,0],['api',1,0]]]]},
    {n:'public-1a',id:'subnet-08a1d6b3',type:'Public',z:'us-east-1a',cidr:'10.50.101.0/24',used:7,total:251,cl:[]}]}
 };
 var sumT=function(sn){return sn.cl.reduce(function(a,k){return a+k[1].reduce(function(b,x){return b+x[1]},0)},0)+(sn.extra||0)};
 var vpcs=D.vpcs.map(function(v){var d=N[v.id],tasks=d.subnets.reduce(function(a,x){return a+sumT(x)},0)+(d.noSubnet?1:0),cls={};d.subnets.forEach(function(x){x.cl.forEach(function(k){cls[k[0]]=1})});
  var bad=Object.keys(cls).some(function(n){return D.cluster(n).status==='bad'}),warn=Object.keys(cls).some(function(n){return D.cluster(n).status==='warn'});
  return Object.assign({},v,{d:d,tasks:tasks,nCl:Object.keys(cls).length,status:bad?'bad':warn?'warn':'ok',statusL:bad?'Has failing services':warn?'Has degraded services':'Healthy'})});
 var q=s.q.toLowerCase(),shown=vpcs.filter(function(v){return !q||[v.name,v.id,v.acct,v.region,v.cidr].join(' ').toLowerCase().indexOf(q)>=0});
 var sel=vpcs.filter(function(v){return v.id===s.sel})[0]||shown[0]||null;
 if(sel&&shown.indexOf(sel)<0&&q)sel=shown[0]||null;
 var accts=[];shown.forEach(function(v){if(accts.indexOf(v.acct)<0)accts.push(v.acct)});
 var groups=accts.map(function(a){var items=shown.filter(function(v){return v.acct===a});return {name:a,count:items.length,vpcs:items.map(function(v){var on=sel&&v.id===sel.id;return {name:v.name,region:v.region,cidr:v.cidr,counts:v.nCl+' cl · '+v.tasks+' t',status:v.status,statusL:v.statusL,pressed:on,
   sty:'display:flex;align-items:center;gap:10px;width:100%;text-align:left;padding:6px 8px;border-radius:var(--r);cursor:pointer;font:inherit;color:var(--text);border:1px solid '+(on?'var(--accent-line)':'transparent')+';background:'+(on?'var(--accent-soft)':'transparent'),
   pick:function(){c.setState({sel:v.id})}}})}});
 var tot=vpcs.reduce(function(a,v){return a+v.tasks},0),nSub=vpcs.reduce(function(a,v){return a+v.d.subnets.length},0);
 var out={summary:vpcs.length+' VPCs · '+accts.length+' accounts · '+nSub+' subnets · '+tot+' running tasks',q:s.q,onQ:function(e){c.setState({q:e.target.value})},clearQ:H.set(c,{q:''}),groups:groups,noMatch:!shown.length,hiddenNote:'1 VPC without tasks hidden',hasSel:!!sel,noSel:!sel,
  sel:{},kpis:[],subnets:[],tree:[],gw:{igw:'',nat:'',vpce:''},allLabel:s.all?'Collapse to clusters':'Expand all',toggleAll:function(){c.setState({all:!s.all,exp:{}})}};
 if(!sel)return out;
 var d=sel.d,zones=[],usedSum=0,totSum=0;
 var subRows=d.subnets.map(function(x){var t=sumT(x),pct=Math.round(x.used/x.total*100);usedSum+=x.used;totSum+=x.total;
  return {name:x.n,id:x.id,type:x.type,zone:x.z,cidr:x.cidr,usedL:x.used+' of '+x.total+' ('+pct+'%)',bar:H.meterStyle(pct),cls:pct>90?'bad':pct>80?'warn':'',stripe:pct>80?'stripe-warn':'',clusters:x.cl.length,tasks:t,sub:x}});
 var zoneNames=[];d.subnets.forEach(function(x){if(zoneNames.indexOf(x.z)<0)zoneNames.push(x.z)});
 var pub=d.subnets.filter(function(x){return x.type==='Public'}).length;
 out.sel={name:sel.name,id:sel.id,cidr:sel.cidr,acct:sel.acct,region:sel.region,hasNote:!!d.note,note:d.note||''};
 var runZones=zoneNames.filter(function(z){return d.subnets.some(function(x){return x.z===z&&sumT(x)>0})}).length;
 out.kpis=[{k:'Subnets',v:d.subnets.length,d:(d.subnets.length-pub)+' private · '+pub+' public'},{k:'Clusters',v:sel.nCl,d:'with tasks here'},{k:'Running tasks',v:sel.tasks,d:'in '+runZones+(runZones===1?' zone':' zones')},{k:'IPs used',v:usedSum,d:'of '+totSum},{k:'NAT gateways',v:d.nat.length,d:d.natMode}];
 out.subnets=subRows;
 out.gw={igw:d.igw,nat:d.nat.length?d.nat.join(', '):'none',vpce:d.vpce.length?d.vpce.join(', '):'none'};
 var base='display:flex;flex-wrap:wrap;align-items:center;gap:4px 10px;min-height:34px;border-bottom:1px solid var(--row-border);padding:4px 12px 4px ';
 var T=[];
 zoneNames.forEach(function(z){var subs=d.subnets.filter(function(x){return x.z===z}),zt=subs.reduce(function(a,x){return a+sumT(x)},0);
  T.push({isZone:true,level:1,sty:base+'12px;background:var(--raised)',label:z,meta:subs.length+(subs.length===1?' subnet':' subnets')+' · '+zt+(zt===1?' task':' tasks')});
  subs.forEach(function(x){T.push({isSubnet:true,level:2,sty:base+'34px',label:x.n,cidr:x.cidr,meta:x.used+' of '+x.total+' IPs'+(sumT(x)?'':' · no tasks')});
   x.cl.forEach(function(k){var key=sel.id+'|'+x.n+'|'+k[0],open=s.all||!!s.exp[key],cl=D.cluster(k[0]),n=k[1].reduce(function(a,y){return a+y[1]},0);
    T.push({isCluster:true,level:3,sty:base+'56px',label:k[0],status:cl.status,statusL:cl.statusL,meta:k[1].length+(k[1].length===1?' service':' services')+' · '+n+' tasks here',expanded:open,toggleLabel:(open?'Collapse ':'Expand ')+k[0],
     toggle:function(){var e=Object.assign({},s.exp);if(s.all){c.setState({all:false,exp:(function(){var o={};d.subnets.forEach(function(sx){sx.cl.forEach(function(kk){o[sel.id+'|'+sx.n+'|'+kk[0]]=true})});delete o[key];return o})()})}else{e[key]=!e[key];c.setState({exp:e})}}});
    if(open)k[1].forEach(function(y){var sv=D.svc(y[0]);T.push({isService:true,level:4,sty:base+'96px',label:y[0],status:sv.status,statusL:sv.statusL,meta:y[1]+(y[1]===1?' task':' tasks')+' here · :'+sv.rev+' · '+y[2]+' unhealthy'})})});
   if(d.standalone&&d.standalone[x.n])T.push({isNote:true,level:3,sty:base+'56px',label:'Standalone tasks',meta:d.standalone[x.n]})})});
 if(d.noSubnet)T.push({isNote:true,level:1,sty:base+'12px',label:'No subnet',meta:d.noSubnet});
 out.tree=T;
 return out;
}};
