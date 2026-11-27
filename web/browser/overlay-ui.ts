export function banner(message:string,action?:{label:string,run:()=>void}){
 const existing=document.getElementById('cwb-status');existing?.remove();
 const box=document.createElement('aside');box.id='cwb-status';box.setAttribute('role','alert');
 box.textContent=message;
 if(action){const button=document.createElement('button');button.textContent=action.label;button.onclick=action.run;box.append(button);}
 (document.body??document.documentElement).append(box);
}
