'use strict';
const $ = id => document.getElementById(id);
let session = sessionStorage.getItem('flip-picker') || '';
const message = (text, error = false) => { $('message').textContent = text; $('message').className = error ? 'error' : ''; };
async function request(path, body) {
  const response = await fetch(path, {method:'POST', headers:{'Content-Type':'application/json', 'Authorization':'Bearer '+session}, body:JSON.stringify(body), credentials:'omit', cache:'no-store'});
  if (!response.ok) throw new Error(await response.text());
  return response.json();
}
function element(tag, text, className) {
  const node = document.createElement(tag); node.textContent = text; if (className) node.className = className; return node;
}
function link(text, url) { const node = element('a',text); node.href = url; node.target = '_blank'; node.rel = 'noopener noreferrer'; return node; }
function render(state) {
  $('count').textContent = String(state.Worktrees.length).padStart(2,'0');
  const selected = state.Worktrees.find(w => w.Selected);
  $('selection').textContent = selected ? selected.Name : 'No worktree selected';
  $('shared-link').href = state.URL; $('shared-link').hidden = !selected;
  $('worktrees').replaceChildren();
  for (const tree of state.Worktrees) {
    const row = element('article','','row'+(tree.Selected ? ' selected' : ''));
    const title = element('div',''); title.append(element('h3',tree.Name,'name'));
    if (tree.Selected) title.append(element('span','Selected','badge'));
    const services = element('ul','','services'); services.setAttribute('aria-label',tree.Name+' services');
    for (const service of tree.Services) { const item = element('li','',service.Status.split(':')[0]); const dot = element('span','','dot'); dot.setAttribute('aria-hidden','true'); item.append(dot,document.createTextNode(service.Name+' · '+service.Status+(service.Port ? ' · :'+service.Port : ''))); services.append(item); }
    const actions = element('div','','actions');
    const button = element('button',tree.Selected ? 'Select again' : 'Select preview'); button.type = 'button'; button.setAttribute('aria-label','Select '+tree.Name+' preview'); button.onclick = () => update('use',tree.Name); actions.append(button);
    if (tree.URL) actions.append(link('Own preview ↗',tree.URL));
    row.append(title,services,actions); $('worktrees').append(row);
  }
}
async function update(action = 'status', name = '') {
  $('worktrees').setAttribute('aria-busy','true'); document.querySelectorAll('button').forEach(b => b.disabled = true);
  message(action === 'use' ? 'Starting '+name+' and waiting for readiness…' : 'Reading service status…');
  try { render(await request('/picker/api',{Action:action,Name:name})); message(action === 'use' ? 'Selected '+name+'. Refresh your app tabs.' : 'Status updated.'); }
  catch (error) { message(error.message.trim(),true); }
  finally { $('worktrees').setAttribute('aria-busy','false'); document.querySelectorAll('button').forEach(b => b.disabled = !session); }
}
$('refresh').onclick = () => update();
(async () => {
  const grant = location.hash.slice(1); history.replaceState(null,'',location.pathname);
  try {
    if (grant) { session = ''; sessionStorage.removeItem('flip-picker'); session = (await request('/picker/session',{Grant:grant})).session; sessionStorage.setItem('flip-picker',session); }
    if (session) await update();
  } catch (error) { message(error.message.trim(),true); }
})();
