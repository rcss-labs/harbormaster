/* Shared mock estate. Every screen reads this so numbers agree across artboards. All values are invented. */
var D=(function(){
 var A={home:{name:'homelab-admin',id:'482109375512'},stag:{name:'staging-example',id:'731022849906'}};
 var clusters=[
  {name:'core-services',acct:'homelab-admin',acctId:A.home.id,region:'us-east-1',tag:'Prod',status:'info',statusL:'Deploying',cpu:41,mem:58,vcpu:18.5,gib:37,spot:0,last:'3 min ago',vpc:'homelab-main',insights:true},
  {name:'background-jobs',acct:'homelab-admin',acctId:A.home.id,region:'us-east-1',tag:'Prod',status:'bad',statusL:'Needs attention',cpu:63,mem:71,vcpu:9,gib:18,spot:33,last:'18 min ago',vpc:'homelab-main',insights:true},
  {name:'ci-tools',acct:'homelab-admin',acctId:A.home.id,region:'us-east-1',tag:'Dev',status:'ok',statusL:'Healthy',cpu:22,mem:47,vcpu:6,gib:16,spot:50,last:'2 h ago',vpc:'shared-services',insights:true},
  {name:'eu-apps',acct:'homelab-admin',acctId:A.home.id,region:'eu-west-1',tag:'Prod',status:'warn',statusL:'Degraded',cpu:36,mem:63,vcpu:6,gib:12,spot:0,last:'26 h ago',vpc:'eu-apps',insights:true},
  {name:'edge-proxy',acct:'homelab-admin',acctId:A.home.id,region:'eu-central-1',tag:'Prod',status:'ok',statusL:'Healthy',cpu:null,mem:null,vcpu:2.5,gib:5,spot:0,last:'4 d ago',vpc:'edge',insights:false},
  {name:'staging-web',acct:'staging-example',acctId:A.stag.id,region:'us-east-1',tag:'Staging',status:'ok',statusL:'Healthy',cpu:18,mem:39,vcpu:2,gib:4,spot:100,last:'41 min ago',vpc:'staging',insights:true}
 ];
 function s(name,cluster,tag,status,statusL,desired,running,rev,image,cpu,mem,last,extra){var c=clusters.filter(function(x){return x.name===cluster})[0];return Object.assign({name:name,cluster:cluster,acct:c.acct,region:c.region,tag:tag,inherited:tag===c.tag,status:status,statusL:statusL,desired:desired,running:running,rev:rev,image:image,cpu:cpu,mem:mem,last:last},extra||{})}
 var services=[
  s('image-resizer','background-jobs','Prod','bad','Failing',2,0,6,'0.4.2',null,null,'18 min ago',{why:'OutOfMemoryError: Container killed due to memory usage (exit 137)',age:'18 min ago'}),
  s('catalog-api','eu-apps','Prod','warn','1 unhealthy',2,2,23,'4.1.0',36,63,'26 h ago',{why:'Target health checks fail with HTTP 503',age:'42 min ago',lb:true}),
  s('search-indexer','eu-apps','Prod','warn','Restart loop',1,1,14,'2.0.3',48,77,'3 d ago',{why:'3 tasks stopped with an error in the last 24 h',age:'1 h ago'}),
  s('notifications','core-services','Prod','info','Rolling out',2,3,10,'2.3.0',29,41,'3 min ago',{why:'Rolling out :10, 1 of 2 new tasks running',age:'3 min ago'}),
  s('payments-api','core-services','Prod','ok','Steady',3,3,42,'1.18.2',44,61,'5 h ago',{lb:true,pinned:true}),
  s('web-frontend','core-services','Prod','ok','Steady',2,2,87,'3.4.0',31,52,'1 d ago',{lb:true}),
  s('auth-service','core-services','Prod','ok','Steady',2,2,15,'0.9.7',18,44,'6 d ago',{lb:true}),
  s('report-worker','background-jobs','Prod','ok','Steady',1,1,31,'5.0.1',71,68,'2 d ago'),
  s('email-sender','background-jobs','Prod','ok','Steady',1,1,12,'1.3.3',9,31,'9 d ago'),
  s('jenkins-controller','ci-tools','Dev','ok','Steady',1,1,58,'2.479.1-lts',27,74,'2 h ago',{pinned:true}),
  s('artifact-proxy','ci-tools','Prod','ok','Steady',1,1,7,'1.0.4',12,28,'12 d ago'),
  s('edge-nginx','edge-proxy','Prod','ok','Steady',2,2,19,'1.27.2',null,null,'4 d ago',{lb:true}),
  s('cert-renewer','edge-proxy','Prod','ok','Steady',1,1,4,'0.2.1',null,null,'21 d ago'),
  s('web','staging-web','Staging','ok','Steady',1,1,33,'3.5.0-rc.1',16,37,'41 min ago',{lb:true}),
  s('api','staging-web','Staging','ok','Steady',1,1,21,'1.19.0-rc.2',20,41,'3 h ago',{lb:true})
 ];
 clusters.forEach(function(c){var ss=services.filter(function(x){return x.cluster===c.name});c.services=ss.map(function(x){return x.name});c.desired=ss.reduce(function(a,x){return a+x.desired},0);c.running=ss.reduce(function(a,x){return a+x.running},0)});
 var tasks=[
  {id:'4fd1c2a9e07b',svc:'image-resizer',cluster:'background-jobs',status:'bad',statusL:'Stopped (exit 137)',cause:null,health:'–',rev:6,ip:'10.20.1.44',subnet:'private-1a',az:'us-east-1a',cap:'FARGATE',cpu:null,mem:null,started:'24 min ago',stopped:'18 min ago',reason:'Out of memory (exit 137)',raw:'OutOfMemoryError: Container killed due to memory usage',group:6},
  {id:'7044b8c03dd5',svc:'catalog-api',cluster:'eu-apps',status:'warn',statusL:'Running',cause:null,health:'Unhealthy',rev:23,ip:'10.30.1.17',subnet:'private-1a',az:'eu-west-1a',cap:'FARGATE',cpu:34,mem:56,started:'26 h ago',stopped:null,reason:'Failed load balancer health checks',raw:'Health checks failed with these codes: [503]'},
  {id:'164daa9efe86',svc:'catalog-api',cluster:'eu-apps',status:'ok',statusL:'Running',cause:null,health:'Healthy',rev:23,ip:'10.30.2.52',subnet:'private-1b',az:'eu-west-1b',cap:'FARGATE',cpu:35,mem:62,started:'26 h ago',stopped:null,reason:null},
  {id:'9f2e1a3b77c0',svc:'payments-api',cluster:'core-services',status:'ok',statusL:'Running',cause:null,health:'Healthy',rev:42,ip:'10.20.1.21',subnet:'private-1a',az:'us-east-1a',cap:'FARGATE',cpu:46,mem:60,started:'5 h ago',stopped:null,reason:null},
  {id:'b81c7d40a2f6',svc:'payments-api',cluster:'core-services',status:'ok',statusL:'Running',cause:null,health:'Healthy',rev:42,ip:'10.20.2.38',subnet:'private-1b',az:'us-east-1b',cap:'FARGATE',cpu:42,mem:63,started:'5 h ago',stopped:null,reason:null},
  {id:'e30a95d1c6b4',svc:'payments-api',cluster:'core-services',status:'ok',statusL:'Running',cause:null,health:'Healthy',rev:42,ip:'10.20.1.67',subnet:'private-1a',az:'us-east-1a',cap:'FARGATE',cpu:44,mem:59,started:'5 h ago',stopped:null,reason:null},
  {id:'a5c39e17d0b2',svc:'notifications',cluster:'core-services',status:'info',statusL:'Activating',cause:null,health:'No check',rev:10,ip:'10.20.2.91',subnet:'private-1b',az:'us-east-1b',cap:'FARGATE',cpu:null,mem:null,started:'1 min ago',stopped:null,reason:null},
  {id:'c72f08be1a43',svc:'payments-api',cluster:'core-services',status:'exp',statusL:'Stopped (exit 0)',cause:'deploy',health:'–',rev:41,ip:'10.20.1.19',subnet:'private-1a',az:'us-east-1a',cap:'FARGATE',cpu:null,mem:null,started:'2 d ago',stopped:'5 h ago',reason:'Replaced by a deployment',raw:'Scaling activity initiated by (deployment ecs-svc/8841)'},
  {id:'0dfe41c8b96a',svc:'web',cluster:'staging-web',status:'exp',statusL:'Stopped (exit 143)',cause:'spot',health:'–',rev:33,ip:'10.50.1.12',subnet:'private-1a',az:'us-east-1a',cap:'FARGATE_SPOT',cpu:null,mem:null,started:'6 h ago',stopped:'52 min ago',reason:'Spot interruption',raw:'Your Spot Task was interrupted.'},
  {id:'3ab6e2f7c150',svc:'report-worker',cluster:'background-jobs',status:'ok',statusL:'Running',cause:null,health:'No check',rev:31,ip:'10.20.1.83',subnet:'private-1a',az:'us-east-1a',cap:'FARGATE_SPOT',cpu:71,mem:68,started:'2 d ago',stopped:null,reason:null},
  {id:'d94b1076ea3c',svc:'jenkins-controller',cluster:'ci-tools',status:'ok',statusL:'Running',cause:null,health:'Healthy',rev:58,ip:'10.21.1.10',subnet:'tools-1a',az:'us-east-1a',cap:'FARGATE',cpu:27,mem:74,started:'2 h ago',stopped:null,reason:null},
  {id:'61e0c3a8f2d9',svc:null,standalone:'release-build',cluster:'ci-tools',status:'ok',statusL:'Running',cause:null,health:'No check',rev:3,ip:'10.21.1.44',subnet:'tools-1a',az:'us-east-1a',cap:'FARGATE',cpu:88,mem:52,started:'12 min ago',stopped:null,reason:null}
 ];
 tasks.push({id:'2c9d71e4b08f',svc:null,standalone:'db-migrate',cluster:'background-jobs',status:'ok',statusL:'Running',cause:null,health:'No check',rev:12,ip:'10.20.1.96',subnet:'private-1a',az:'us-east-1a',cap:'FARGATE',cpu:64,mem:38,started:'6 min ago',stopped:null,reason:null});
 /* Fill the list so every service has as many running tasks as its running count. */
 var __hex='0123456789abcdef',__n=1;services.forEach(function(v){var have=tasks.filter(function(t){return t.svc===v.name&&!t.stopped}).length,c=clusters.filter(function(x){return x.name===v.cluster})[0],net={'homelab-main':'10.20','shared-services':'10.21','eu-apps':'10.30','edge':'10.40','staging':'10.50'}[c.vpc];for(var i=have;i<v.running;i++){var id='';for(var k=0;k<12;k++){id+=__hex[(__n*(k+7)*2654435)%16]}__n++;var b=i%2===0;tasks.push({id:id,svc:v.name,cluster:v.cluster,status:v.status==='warn'?'warn':'ok',statusL:'Running',cause:null,health:v.lb?'Healthy':'No check',rev:v.rev,ip:net+'.'+(b?1:2)+'.'+(20+__n),subnet:b?'private-1a':'private-1b',az:c.region+(b?'a':'b'),cap:c.spot>50?'FARGATE_SPOT':'FARGATE',cpu:v.cpu,mem:v.mem,started:v.last,stopped:null,reason:null})}});
 var attention=[
  {sev:'bad',sevL:'Failing',svc:'image-resizer',cluster:'background-jobs',title:'0 of 2 tasks running',why:'OutOfMemoryError: container killed at 1024 MiB. 6 stops in the last hour.',next:'Raise task memory to 2048 MiB',nextTab:'SvcDeploy',age:'18 min ago',related:'Started after :6 was deployed by dana, 19 min earlier'},
  {sev:'warn',sevL:'Warning',svc:'catalog-api',cluster:'eu-apps',title:'1 of 2 targets unhealthy',why:'Target health checks fail with HTTP 503 on task 7044b8c03dd5.',next:'Open logs for the unhealthy task',nextTab:'SvcLogs',age:'42 min ago',related:'No change in the last 26 h'},
  {sev:'warn',sevL:'Warning',svc:'search-indexer',cluster:'eu-apps',title:'Restart loop',why:'3 tasks stopped with an error in the last 24 h.',next:'Review stopped tasks',nextTab:'SvcTasks',age:'1 h ago',related:'Memory has been above 75% for 6 h'},
  {sev:'info',sevL:'In progress',svc:'notifications',cluster:'core-services',title:'Rolling out :10',why:'1 of 2 new tasks running. Circuit breaker with rollback is on.',next:'Watch the rollout',nextTab:'SvcOverview',age:'3 min ago',related:'Deployed by omer'}
 ];
 var activity=[
  {sev:'bad',kind:'problem',subj:'image-resizer',text:'Tasks stopped: OutOfMemoryError',age:'18 min ago',actor:'ECS event · background-jobs',n:6},
  {sev:'warn',kind:'problem',subj:'catalog-api',text:'Port 8080 unhealthy in catalog-api-tg (503)',age:'42 min ago',actor:'ECS event · eu-apps'},
  {sev:'warn',kind:'problem',subj:'search-indexer',text:'Unable to consistently start tasks',age:'1 h ago',actor:'ECS event · eu-apps',n:3},
  {sev:'info',kind:'change',subj:'notifications',text:'Deployed revision :10, image 2.3.0',age:'3 min ago',actor:'omer'},
  {sev:'mute',kind:'change',subj:'payments-api',text:'Set desired count to 3',age:'5 h ago',actor:'dana'},
  {sev:'mute',kind:'change',subj:'image-resizer',text:'Deployed revision :6, image 0.4.2',age:'37 min ago',actor:'dana'},
  {sev:'mute',kind:'change',subj:'jenkins-controller',text:'Opened a terminal session',age:'2 h ago',actor:'oncall'}
 ];
 var dataHealth=[
  {sev:'warn',where:'homelab-admin / eu-central-1',text:'Updates slowed by AWS throttling. Data is 40 s old.',since:'12 min ago'},
  {sev:'bad',where:'staging-example / us-east-1',text:'Service data is 4 min old. Other data is current.',detail:'ecs:DescribeServices returned AccessDenied. Check the harbormaster-access role.',since:'4 min ago'}
 ];
 var users=[
  {name:'pavel',email:'pavel@example.test',admin:true,signin:['Password','Break-glass'],last:'just now',you:true},
  {name:'dana',email:'dana@example.test',admin:false,signin:['SSO linked'],last:'37 min ago',perms:{Dev:'VLODET',Staging:'VLODE',Prod:'VL'}},
  {name:'omer',email:'omer@example.test',admin:false,signin:['SSO linked'],last:'3 min ago',perms:{Dev:'VLO',Staging:'V',Prod:''}},
  {name:'oncall',email:'oncall@example.test',admin:false,signin:['Link pending'],last:'2 h ago',perms:{Dev:'',Staging:'VLOT',Prod:'VLOT'}}
 ];
 var tags=[{name:'Dev',color:'neutral',clusters:1,direct:0,effect:1,def:true},{name:'Staging',color:'amber',clusters:1,direct:0,effect:2},{name:'Prod',color:'red',clusters:4,direct:1,effect:12}];
 var accounts=[
  {name:'homelab-admin',id:A.home.id,role:'arn:aws:iam::482109375512:role/harbormaster-access',regions:['us-east-1','eu-west-1','eu-central-1'],clusters:5,status:'warn',statusL:'Slowed by AWS throttling',last:'12 s ago'},
  {name:'staging-example',id:A.stag.id,role:'arn:aws:iam::731022849906:role/harbormaster-access',regions:['us-east-1'],clusters:1,status:'bad',statusL:'Data is 4 min old',last:'4 min ago',err:'ecs:DescribeServices: AccessDenied'}
 ];
 var vpcs=[
  {name:'homelab-main',id:'vpc-0a41c7e29b3d',acct:'homelab-admin',region:'us-east-1',cidr:'10.20.0.0/16',clusters:2,tasks:13},
  {name:'shared-services',id:'vpc-07de19a4c011',acct:'homelab-admin',region:'us-east-1',cidr:'10.21.0.0/16',clusters:1,tasks:3},
  {name:'eu-apps',id:'vpc-0f3392bb71e2',acct:'homelab-admin',region:'eu-west-1',cidr:'10.30.0.0/16',clusters:1,tasks:3},
  {name:'edge',id:'vpc-03b1d7f0c5a8',acct:'homelab-admin',region:'eu-central-1',cidr:'10.40.0.0/16',clusters:1,tasks:3},
  {name:'staging',id:'vpc-0b18e6cd2a91',acct:'staging-example',region:'us-east-1',cidr:'10.50.0.0/16',clusters:1,tasks:2}
 ];
 var efs=[
  {name:'uploads-archive',id:'fs-07a2b9e1',region:'us-east-1',vpc:'homelab-main',gb:211.6,growth:'+0%',mode:'Elastic',mibs:0,ap:0,mounted:[]},
  {name:'search-index',id:'fs-0d18c2ab',region:'eu-west-1',vpc:'eu-apps',gb:74.9,growth:'+6%',mode:'Provisioned 64',mibs:31,ap:1,mounted:[{svc:'search-indexer',cluster:'eu-apps',path:'/index'}]},
  {name:'jenkins-home',id:'fs-0c91a4e7',region:'us-east-1',vpc:'shared-services',gb:38.4,growth:'+11%',mode:'Elastic',mibs:12,ap:1,mounted:[{svc:'jenkins-controller',cluster:'ci-tools',path:'/var/jenkins_home'}]},
  {name:'reports-data',id:'fs-02e7d1c9',region:'us-east-1',vpc:'homelab-main',gb:12.1,growth:'+3%',mode:'Bursting',mibs:4,ap:1,mounted:[{svc:'report-worker',cluster:'background-jobs',path:'/data/reports'}]},
  {name:'shared-certs',id:'fs-0a6b33f0',region:'eu-central-1',vpc:'edge',gb:0.02,growth:'—',mode:'Bursting',mibs:0.1,ap:2,mounted:[{svc:'edge-nginx',cluster:'edge-proxy',path:'/etc/nginx/certs'},{svc:'cert-renewer',cluster:'edge-proxy',path:'/certs'}]}
 ];
 var log=[
  ['11:42:18','info','payments-api/9f2e1a3b','POST /v1/charges 201 143ms provider=stripe'],
  ['11:42:17','info','payments-api/b81c7d40','GET /health 200 2ms'],
  ['11:42:15','warn','payments-api/9f2e1a3b','slow query took 812ms: SELECT * FROM items WHERE owner_id = $1'],
  ['11:42:14','info','otel-collector/e30a95d1','exported 128 spans in 41ms'],
  ['11:42:11','error','payments-api/b81c7d40','card_declined code=insufficient_funds charge=ch_3Q8x'],
  ['11:42:09','info','payments-api/e30a95d1','GET /api/v1/items 200 41ms'],
  ['11:42:06','warn','payments-api/e30a95d1','upstream timeout after 5000ms (retrying 1/3)'],
  ['11:42:03','info','payments-api/9f2e1a3b','POST /v1/charges 201 151ms provider=stripe'],
  ['11:41:58','info','payments-api/b81c7d40','GET /health 200 2ms'],
  ['11:41:52','info','payments-api/9f2e1a3b','POST /v1/refunds 200 96ms provider=stripe']
 ];
 var stopReasons=[['Replaced by a deployment',15,'mute'],['Out of memory (exit 137)',6,'bad'],['Failed load balancer health checks',3,'warn'],['Spot interruption',2,'exp'],['Scaled in',2,'mute'],['Stopped by a user',1,'mute']];
 return {accounts:accounts,clusters:clusters,services:services,tasks:tasks,attention:attention,activity:activity,dataHealth:dataHealth,users:users,tags:tags,vpcs:vpcs,efs:efs,log:log,stopReasons:stopReasons,
  svc:function(n){return services.filter(function(x){return x.name===n})[0]},cluster:function(n){return clusters.filter(function(x){return x.name===n})[0]}};
})();
