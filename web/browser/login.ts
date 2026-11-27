const form=document.querySelector('form')!;
const errorBox=document.getElementById('error')!;
async function fetchCSRF(){const response=await fetch('/auth/csrf');if(!response.ok)throw new Error('CSRF_UNAVAILABLE');const {csrf}=await response.json();return csrf as string}
async function redeemTicket(ticket:string){
 errorBox.textContent='正在通过一次性安全链接自动登录…';
 try{
  const csrf=await fetchCSRF();
  const result=await fetch('/auth/ticket',{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':csrf},body:JSON.stringify({ticket})});
  if(result.ok){location.replace('/');return}
  errorBox.textContent=result.status===429?'登录尝试过于频繁，请稍后重试。':'一次性登录链接已失效或已使用，请在控制面板重新复制，或输入密码登录。';
 }catch{errorBox.textContent='网络异常，请重试或输入密码登录。'}
}
const hashParams=new URLSearchParams(location.hash.slice(1));
const ticket=hashParams.get('ticket');
if(ticket){history.replaceState(null,'',location.pathname);void redeemTicket(ticket)}
form.onsubmit=async(event)=>{event.preventDefault();const field=document.getElementById('password') as HTMLInputElement;const csrf=await fetchCSRF();const result=await fetch('/auth/login',{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':csrf},body:JSON.stringify({password:field.value})});field.value='';if(result.ok)location.assign('/');else errorBox.textContent=result.status===429?'登录尝试过于频繁，请稍后重试。':'密码验证未通过。'};
