'use strict';
const $ = id => document.getElementById(id);
let projects = [], config = {projects: []}, busy = false, polling = false, authRequired = false;
let discoveredCandidates = [], editingProject = null, deletingProject = null, lastProjectGroup = '';
let browseTarget = 'sourcePath', browseDirectory = '', browseParent = '', browseLoading = false, lastBrowsePath = '';
let serviceBusy = false, serviceSwitching = false;
let logsManaged = false, clearingLogs = false, logRequestVersion = 0;
try { lastBrowsePath = localStorage.getItem('pm2-last-directory') || ''; } catch (_) {}
const labels = {online: 'Running', stopped: 'Stopped', errored: 'Error', not_registered: 'Not started', launching: 'Starting', mixed: 'Mixed status', stopping: 'Stopping'};

function node(tag, cls, text) {
  const element = document.createElement(tag);
  if (cls) element.className = cls;
  if (text !== undefined) element.textContent = text;
  return element;
}
function notice(message) { $('notice').textContent = message; $('notice').hidden = !message; }
async function api(path, method = 'GET', data) {
  const headers = {}, authorization = sessionStorage.getItem('pm2-authorization');
  if (authorization && authorization.startsWith('Basic ')) headers.Authorization = authorization;
  if (data !== undefined) headers['Content-Type'] = 'application/json';
  const response = await fetch('/api/' + path, {method, headers, body: data === undefined ? undefined : JSON.stringify(data)});
  const body = await response.json();
  if (!response.ok) {
    if (response.status === 401) {
      authRequired = true; $('authError').textContent = 'Sign in or check your username and password.';
      projects = []; render(); if (!$('authDialog').open) $('authDialog').showModal();
    }
    throw new Error(body.error || 'Server error');
  }
  return body;
}
const memory = bytes => bytes >= 1073741824 ? (bytes / 1073741824).toFixed(2) + ' GiB' : (bytes / 1048576).toFixed(1) + ' MiB';
function button(text, handler) {
  const element = node('button', '', text); element.type = 'button'; element.disabled = busy;
  element.addEventListener('click', handler); return element;
}
function projectPortCell(port) {
  const cell = node('td');
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    cell.textContent = '—'; return cell;
  }
  const url = new URL('http://' + window.location.hostname);
  url.port = String(port);
  const link = node('a', 'port-link', String(port));
  link.href = url.href; link.target = '_blank'; link.rel = 'noopener noreferrer';
  link.title = 'Open ' + url.href + ' in a new tab';
  cell.append(link); return cell;
}
async function action(kind, target) {
  if (busy) return;
  busy = true; notice(''); render();
  try {
    const data = await api('action', 'POST', {action: kind, ...target});
    const failed = data.results.filter(result => !result.ok);
    if (failed.length) notice(failed.map(result => result.name + ': ' + result.error).join('\n'));
  } catch (error) { notice(error.message); }
  finally { busy = false; await refresh(); render(); }
}
function render() {
  const query = $('search').value.toLocaleLowerCase('en-US');
  const filtered = projects.filter(project => (!$('running').checked || project.status === 'online') && [project.name, project.cwd, project.group].join(' ').toLocaleLowerCase('en-US').includes(query));
  $('total').textContent = projects.length;
  $('online').textContent = projects.filter(project => project.status === 'online').length;
  $('cpu').textContent = projects.reduce((sum, project) => sum + project.cpu, 0).toFixed(1) + '%';
  $('memory').textContent = memory(projects.reduce((sum, project) => sum + project.memory, 0));
  const root = $('groups'); root.replaceChildren();
  const groups = new Map();
  for (const project of filtered) {
    const key = project.group || '';
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(project);
  }
  for (const [group, items] of groups) {
    const section = node('section', 'group'), head = node('div', 'group-head');
    const title = node('div', 'group-title', group || 'Ungrouped');
    title.append(node('small', '', items.length + (items.length === 1 ? ' project' : ' projects'))); head.append(title);
    const actions = node('div', 'actions');
    if (items.some(project => project.managed)) {
      for (const [kind, label] of [['start', 'Start group'], ['stop', 'Stop'], ['restart', 'Restart']]) actions.append(button(label, () => action(kind, {group})));
    }
    head.append(actions); section.append(head);
    const wrap = node('div', 'table-wrap'), table = node('table'), thead = node('thead'), header = node('tr');
    for (const label of ['PROJECT / DIRECTORY', 'STATUS', 'PORT', 'CPU', 'RAM', 'REQUESTS/S', 'ACTIONS']) header.append(node('th', '', label));
    thead.append(header); table.append(thead);
    const tbody = node('tbody');
    for (const project of items) {
      const row = node('tr'), identity = node('td');
      identity.append(node('span', 'project-name', project.name), node('span', 'path', project.cwd || '—')); identity.title = project.cwd; row.append(identity);
      const status = node('td'); status.append(node('span', 'badge ' + project.status, labels[project.status] || project.status));
      if (!project.managed) status.append(node('div', 'muted', 'PM2 · read only'));
      row.append(status, projectPortCell(project.port), node('td', '', project.cpu.toFixed(1) + '%'), node('td', '', memory(project.memory)));
      const metric = node('td', '', project.requestsPerSecond == null ? '—' : project.requestsPerSecond.toFixed(2));
      metric.title = project.metricsError || 'Prometheus counter; calculated after the first sample'; row.append(metric);
      const controls = node('td'), buttons = node('div', 'actions');
      if (project.managed) {
        for (const [kind, label] of [['start', 'Start'], ['stop', 'Stop'], ['restart', 'Restart']]) buttons.append(button(label, () => action(kind, {name: project.name})));
        buttons.append(button('Edit', () => openProjectForm(project.name)));
        const remove = button('Delete', () => confirmProjectDeletion(project.name)); remove.className = 'danger'; buttons.append(remove);
      }
      if (project.instances.length) buttons.append(button('Logs', () => logs(project)));
      controls.append(buttons); row.append(controls); tbody.append(row);
    }
    table.append(tbody); wrap.append(table); section.append(wrap); root.append(section);
  }
  if (!filtered.length) root.append(node('div', 'empty', projects.length ? 'No projects match your search.' : 'Add your first project. Existing PM2 processes also appear here automatically.'));
}
async function refresh() {
  if (polling || busy || authRequired) return;
  polling = true;
  try {
    const data = await api('projects'); projects = data.projects;
    $('connection').textContent = '● Connected · ' + new Date(data.updatedAt).toLocaleTimeString('en-US'); render();
  } catch (error) { $('connection').textContent = '● Connection error'; notice(error.message); }
  finally { polling = false; }
}
async function loadConfig() {
  const data = await api('config'); config = {...data, projects: data.projects || []}; return config;
}
function populateGroups(items) {
  const names = [...new Set(items.map(project => project.group).filter(Boolean))].sort((a, b) => a.localeCompare(b, 'en'));
  $('groupOptions').replaceChildren();
  for (const name of names) { const option = node('option', '', name); option.value = name; $('groupOptions').append(option); }
}
function fillProjectForm(project, includeGroup = true) {
  const fields = {projectName: project.name, projectCwd: project.cwd, projectCommand: project.command, projectPort: project.port || '', projectArgs: JSON.stringify(project.args || [], null, 2), projectEnv: JSON.stringify(project.env || {}, null, 2)};
  if (includeGroup) Object.assign(fields, {projectGroup: project.group || '', projectMetricsURL: project.metricsUrl || '', projectRequestMetric: project.requestMetric || ''});
  for (const [id, value] of Object.entries(fields)) $(id).value = value;
  updateWatchControl(project);
}
function updateWatchControl(project = null) {
  const command = $('projectCommand').value.split('/').pop();
  let args = [];
  try { const parsed = JSON.parse($('projectArgs').value); if (Array.isArray(parsed)) args = parsed; } catch (_) {}
  const dotnet = command === 'dotnet' && ['watch', 'run'].includes(args[0]);
  const next = command === 'npm' && args[0] === 'run' && ['dev', 'start'].includes(args[1]);
  $('watchLabel').hidden = !dotnet && !next;
  $('projectWatch').disabled = !dotnet && !next;
  if (project) $('projectWatch').checked = project.watchFiles ?? (dotnet ? args[0] === 'watch' : args[1] === 'dev');
  $('watchHelp').textContent = next ? 'Next.js: enabled uses npm run dev; disabled uses npm run start. Run npm run build before starting or restarting with watching disabled. npm support requires a Next.js project.' : 'Disabled uses dotnet run: code changes require Restart. Enabled uses dotnet watch run and hot reload. Saving does not restart the process.';
}
async function openProjectForm(name = null) {
  try {
    await loadConfig();
    const project = name === null ? null : config.projects.find(item => item.name === name);
    if (name !== null && !project) throw new Error('This project no longer exists. Refresh the dashboard.');
    $('projectForm').reset(); editingProject = name; discoveredCandidates = [];
    populateGroups(config.projects); $('discoveryResult').hidden = true; $('projectError').textContent = '';
    $('discoveryStatus').textContent = 'Read project settings from a server path, or enter the fields manually.';
    $('projectTitle').textContent = project ? 'Edit project' : 'Add project';
    $('projectSave').textContent = project ? 'Save changes' : 'Save project';
    $('projectHelp').textContent = project ? 'Editing saves dashboard settings. With Watch files configured, Restart applies the saved launch settings. Renaming does not rename existing PM2 records. Saving keeps existing processes running.' : 'Saving a project does not start it. PM2 runs the command in the selected directory.';
    if (project) fillProjectForm(project); else { $('projectGroup').value = lastProjectGroup; $('projectWatch').checked = false; updateWatchControl(); }
    $('projectDialog').showModal();
  } catch (error) { notice(error.message); }
}
function applyDiscoveredProject() {
  const candidate = discoveredCandidates[Number($('discoveryTarget').value)]; if (!candidate) return;
  fillProjectForm(candidate.project, false);
  $('discoveryInfo').textContent = [candidate.source, candidate.portSource ? 'Port source: ' + candidate.portSource : 'Port not found; enter it manually.', ...(candidate.warnings || [])].join('\n');
}
async function discoverProject() {
  if ($('discover').disabled) return;
  $('discover').disabled = true; $('discoveryStatus').textContent = 'Reading project files…'; $('discoveryResult').hidden = true; discoveredCandidates = [];
  try {
    const data = await api('discover', 'POST', {path: $('sourcePath').value.trim()}); discoveredCandidates = data.candidates || [];
    $('discoveryTarget').replaceChildren();
    for (const [index, candidate] of discoveredCandidates.entries()) { const option = node('option', '', candidate.label); option.value = String(index); $('discoveryTarget').append(option); }
    $('discoveryResult').hidden = !discoveredCandidates.length;
    $('discoveryStatus').textContent = [discoveredCandidates.length + ' options found. Review the fields before saving.', ...(data.warnings || [])].join('\n');
    if (discoveredCandidates.length) { $('discoveryTarget').value = '0'; applyDiscoveredProject(); }
  } catch (error) { $('discoveryStatus').textContent = error.message; }
  finally { $('discover').disabled = false; }
}
async function browseDirectoryAt(path = '') {
  if (browseLoading) return false;
  browseLoading = true; $('browseSelect').disabled = true; $('browseStatus').textContent = 'Loading directory…';
  $('browseEntries').replaceChildren();
  try {
    const data = await api('browse?path=' + encodeURIComponent(path) + '&hidden=' + $('browseHidden').checked);
    browseDirectory = data.path; browseParent = data.parent; lastBrowsePath = data.path;
    try { localStorage.setItem('pm2-last-directory', lastBrowsePath); } catch (_) {}
    $('browsePath').value = data.path; $('browseUp').disabled = data.parent === data.path;
    const entries = data.entries.filter(entry => browseTarget === 'sourcePath' || entry.directory);
    for (const entry of entries) {
      const item = button('', () => entry.directory ? browseDirectoryAt(entry.path) : selectBrowsePath(entry.path));
      item.className = 'browse-entry'; item.append(node('span', '', entry.directory ? 'Folder' : 'File'), node('strong', '', entry.name));
      $('browseEntries').append(item);
    }
    $('browseStatus').textContent = (entries.length ? 'Open a folder' + (browseTarget === 'sourcePath' ? ', choose a project file,' : '') + ' or select the current directory.' : 'No matching entries in this directory.') + (data.truncated ? '\nOnly the first 5,000 entries were inspected. Enter a narrower directory path above.' : '');
    $('browseSelect').disabled = false;
    return true;
  } catch (error) { $('browseStatus').textContent = error.message; return false; }
  finally { browseLoading = false; }
}
async function openServerBrowser(target) {
  if (browseLoading) return;
  browseTarget = target; browseDirectory = ''; browseParent = '';
  $('browseTitle').textContent = target === 'projectCwd' ? 'Select working directory' : 'Select project file or directory';
  $('browseDialog').showModal();
  // A saved directory may have been removed or become inaccessible.
  if (!await browseDirectoryAt(lastBrowsePath) && lastBrowsePath && !authRequired) await browseDirectoryAt('');
}
function selectBrowsePath(path) {
  if (browseLoading || !path) return;
  $(browseTarget).value = path; $('browseDialog').close();
}
async function saveProject(event) {
  event.preventDefault(); if ($('projectSave').disabled) return;
  $('projectSave').disabled = true; $('projectError').textContent = '';
  const originalName = editingProject;
  try {
    const fields = new FormData(event.target), project = Object.fromEntries(fields.entries());
    if (!$('projectWatch').disabled) project.watchFiles = $('projectWatch').checked;
    project.port = Number(project.port) || 0; project.args = JSON.parse(project.args); project.env = JSON.parse(project.env);
    const body = {project}; if (originalName !== null) body.name = originalName;
    config = await api('project', originalName === null ? 'POST' : 'PUT', body);
    lastProjectGroup = project.group; $('projectDialog').close(); editingProject = null;
    notice(originalName === null ? 'Project saved. Use Start to run it.' : 'Project settings saved. Use Restart to apply a configured watch mode and launch settings.'); await refresh();
  } catch (error) { $('projectError').textContent = error.message; }
  finally { $('projectSave').disabled = false; }
}
function confirmProjectDeletion(name) {
  deletingProject = name; $('deleteName').textContent = name; $('deleteError').textContent = ''; $('deleteDialog').showModal();
}
async function deleteProject() {
  if (deletingProject === null || $('confirmDelete').disabled) return;
  const name = deletingProject; $('confirmDelete').disabled = true; $('deleteError').textContent = '';
  try {
    config = await api('project', 'DELETE', {name}); deletingProject = null; $('deleteDialog').close();
    notice('Project removed from configuration. Existing PM2 processes are now unmanaged.'); await refresh();
  } catch (error) { $('deleteError').textContent = error.message; }
  finally { $('confirmDelete').disabled = false; }
}
async function logs(project) {
  logsManaged = project.managed; $('clearLogs').disabled = !logsManaged || clearingLogs;
  $('logTitle').textContent = project.name + ' · Logs'; $('logInstance').replaceChildren();
  for (const process of project.instances) { const option = node('option', '', `PM2 #${process.pm_id} · PID ${process.pid}`); option.value = process.pm_id; $('logInstance').append(option); }
  if (project.instances.length) $('logInstance').value = String(project.instances[0].pm_id);
  $('logsDialog').showModal(); await refreshLogs();
}
async function refreshLogs() {
  const version = ++logRequestVersion;
  try {
    const data = await api('logs?id=' + encodeURIComponent($('logInstance').value));
    if (version !== logRequestVersion) return;
    $('stdout').textContent = data.stdout || '(empty)'; $('stderr').textContent = data.stderr || '(empty)'; $('logWarnings').textContent = data.warnings.join('\n');
  } catch (error) { if (version === logRequestVersion) $('logWarnings').textContent = error.message; }
}
async function clearLogs() {
  if (!logsManaged || clearingLogs) return;
  const id = Number($('logInstance').value);
  if (!Number.isInteger(id) || id < 0) return;
  if (!confirm('Permanently clear the stdout and stderr logs for PM2 #' + id + '? The process will keep running.')) return;
  clearingLogs = true; ++logRequestVersion;
  $('clearLogs').disabled = true; $('refreshLogs').disabled = true; $('logInstance').disabled = true;
  try {
    await api('logs', 'DELETE', {id}); await refreshLogs();
    $('logWarnings').textContent = ['Logs cleared. New output may appear immediately.', $('logWarnings').textContent].filter(Boolean).join('\n');
  } catch (error) { $('logWarnings').textContent = error.message; }
  finally { clearingLogs = false; $('clearLogs').disabled = !logsManaged; $('refreshLogs').disabled = false; $('logInstance').disabled = false; }
}
function showServiceStatus(data) {
  $('serviceStatus').textContent = data.available ? (data.runningAsService ? 'This dashboard is running as a user service.' : data.active ? 'A user service is already active.' : 'This dashboard is running in the foreground.') + ' Autostart: ' + (data.enabled ? 'enabled.' : 'disabled.') : 'User service unavailable.';
  $('serviceHint').textContent = [data.unitPath ? 'Unit: ' + data.unitPath : '', data.hint || ''].filter(Boolean).join('\n');
  $('serviceError').textContent = data.error || '';
  $('enableService').textContent = data.runningAsService ? 'Enable autostart' : 'Run as service';
  $('enableService').disabled = !data.available || serviceBusy || serviceSwitching || (data.runningAsService && data.enabled) || (data.active && !data.runningAsService);
  $('disableService').disabled = !data.enabled || serviceBusy || serviceSwitching;
}
async function loadServiceStatus() {
  try { showServiceStatus(await api('service')); }
  catch (error) { $('serviceError').textContent = error.message; $('enableService').disabled = true; $('disableService').disabled = true; }
}
async function serviceAction(action) {
  if (serviceBusy || serviceSwitching) return;
  let actionError = '';
  serviceBusy = true; $('enableService').disabled = true; $('disableService').disabled = true; $('refreshService').disabled = true; $('serviceError').textContent = '';
  try {
    const data = await api('service', 'POST', {action});
    if (data.handoff) {
      serviceSwitching = true; $('serviceStatus').textContent = 'Switching to the user service. Waiting for the dashboard to reconnect…';
      setTimeout(() => checkServiceHandoff(0), 1000);
    } else { notice(data.message); showServiceStatus(data.status); }
  } catch (error) { actionError = error.message; }
  finally { serviceBusy = false; $('refreshService').disabled = false; if (!serviceSwitching) await loadServiceStatus(); if (actionError) $('serviceError').textContent = actionError; }
}
async function checkServiceHandoff(attempt) {
  try {
    const data = await api('service');
    if (data.runningAsService) { serviceSwitching = false; showServiceStatus(data); notice('Dashboard is now running as a user service.'); await refresh(); return; }
  } catch (_) {}
  if (attempt < 19) { setTimeout(() => checkServiceHandoff(attempt + 1), 1000); return; }
  serviceSwitching = false; $('serviceError').textContent = 'Could not verify the service handover. Check journalctl --user -u simple-pm2-monitor, then refresh service status.';
}
for (const element of document.querySelectorAll('[data-close]')) element.addEventListener('click', () => $(element.dataset.close).close());
$('refresh').addEventListener('click', refresh); $('search').addEventListener('input', render); $('running').addEventListener('change', render);
$('add').addEventListener('click', () => openProjectForm()); $('projectForm').addEventListener('submit', saveProject);
$('projectCommand').addEventListener('input', () => updateWatchControl());
$('projectArgs').addEventListener('input', () => updateWatchControl());
$('discover').addEventListener('click', discoverProject); $('discoveryTarget').addEventListener('change', applyDiscoveredProject);
$('browseSource').addEventListener('click', () => openServerBrowser('sourcePath'));
$('browseCwd').addEventListener('click', () => openServerBrowser('projectCwd'));
$('browseForm').addEventListener('submit', event => { event.preventDefault(); return browseDirectoryAt($('browsePath').value.trim()); });
$('browseHome').addEventListener('click', () => browseDirectoryAt(''));
$('browseUp').addEventListener('click', () => browseParent && browseDirectoryAt(browseParent));
$('browseHidden').addEventListener('change', () => browseDirectoryAt(browseDirectory));
$('browseSelect').addEventListener('click', () => selectBrowsePath(browseDirectory));
$('sourcePath').addEventListener('keydown', event => { if (event.key === 'Enter') { event.preventDefault(); discoverProject(); } });
$('confirmDelete').addEventListener('click', deleteProject);
$('config').addEventListener('click', async () => { try { $('configText').value = JSON.stringify(await loadConfig(), null, 2); $('configDialog').showModal(); await loadServiceStatus(); } catch (error) { notice(error.message); } });
$('enableService').addEventListener('click', () => serviceAction('enable'));
$('disableService').addEventListener('click', () => serviceAction('disable'));
$('refreshService').addEventListener('click', loadServiceStatus);
$('saveConfig').addEventListener('click', async () => { try { config = await api('config', 'PUT', JSON.parse($('configText').value)); $('configDialog').close(); notice('Configuration saved.'); await refresh(); } catch (error) { alert(error.message); } });
$('auth').addEventListener('click', () => $('authDialog').showModal());
$('authForm').addEventListener('submit', async event => {
  event.preventDefault(); const bytes = new TextEncoder().encode($('username').value + ':' + $('password').value);
  sessionStorage.setItem('pm2-authorization', 'Basic ' + btoa(Array.from(bytes, byte => String.fromCharCode(byte)).join('')));
  $('password').value = ''; $('authError').textContent = ''; authRequired = false; $('authDialog').close(); notice(''); await refresh();
});
$('refreshLogs').addEventListener('click', refreshLogs); $('logInstance').addEventListener('change', refreshLogs);
$('clearLogs').addEventListener('click', clearLogs);
refresh(); setInterval(refresh, 5000);
