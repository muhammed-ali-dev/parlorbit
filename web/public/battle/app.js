const $ = s => document.querySelector(s);
const canvas = $('#arena'), ctx = canvas.getContext('2d');
const COLORS = ['#357b73','#637bb0','#9974a6','#448396','#aa657b','#6c8252','#647f93','#8a7395'];
const LABELS = {ice:'Ice',speed:'Speed',giant:'Giant',tiny:'Tiny',wind:'Wind',lava:'Lava',reverse:'Reverse',chaos:'Hazards'};
const PHASES = {lobby:'Ready',build:'Add a mutation',resolve:'Combining ideas',play:'Playing',results:'Round over',finished:'Finished'};
const params = new URLSearchParams(location.search);
const roomQuery = new URLSearchParams({houseId:params.get('houseId') || '',roomId:params.get('roomId') || ''}).toString();
if (params.get('houseId') && params.get('roomId')) $('#back-link').href = `/houses/${encodeURIComponent(params.get('houseId'))}/rooms/${encodeURIComponent(params.get('roomId'))}`;
let session = null, state = null, source = null, offset = 0, phaseKey = '', playersKey = '', mutationsKey = '', sending = false, lastInput = '', lastSent = 0;
const keys = new Set();
let palette;
function readPalette(){const css=getComputedStyle(document.documentElement);palette=Object.fromEntries(['arena-bg','arena-grid','arena-border','ink','spark','danger'].map(key=>[key,css.getPropertyValue(`--${key}`).trim()]));
  // Resolve light-dark() into computed colors through a real CSS property.
  const probe=document.createElement('span');probe.hidden=true;document.body.append(probe);for(const key of Object.keys(palette)){probe.style.color=`var(--${key})`;palette[key]=getComputedStyle(probe).color}probe.remove();}
