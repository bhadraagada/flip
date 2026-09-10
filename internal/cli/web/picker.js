'use strict';
const $ = id => document.getElementById(id);
let session = sessionStorage.getItem('flip-picker') || '';
let latestState = null;
const message = (text, error = false) => { $('message').textContent = text; $('message').className = error ? 'error' : ''; };
async function request(path, body) {
  const response = await fetch(path, {method:'POST', headers:{'Content-Type':'application/json', 'Authorization':'Bearer '+session}, body:JSON.stringify(body), credentials:'omit', cache:'no-store'});
  if (!response.ok) {
    if (response.status === 403) { session = ''; sessionStorage.removeItem('flip-picker'); }
    throw new Error(await response.text());
  }
  return response.json();
}
function element(tag, text, className) {
  const node = document.createElement(tag); node.textContent = text; if (className) node.className = className; return node;
}
function link(text, url) { const node = element('a',text); node.href = url; node.target = '_blank'; node.rel = 'noopener noreferrer'; return node; }
function render(state) {
  latestState = state;
  $('count').textContent = String(state.Worktrees.length).padStart(2,'0');
  $('running-count').textContent = state.Worktrees.filter(w => w.Services.some(s => s.Status.startsWith('running'))).length;
  const selected = state.Worktrees.find(w => w.Selected);
  $('selection').textContent = selected ? selected.Name : 'No worktree selected';
  $('shared-link').href = state.URL; $('shared-link').textContent = state.URL+' ↗'; $('shared-link').hidden = !selected;
  $('worktrees').replaceChildren();
  const query = $('search').value.trim().toLowerCase();
  for (const tree of state.Worktrees.filter(w => w.Name.toLowerCase().includes(query))) {
    const row = element('article','','row'+(tree.Selected ? ' selected' : ''));
    const title = element('div',''); title.append(element('h3',tree.Name,'name'));
    if (tree.Selected) title.append(element('span','CURRENT PREVIEW','badge'));
    const services = element('ul','','services'); services.setAttribute('aria-label',tree.Name+' services');
    for (const service of tree.Services) { const status = service.Status.split(':')[0]; const item = element('li','',status); item.title = service.Status+(service.Port ? ' · port '+service.Port : ''); const dot = element('span','','dot'); dot.setAttribute('aria-hidden','true'); item.append(dot,document.createTextNode(service.Name),element('span',status,'service-state')); services.append(item); }
    const actions = element('div','','actions');
    const button = element('button',tree.Selected ? 'Reselect ↻' : 'Switch →'); button.type = 'button'; button.disabled = !session || $('worktrees').getAttribute('aria-busy') === 'true'; button.setAttribute('aria-label','Select '+tree.Name+' preview'); button.onclick = () => update('use',tree.Name); actions.append(button);
    if (tree.URL) { const preview = link(tree.URL.replace('http://','')+' ↗',tree.URL); preview.setAttribute('aria-label','Open '+tree.Name+' own preview at '+tree.URL); actions.append(preview); }
    row.append(title,services,actions); $('worktrees').append(row);
  }
  if (!$('worktrees').children.length) $('worktrees').append(element('p','No worktrees match your search.','empty'));
}
async function update(action = 'status', name = '') {
  $('worktrees').setAttribute('aria-busy','true'); document.querySelectorAll('button').forEach(b => b.disabled = true);
  message(action === 'use' ? 'Starting '+name+' and waiting for readiness…' : 'Reading service status…');
  try { render(await request('/picker/api',{Action:action,Name:name})); message(action === 'use' ? 'Selected '+name+'. Refresh your app tabs.' : 'Status updated.'); }
  catch (error) { message(error.message.trim(),true); }
  finally {
    $('worktrees').setAttribute('aria-busy','false'); document.querySelectorAll('button').forEach(b => b.disabled = !session);
    if (action === 'use') [...document.querySelectorAll('button')].find(b => b.getAttribute('aria-label') === 'Select '+name+' preview')?.focus({preventScroll:true});
  }
}
$('refresh').onclick = () => update();
$('search').oninput = () => { if (latestState) render(latestState); };
async function login() {
  const grant = location.hash.slice(1); history.replaceState(null,'',location.pathname);
  try {
    if (grant) {
      session = ''; sessionStorage.removeItem('flip-picker'); document.querySelectorAll('button').forEach(b => b.disabled = true);
      session = (await request('/picker/session',{Grant:grant})).session; sessionStorage.setItem('flip-picker',session);
    }
    if (session) await update();
  } catch (error) { message(error.message.trim(),true); }
}
window.addEventListener('hashchange',login);
login();
