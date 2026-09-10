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
function icon(name) {
  const svg = document.createElementNS('http://www.w3.org/2000/svg','svg');
  svg.classList.add('icon'); svg.setAttribute('aria-hidden','true');
  const use = document.createElementNS(svg.namespaceURI,'use'); use.setAttribute('href','#icon-'+name); svg.append(use); return svg;
}
function age(stamp) {
  if (!stamp || stamp < 0) return 'No activity yet';
  const minutes = Math.max(0,Math.floor(Date.now()/60000-stamp/60));
  if (minutes < 1) return 'Just now';
  if (minutes < 60) return minutes+'m ago';
  if (minutes < 1440) return Math.floor(minutes/60)+'h ago';
  return Math.floor(minutes/1440)+'d ago';
}
let view = 'trees';
function render(state) {
  latestState = state;
  const branches = state.Branches || [];
  $('count').textContent = state.Worktrees.length;
  $('branch-count').textContent = branches.length;
  $('running-count').textContent = state.Worktrees.filter(w => w.Services.some(s => s.Status.startsWith('running'))).length;
  const selected = state.Worktrees.find(w => w.Selected);
  $('selection').textContent = selected ? selected.Branch || selected.Name : 'No worktree selected';
  $('shared-link').href = state.URL; $('shared-link').replaceChildren(document.createTextNode(state.URL.replace('http://','')),icon('arrow-up-right')); $('shared-link').hidden = !selected;
  $('trees-tab').setAttribute('aria-pressed',view === 'trees'); $('branches-tab').setAttribute('aria-pressed',view === 'branches');
  $('view-hint').textContent = view === 'trees' ? 'Current preview first, then recent activity' : 'Switch servers to a local branch. New checkouts may need dependencies installed.';
  $('name-label').textContent = view === 'trees' ? 'WORKTREE / BRANCH' : 'BRANCH';
  $('worktrees').replaceChildren();
  const query = $('search').value.trim().toLowerCase();
  const entries = view === 'trees' ? state.Worktrees : branches.map(b => {
    const tree = state.Worktrees.find(w => w.Name === b.Worktree);
    return {...b, Branch:b.Name, Selected:tree?.Selected || false, Services:tree?.Services || [], LastActivity:tree?.LastActivity || b.LastActivity};
  }).sort((a,b) => Number(b.Selected)-Number(a.Selected) || b.LastActivity-a.LastActivity);
  for (const tree of entries.filter(w => (w.Name+' '+(w.Branch || '')).toLowerCase().includes(query))) {
    const row = element('article','','row'+(tree.Selected ? ' selected' : ''));
    const title = element('div','','identity');
    const glyph = element('span','','row-icon'); glyph.append(icon(view === 'trees' ? 'layers' : 'git-branch'));
    const details = element('div'); details.append(element('h3',tree.Name,'name'));
    const subtitle = element('div','','subtitle');
    if (tree.Selected) { const badge = element('span','Current','badge'); badge.prepend(icon('check')); subtitle.append(badge); }
    if (view === 'trees' && tree.Branch) subtitle.append(element('span',tree.Branch,'branch-name'));
    if (view === 'branches') subtitle.append(element('span',tree.Worktree ? 'Checkout available' : 'No checkout yet','branch-name'));
    details.append(subtitle); title.append(glyph,details);
    const services = element('ul','','services'); services.setAttribute('aria-label',tree.Name+' services');
    for (const service of tree.Services) { const status = service.Status.split(':')[0]; const item = element('li','',status); item.title = service.Status+(service.Port ? ' · port '+service.Port : ''); const dot = element('span','','dot'); dot.setAttribute('aria-hidden','true'); item.append(dot,document.createTextNode(service.Name),element('span',status,'service-state')); services.append(item); }
    if (!tree.Services.length) services.append(element('li','Uses current service configuration','service-note'));
    const activity = element('time',age(tree.LastActivity),'activity'); if (tree.LastActivity > 0) { activity.dateTime = new Date(tree.LastActivity*1000).toISOString(); activity.title = new Date(tree.LastActivity*1000).toLocaleString(); }
    const actions = element('div','','actions');
    const button = element('button',tree.Selected ? 'Reselect' : view === 'branches' && !tree.Worktree ? 'Set up & switch' : 'Switch'); button.append(icon(tree.Selected ? 'refresh-cw' : 'arrow-right')); button.type = 'button'; button.disabled = !session || $('worktrees').getAttribute('aria-busy') === 'true'; button.setAttribute('aria-label','Select '+tree.Name+' preview'); button.onclick = () => update(view === 'branches' ? 'branch' : 'use',tree.Name); actions.append(button);
    if (tree.URL) { const preview = link('Own preview',tree.URL); preview.append(icon('arrow-up-right')); preview.setAttribute('aria-label','Open '+tree.Name+' own preview at '+tree.URL); actions.append(preview); }
    row.append(title,services,activity,actions); $('worktrees').append(row);
  }
  if (!$('worktrees').children.length) $('worktrees').append(element('p',entries.length ? 'Nothing matches your search.' : view === 'branches' ? 'No local branches available. Branch switching requires one Git repository.' : 'No worktrees configured.','empty'));
}
async function update(action = 'status', name = '') {
  $('worktrees').setAttribute('aria-busy','true'); document.querySelectorAll('button').forEach(b => b.disabled = true);
  message(action !== 'status' ? 'Starting '+name+' and waiting for readiness...' : 'Reading service status...');
  try { render(await request('/picker/api',{Action:action,Name:name})); message(action !== 'status' ? 'Selected '+name+'. Refresh your app tabs.' : 'Status updated.'); }
  catch (error) { message(error.message.trim(),true); }
  finally {
    $('worktrees').setAttribute('aria-busy','false'); document.querySelectorAll('button').forEach(b => b.disabled = !session);
    if (action !== 'status') [...document.querySelectorAll('button')].find(b => b.getAttribute('aria-label') === 'Select '+name+' preview')?.focus({preventScroll:true});
  }
}
document.querySelectorAll('[data-icon]').forEach(node => node.append(icon(node.dataset.icon)));
$('trees-tab').onclick = () => { view = 'trees'; if (latestState) render(latestState); };
$('branches-tab').onclick = () => { view = 'branches'; if (latestState) render(latestState); };
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