readPalette();matchMedia('(prefers-color-scheme: dark)').addEventListener('change',readPalette);
function toast(message){$('#toast').textContent=message;$('#toast').hidden=false;clearTimeout(toast.timer);toast.timer=setTimeout(()=>$('#toast').hidden=true,3000)}
async function api(path,data={}){const response=await fetch(`/api/v1/battle/${path}?${roomQuery}`,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(data)});const result=await response.json();if(!response.ok)throw new Error(result.error?.message || 'That didn’t work. Try again.');return result.data}
function textEl(tag,text,className){const el=document.createElement(tag);el.textContent=text;if(className)el.className=className;return el}
function setMode(){const live=state?.mode==='gemini',demo=state?.mode==='script-demo',limited=state?.mode==='local-limited',fallback=state?.mode==='local-fallback',configured=session?.aiConfigured&&!['play','results','finished'].includes(state?.phase);$('#engine-mode').textContent=live?'AI mode':demo?'Program demo':limited?'AI paused':fallback?'Demo fallback':configured?'AI enabled':'Demo mode';$('#mode-description').textContent=(live?'Generated with '+session.model+'.':demo?'Handwritten portal program. No model was called.':limited?state.summary:fallback?'AI resolution failed. Keyword rules kept this round running.':configured?'One '+session.model+' call combines all players’ ideas each round.':'Demo mode uses the suggested keyword rules. No AI model is called.')+' Up to 8 players; new players join the House first.'+(session?' AI allowance: '+session.aiDailyLimit+'/day, '+session.aiMonthlyLimit+'/month.':'')}
async function join(){
  $('#join-overlay').hidden=false;$('#retry').hidden=true;$('#join-title').textContent='Opening the arena…';$('#join-error').textContent='';
  try{session=await api('join');source?.close();source=new EventSource(`/api/v1/battle/events?${roomQuery}`);source.onmessage=e=>{state=JSON.parse(e.data);offset=state.serverNow-Date.now();$('#join-overlay').hidden=true;renderUI()};source.onerror=()=>{$('#phase-label').textContent='Reconnecting…'};$('#invite').disabled=false;setMode()}
  catch(error){$('#join-title').textContent='Couldn’t enter the arena.';$('#join-error').textContent=error.message;$('#retry').hidden=false}
}
$('#retry').addEventListener('click',join);
$('#show-code').addEventListener('click',()=>{$('#code-summary').textContent=state.summary;$('#feature-code').textContent=JSON.stringify(state.programs,null,2);$('#code-dialog').showModal();$('#options').open=false});
$('#close-code').addEventListener('click',()=>$('#code-dialog').close());
$('#demo-program').addEventListener('click',()=>{$('#options').open=false;api('demo').catch(error=>toast(error.message))});
$('#invite').addEventListener('click',async()=>{try{await navigator.clipboard.writeText(location.href);toast('Arena link copied.')}catch{toast('Copy the arena URL from the address bar.')}$('#options').open=false});
$('#reset').addEventListener('click',()=>{keys.clear();$('#options').open=false;api('reset').catch(error=>toast(error.message))});
$('#start-button').addEventListener('click',()=>api('start').catch(error=>toast(error.message)));
$('#next-button').addEventListener('click',()=>{const task=state?.phase==='finished'?api('reset').then(()=>api('start')):api('advance');task.catch(error=>toast(error.message))});
$('#prompt-form').addEventListener('submit',async e=>{e.preventDefault();$('#submit-prompt').disabled=true;try{const result=await api('prompt',{prompt:$('#prompt').value});$('#prompt-feedback').textContent=result.aiConfigured?'Submitted. You can still edit it.':result.supported?`Queued: ${result.matched.map(id=>LABELS[id]).join(' + ')}.`:'No match. Try one of the ideas.';$('#submit-prompt').textContent='Update'}catch(error){$('#prompt-feedback').textContent=error.message}finally{$('#submit-prompt').disabled=state?.phase!=='build'}});
document.querySelectorAll('[data-prompt]').forEach(button=>button.addEventListener('click',()=>{$('#prompt').value=button.dataset.prompt;button.closest('details').open=false;$('#prompt').focus()}));
document.addEventListener('pointerdown',e=>{if(!$('#options').contains(e.target))$('#options').open=false});
document.addEventListener('keydown',e=>{if(e.key==='Escape'){$('#options').open=false;document.querySelector('.ideas').open=false}});
function renderUI(){
  const host=state.host===session.id,build=state.phase==='build',play=state.phase==='play';document.body.dataset.phase=state.phase;
  $('#round-label').textContent=state.round?`Round ${state.round} / 3`:'3 rounds';$('#phase-label').textContent=PHASES[state.phase];
  $('#reset').hidden=!host;$('#reset').disabled=!host;$('#composer').hidden=!build;$('#phase-overlay').hidden=false;$('#touch-controls').hidden=!play;
  $('#show-code').disabled=!(state.programs?.length);$('#demo-program').hidden=!(host&&state.phase==='lobby'&&!session.aiConfigured);
  $('#play-help').textContent=play?'WASD / arrows · Collect sparks, avoid hazards.':'Collect sparks. Avoid hazards.';
  $('#player-count').textContent=`${state.players.length} ${state.players.length===1?'player':'players'}`;$('#prompt-status').textContent=`${state.submitted.length} of ${state.players.length} submitted`;
  $('#submit-prompt').disabled=!build;$('#prompt').disabled=!build;setMode();
  const nextPlayersKey=JSON.stringify(state.players.map(p=>[p.id,p.name,p.score,p.connected,build&&state.submitted.includes(p.id)]));
  if(nextPlayersKey!==playersKey){playersKey=nextPlayersKey;$('#players').replaceChildren();[...state.players].sort((a,b)=>b.score-a.score).forEach(p=>{const row=textEl('div','','player'),avatar=textEl('span',p.name.slice(0,1).toUpperCase(),'player-avatar');avatar.style.background=COLORS[p.color];const name=textEl('span',p.name,'name');if(p.id===session.id)name.append(textEl('small','you'));row.append(avatar,name);if(!p.connected)row.append(textEl('small','away'));else if(build&&state.submitted.includes(p.id))row.append(textEl('small','✓'));row.append(textEl('span',String(p.score),'score'));$('#players').append(row)})}
  const nextMutationsKey=JSON.stringify([state.mutations,state.programs]);if(nextMutationsKey!==mutationsKey){mutationsKey=nextMutationsKey;$('#mutations').replaceChildren();for(const id of state.mutations)$('#mutations').append(textEl('span',LABELS[id],'mutation-chip'));for(const program of state.programs||[])$('#mutations').append(textEl('span',program.name,'mutation-chip'))}
  const nextPhaseKey=`${state.phase}-${state.round}-${host}`;
  if(nextPhaseKey!==phaseKey){phaseKey=nextPhaseKey;keys.clear();lastInput='';$('#start-button').hidden=!(state.phase==='lobby'&&host);$('#next-button').hidden=!(['build','results'].includes(state.phase)&&host);$('#next-button').textContent=build?'Play now':'Next round';$('#next-button').className=build?'button quiet':'button primary';$('#results-list').replaceChildren();
    const copy={play:['The arena is live.','Collect sparks and avoid hazards. Move with WASD or arrow keys.'],lobby:['Ready to play?',host?'Start a game when everyone is here.':'Waiting for the House host to start.'],build:['What changes?','One idea each. Changes stay for the next round.'],resolve:['Combining your ideas…','This should only take a moment.'],results:['Round complete','Here’s where everyone stands.'],finished:['Game over','Final scores. Play again when you’re ready.']}[state.phase];
    if(copy){$('#phase-title').textContent=copy[0];$('#phase-description').textContent=copy[1]}
    if(build){$('#prompt').value='';$('#prompt-feedback').textContent='';$('#submit-prompt').textContent='Submit';document.querySelector('.ideas').open=false}
    if(state.phase==='finished'&&host){$('#next-button').hidden=false;$('#next-button').textContent='Play again'}
  }
  if(['results','finished'].includes(state.phase)){$('#results-list').replaceChildren();[...state.players].sort((a,b)=>b.score-a.score).forEach((p,i)=>{const row=textEl('div','','results-row');row.append(textEl('span',`${i+1}. ${p.name}`),textEl('strong',`${p.score} sparks`));$('#results-list').append(row)})}
}
function editable(target){return target instanceof HTMLElement&&(['INPUT','TEXTAREA'].includes(target.tagName)||target.isContentEditable)}
addEventListener('keydown',e=>{if(editable(e.target)||state?.phase!=='play')return;const key=e.key.length===1?e.key.toLowerCase():e.key;if(['ArrowUp','ArrowDown','ArrowLeft','ArrowRight','w','a','s','d'].includes(key)){e.preventDefault();keys.add(key)}});
addEventListener('keyup',e=>keys.delete(e.key.length===1?e.key.toLowerCase():e.key));addEventListener('blur',()=>{keys.clear();lastInput=''});
document.querySelectorAll('[data-direction]').forEach(button=>{const key={up:'ArrowUp',down:'ArrowDown',left:'ArrowLeft',right:'ArrowRight'}[button.dataset.direction];button.addEventListener('pointerdown',e=>{e.preventDefault();button.setPointerCapture(e.pointerId);keys.add(key)});for(const event of ['pointerup','pointercancel','lostpointercapture'])button.addEventListener(event,()=>keys.delete(key))});
setInterval(async()=>{if(!session||!state||sending)return;const x=state.phase==='play'?Number(keys.has('d')||keys.has('ArrowRight'))-Number(keys.has('a')||keys.has('ArrowLeft')):0,y=state.phase==='play'?Number(keys.has('s')||keys.has('ArrowDown'))-Number(keys.has('w')||keys.has('ArrowUp')):0,sig=`${x},${y}`;if(sig===lastInput&&performance.now()-lastSent<150)return;sending=true;try{await api('input',{x,y});lastInput=sig;lastSent=performance.now()}catch{}finally{sending=false}},50);
function draw(time){
  ctx.clearRect(0,0,1000,640);ctx.fillStyle=palette['arena-bg'];ctx.fillRect(0,0,1000,640);ctx.strokeStyle=palette['arena-grid'];ctx.lineWidth=1;
  for(let x=20;x<1000;x+=40){ctx.beginPath();ctx.moveTo(x,0);ctx.lineTo(x,640);ctx.stroke()}for(let y=0;y<640;y+=40){ctx.beginPath();ctx.moveTo(0,y);ctx.lineTo(1000,y);ctx.stroke()}
  ctx.strokeStyle=palette['arena-border'];ctx.lineWidth=2;ctx.strokeRect(15,15,970,610);
  for(const coin of state?.coins||[]){ctx.save();ctx.translate(coin.x,coin.y);ctx.rotate(Math.PI/4);ctx.fillStyle=palette.spark;ctx.fillRect(-5,-5,10,10);ctx.restore()}
  for(const hazard of state?.hazards||[]){ctx.save();ctx.beginPath();ctx.arc(hazard.x,hazard.y,hazard.r,0,Math.PI*2);ctx.fillStyle=palette.danger;ctx.globalAlpha=.17;ctx.fill();ctx.globalAlpha=.8;ctx.strokeStyle=palette.danger;ctx.lineWidth=2;ctx.stroke();ctx.restore()}
  for(const feature of state?.features||[]){ctx.save();ctx.strokeStyle=feature.kind==='score'?palette.spark:palette.ink;ctx.fillStyle=ctx.strokeStyle;ctx.globalAlpha=.1;ctx.beginPath();ctx.arc(feature.x,feature.y,feature.r,0,Math.PI*2);ctx.fill();ctx.globalAlpha=.65;ctx.lineWidth=2;ctx.setLineDash(feature.kind==='portal'?[8,5]:[]);ctx.stroke();ctx.setLineDash([]);ctx.font='13px Nunito,sans-serif';ctx.textAlign='center';ctx.fillText(feature.name,feature.x,feature.y-feature.r-9);ctx.restore()}
  for(const p of state?.players||[]){const radius=state.radius;ctx.save();if(p.cooldown&&Math.floor(time/100)%2)ctx.globalAlpha=.45;if(p.id===session?.id){ctx.beginPath();ctx.arc(p.x,p.y,radius+6,0,Math.PI*2);ctx.strokeStyle=COLORS[p.color];ctx.lineWidth=1;ctx.stroke()}ctx.beginPath();ctx.arc(p.x,p.y,radius,0,Math.PI*2);ctx.fillStyle=COLORS[p.color];ctx.fill();ctx.fillStyle='#ffffff';ctx.beginPath();ctx.arc(p.x-5,p.y-3,2.2,0,7);ctx.arc(p.x+5,p.y-3,2.2,0,7);ctx.fill();ctx.fillStyle=palette.ink;ctx.font='14px Nunito, sans-serif';ctx.textAlign='center';ctx.fillText(p.name,p.x,p.y-radius-12);ctx.restore()}
  $('#timer').textContent=state?.phaseEndsAt?`${Math.max(0,Math.ceil((state.phaseEndsAt-Date.now()-offset)/1000))}s`:'—';requestAnimationFrame(draw)
}
requestAnimationFrame(draw);join();
