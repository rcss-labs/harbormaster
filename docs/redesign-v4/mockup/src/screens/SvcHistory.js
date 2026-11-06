var Screen={state:{src:'all',ecs:true,shown:12},vals:function(c,s){
 var L={hm:'Harbormaster',ecs:'ECS events',ct:'CloudTrail'};
 var all=[[0,'09:14','hm','dana','Deployed revision :42, image 1.18.2'],[0,'09:15','ecs','ECS','Service reached a steady state.'],[0,'09:12','ecs','ECS','Started 3 tasks (deployment ecs-svc/8841).'],[0,'09:12','ecs','ECS','Stopped 3 tasks (deployment ecs-svc/8841).'],[0,'08:55','hm','dana','Set desired count to 3'],
  [1,'17:40','ct','terraform-ci','Updated the load balancer health check path to /health'],[1,'16:02','ecs','ECS','Registered target 10.20.1.67 in payments-api-tg.'],[1,'11:30','hm','omer','Restarted the service'],[1,'11:31','ecs','ECS','Started 3 tasks (deployment ecs-svc/8802).'],
  [2,'14:20','hm','dana','Registered task definition revision :41'],[2,'14:26','hm','dana','Deployed revision :41, image 1.18.1'],[2,'14:41','ecs','ECS','Rolled back to :40: deployment ecs-svc/8790 failed (circuit breaker).'],[2,'14:38','ecs','ECS','Unable to consistently start tasks successfully.'],
  [4,'10:05','ct','dana','Changed the Application Auto Scaling target to min 2, max 6'],[4,'09:10','hm','omer','Opened a terminal session in task e30a95d1c6b4'],[4,'09:48','ecs','ECS','Stopped 1 task: scaled in.'],
  [6,'13:00','hm','pavel','Set desired count to 2'],[6,'13:02','ecs','ECS','Stopped 1 task (user request).'],[7,'15:15','ct','terraform-ci','Updated the task role payments-api-task'],[7,'15:30','hm','dana','Deployed revision :40, image 1.17.4'],[9,'10:00','ecs','ECS','Service reached a steady state.'],[9,'09:40','hm','omer','Restarted the service'],[11,'16:20','ct','terraform-ci','Updated security group sg-04c1e7a9'],[12,'12:00','hm','dana','Deployed revision :39, image 1.17.2']];
 all.sort(function(a,b){return a[0]-b[0]||(a[1]<b[1]?1:-1)});var dayL=function(d){return d===0?'Today':d===1?'Yesterday':d+' days ago'};
 var cnt=function(k){return all.filter(function(r){return (k==='all'||r[2]===k)&&(s.ecs||r[2]!=='ecs')}).length};
 var rows=all.filter(function(r){return (s.src==='all'||r[2]===s.src)&&(s.ecs||r[2]!=='ecs')});
 var vis=rows.slice(0,s.shown),groups=[];
 vis.forEach(function(r){var g=groups.length&&groups[groups.length-1].d===r[0]?groups[groups.length-1]:null;if(!g){g={d:r[0],day:dayL(r[0]),rows:[]};groups.push(g)}g.rows.push({time:r[1],srcL:L[r[2]],actor:r[3],text:r[4]})});
 return {srcSeg:H.seg(c,'src',[['all','All',cnt('all')],['hm','Harbormaster',cnt('hm')],['ecs','ECS events',cnt('ecs')],['ct','CloudTrail',cnt('ct')]]),
  showEcs:s.ecs,toggleEcs:H.toggle(c,'ecs'),groups:groups,empty:!rows.length,more:rows.length>s.shown,
  loadMore:function(){c.setState({shown:c.state.shown+10})},reset:H.set(c,{src:'all',ecs:true})};
}};
