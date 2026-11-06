var Screen={state:{as:'on',desired:3,draft:'3',dlg:false},vals:function(c,s){
 var hasAs=s.as==='on',min=2,max=6,n=s.draft===''?NaN:Number(s.draft),valid=Number.isInteger(n)&&n>=0&&n<=10,pct=function(v){return (v/10*100)+'%'};
 var outside=hasAs&&valid&&(n<min||n>max);
 var acts=[['11 min ago','ok','Successfully set desired count to 3.','Target tracking: CPU 41% is below 54%, the scale-in threshold. Changing the desired count from 4 to 3.'],
  ['27 min ago','ok','Successfully set desired count to 4.','Target tracking: CPU 78% is above 66%. Changing the desired count from 3 to 4.'],
  ['Yesterday 08:00','ok','Successfully set desired count to 3.','Scheduled action weekday-peak raised the minimum to 3.'],
  ['2 days ago','ok','Successfully set desired count to 2.','Target tracking: CPU 29% is below 54% for 15 minutes.'],
  ['4 days ago','warn','Set desired count to 6, the maximum.','Target tracking wanted 7 tasks. The maximum of 6 capped it.']].map(function(a){return {when:a[0],status:a[1],statusL:a[1]==='ok'?'Done':'Capped',text:a[2],cause:a[3]}});
 return {
  asSeg:H.seg(c,'as',[['on','Auto scaling on'],['off','No auto scaling']]),hasAs:hasAs,noAs:!hasAs,desired:s.desired,min:min,max:max,draft:s.draft,
  barAria:'Auto scaling range '+min+' to '+max+' tasks, desired '+s.desired,
  rangeStyle:'position:absolute;top:7px;height:8px;border-radius:4px;background:var(--accent);opacity:.55;left:'+pct(min)+';width:'+pct(max-min),
  markStyle:'position:absolute;top:1px;width:3px;height:20px;background:var(--text);left:calc('+pct(s.desired)+' - 1px)',
  invalid:!valid,outside:outside,atCap:valid&&n>=10,
  note:hasAs?'Running '+s.desired+' of '+s.desired+'. Auto scaling keeps this service between '+min+' and '+max+' tasks and may change a value outside that range back.':'No auto scaling: the count stays where you set it. Setting 0 stops every task without deleting the service.',
  onDraft:function(e){c.setState({draft:e.target.value})},
  dec:function(){c.setState({draft:String(Math.max(0,(Number(c.state.draft)||0)-1))})},inc:function(){c.setState({draft:String(Math.min(10,(Number(c.state.draft)||0)+1))})},
  update:function(){if(!valid)return;if(n===c.state.desired){H.toast(c,'Desired count is already '+n+'.')();return}c.setState({dlg:true})},
  dlg:s.dlg,close:H.set(c,{dlg:false}),zero:valid&&n===0,title:valid?'Set payments-api to '+n+' '+(n===1?'task':'tasks')+'?':'Set payments-api to a valid number of tasks?',
  confirm:function(){c.setState({dlg:false,desired:n});H.toast(c,'Desired count set to '+n+'.')()},acts:acts
 };
}};
