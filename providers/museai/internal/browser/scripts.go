package browser

// DOM selectors follow the pinned muse2api engine. Scope all mutation to the
// composer and all result reads to agent messages after the submitted user turn.
const domHelpers = `
const visible = e => !!e && !!(e.getClientRects().length) && getComputedStyle(e).visibility !== 'hidden';
const textarea = () => [...document.querySelectorAll('textarea')].find(visible);
const composer = () => { const t=textarea(); return t && (t.closest('form,[data-testid="hatch-composer"]') || t.parentElement.parentElement.parentElement); };
const agents = () => [...document.querySelectorAll('div[class*="hatch-chat-groupable-bubble"][class*="hatch-agent-bubble-bg"],[data-message-role="assistant"],[data-testid="hatch-agent-message"]')].filter((e,i,a)=>!a.some((p,j)=>j!==i && p.contains(e)));
const users = () => [...document.querySelectorAll('div[class*="hatch-chat-groupable-bubble"][class*="chat-user-bubble"],[data-message-role="user"],[data-testid="hatch-user-message"]')].filter((e,i,a)=>!a.some((p,j)=>j!==i && p.contains(e)));
const stopButton = () => [...document.querySelectorAll('[data-testid="hatch-composer-stop-button"],button[aria-label="Stop" i],button[aria-label="Stop generating" i],button[aria-label="停止"],button[aria-label="停止生成"]')].find(visible);
const sendButton = () => { const c=composer(); return c && [...c.querySelectorAll('button,[role="button"]')].find(e=>visible(e) && (e.dataset.testid==='hatch-composer-send-button' || /^(send|send message|发送|发送消息)$/i.test(e.getAttribute('aria-label')||'') || /^(send|发送)$/i.test((e.innerText||'').trim()))); };
const blocked = () => [...document.querySelectorAll('[role="dialog"],[role="alertdialog"],[aria-modal="true"],[data-testid*="approval"],[data-testid*="permission"]')].some(visible);
const root = document.querySelector('main,[class*="hatch-chat-scroll"],[class*="chat-scroll"]');
if(root) for(const e of [root,...root.querySelectorAll('[class*="scroll"]')]) { const s=getComputedStyle(e);if(s.overflowY==='auto'||s.overflowY==='scroll')e.scrollTop=e.scrollHeight; }
`
const readyJS = `(()=>{` + domHelpers + `
const t=textarea();const h=document.querySelector('[data-hatch-shell-hydration-state]');
return {ready:document.readyState==='complete' && !!t && (!h || h.getAttribute('data-hatch-shell-hydration-state')==='hydrated') && !(document.body.innerText||'').includes('Connecting...'),blocked:blocked(),login:!!document.querySelector('input[type="password"],a[href="/login"],a[href="/signin"]')};})()`
const baselineJS = `(()=>{` + domHelpers + `
const as=agents(),us=users();return {agents:as.length,users:us.length,sources:[...document.querySelectorAll('img,video')].map(e=>e.currentSrc||e.src||'')};})()`
const preparePromptJS = `(prompt=>{` + domHelpers + `
if(blocked())return false;const t=textarea();if(!t)return false;t.scrollIntoView({block:'center'});t.focus();
const setter=Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype,'value').set;setter.call(t,prompt);t.dispatchEvent(new Event('input',{bubbles:true}));t.dispatchEvent(new Event('change',{bubbles:true}));return true;})`
const sendStateJS = `(prompt=>{` + domHelpers + `
const t=textarea(),b=sendButton();return {ready:!blocked() && !!t && t.value===prompt && !!b && !b.disabled,blocked:blocked()};})`
const clickSendJS = `(prompt=>{` + domHelpers + `
if(blocked())return false;const t=textarea(),b=sendButton();if(!t||t.value!==prompt||!b||b.disabled)return false;b.click();return true;})`
const uploadJS = `(refs=>{` + domHelpers + `
if(blocked())return false;const c=composer(),input=c && c.querySelector('input[type="file"]');if(!input)return false;
const dt=new DataTransfer();for(let i=0;i<refs.length;i++){const r=refs[i],s=atob(r.data),bytes=new Uint8Array(s.length);for(let j=0;j<s.length;j++)bytes[j]=s.charCodeAt(j);const ext=r.mime.split('/')[1].replace('jpeg','jpg');dt.items.add(new File([bytes],'reference_'+i+'.'+ext,{type:r.mime}));}
input.files=dt.files;input.dispatchEvent(new Event('change',{bubbles:true}));input.dispatchEvent(new Event('input',{bubbles:true}));return true;})`
const uploadStateJS = `(()=>{` + domHelpers + `
const c=composer();return {blocked:blocked(),count:c?Math.max(c.querySelectorAll('button[aria-label*="Remove attachment" i],button[aria-label*="移除附件"],button[aria-label*="删除附件"]').length,c.querySelectorAll('[data-testid^="hatch-chat-attachment-presentation-"]').length,c.querySelectorAll('img:not([src*="avatar"]),video').length):0,pending:c?!!c.querySelector('[role="progressbar"],[data-upload-state="uploading"],[aria-busy="true"]'):true};})()`
const pollJS = `(opts=>{` + domHelpers + `
const us=users(),as=agents();let selected=[];
if(opts.resume){const last=us[us.length-1];if(last)selected=as.filter(e=>!!(last.compareDocumentPosition(e)&Node.DOCUMENT_POSITION_FOLLOWING));}
else {const sent=us[opts.users];if(sent)selected=as.filter(e=>!!(sent.compareDocumentPosition(e)&Node.DOCUMENT_POSITION_FOLLOWING));selected=selected.filter(e=>!e.closest('form,[class*="chat-user-bubble"]'));}
const renderedText=value=>value.replace(/\s+/g,' ').trim();
const confirmed=opts.resume?us.length>0:us.length>opts.users && renderedText(us[opts.users].innerText||'').includes(renderedText(opts.prompt));
const text=selected.map(e=>(e.innerText||'').trim()).filter(Boolean).join('\n\n');
const terminal=selected.length>0 && selected.every(e=>e.getAttribute('data-message-status')==='completed' || e.getAttribute('data-status')==='completed' || e.getAttribute('data-state')==='complete' || !!e.querySelector('[data-testid="hatch-message-completed"],[data-message-status="completed"]'));
const turnUser=opts.resume?us[us.length-1]:us[opts.users];
const resultRoots=[...selected];if(turnUser)for(const e of document.querySelectorAll('[data-testid^="hatch-chat-attachment-presentation-"]')){
 if(!(turnUser.compareDocumentPosition(e)&Node.DOCUMENT_POSITION_FOLLOWING) || e.closest('form,[class*="chat-user-bubble"],[class*="group/msg"]') || resultRoots.some(p=>p.contains(e)))continue;
 resultRoots.push(e);
}
const media=[],seen=new Set();for(const agent of resultRoots)for(const e of agent.querySelectorAll('img,video')){
 if(e.closest('form,[class*="chat-user-bubble"]'))continue;
 const video=e.tagName==='VIDEO';let src=e.currentSrc||e.src||'';if(video && !src){const s=e.querySelector('source');src=s && s.src || '';}
 if(!src || seen.has(src) || opts.sources.includes(src) || (!video && /avatar|emoji/i.test(src)))continue;
 const attachment=e.closest('[data-testid^="hatch-chat-attachment-presentation-"]');
 if(!attachment && !e.closest('[class*="hatch-agent-bubble-bg"],[data-message-role="assistant"],[data-testid="hatch-agent-message"]'))continue;
 seen.add(src);media.push({src,video,width:video?e.videoWidth:e.naturalWidth,height:video?e.videoHeight:e.naturalHeight,ready:video && e.readyState>=2 && Number.isFinite(e.duration) && e.duration>0 && e.videoWidth>0 && e.videoHeight>0});
}
return {text,confirmed,stop:!!stopButton(),terminal:terminal || (opts.resume && media.some(e=>e.video && e.ready)),blocked:blocked(),url:location.href,media};})`
const extractJS = `(async (src)=>{` + domHelpers + `
if(blocked())return {ok:false};
// Fetch only the already-selected DOM source inside the browser. Never fetch a
// supplied reference URL or follow a URL on the Go host's network.
const u=new URL(src,location.href);if(!['https:','http:','blob:','data:'].includes(u.protocol) || u.username || u.password)return {ok:false};
if(u.protocol==='data:' && !/^data:(image|video)\//i.test(src))return {ok:false};
const response=await fetch(src,{credentials:'same-origin',redirect:'error'});if(!response.ok)return {ok:false};const blob=await response.blob();if(!blob.size)return {ok:false};
const bytes=new Uint8Array(await blob.arrayBuffer());let s='';for(let i=0;i<bytes.length;i+=65536)s+=String.fromCharCode(...bytes.subarray(i,i+65536));return {ok:true,mime:blob.type,data:btoa(s)};})`
const stopJS = `(()=>{` + domHelpers + `if(blocked())return false;const b=stopButton();if(b){b.click();return true;}return false;})()`
