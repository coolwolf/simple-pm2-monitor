const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

class Element {
  constructor() { this.value=''; this.checked=false; this.open=false; this.listeners={}; this.children=[]; }
  addEventListener(type,handler) { this.listeners[type]=handler; }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children=children; }
  showModal() { this.open=true; }
  close() { this.open=false; }
  reset() {}
}
function harness(initialStorage={}, responses={}) {
  const elements=new Map(), storage=new Map(Object.entries(initialStorage)), requests=[];
  const get=id=>{if(!elements.has(id))elements.set(id,new Element());return elements.get(id);};
  const expectedBasic='Basic '+Buffer.from('admin\u2603:password\u2603:123').toString('base64');
  const context=vm.createContext({
    document:{getElementById:get,createElement:()=>new Element(),querySelectorAll:()=>[]},
    sessionStorage:{getItem:key=>storage.get(key),setItem:(key,value)=>storage.set(key,value),removeItem:key=>storage.delete(key)},
    localStorage:{getItem:key=>storage.get(key),setItem:(key,value)=>storage.set(key,value)},
    FormData:class { entries(){return Object.entries({name:get('projectName').value,group:get('projectGroup').value,cwd:get('projectCwd').value,command:get('projectCommand').value,port:String(get('projectPort').value),args:get('projectArgs').value,env:get('projectEnv').value,metricsUrl:get('projectMetricsURL').value,requestMetric:get('projectRequestMetric').value});} },
    URL,window:{location:{hostname:'dev.example'}},
    TextEncoder,btoa:value=>Buffer.from(value,'binary').toString('base64'),setInterval:()=>{},setTimeout:()=>0,
    fetch:async(path,options)=>{
      requests.push({path,...options});
      const authorized=options.headers.Authorization===expectedBasic;
      return {ok:authorized,status:authorized?200:401,json:async()=>authorized?(typeof responses[path]==='function'?responses[path](options):(responses[path]||{projects:[],updatedAt:'2026-10-10T10:00:00Z'})):{error:'login required'}};
    },
    confirm:()=>false,
    alert:message=>{throw new Error(message);}
  });
  vm.runInContext(fs.readFileSync(require('node:path').join(__dirname,'../web/app.js'),'utf8'),context);
  return {get,storage,requests,expectedBasic,context};
}
const flush=()=>new Promise(resolve=>setImmediate(resolve));

test('admin login prompts after 401 and sends Unicode username/password correctly',async()=>{
  const h=harness();await flush();assert.equal(h.get('authDialog').open,true);
  h.get('username').value='admin\u2603';h.get('password').value='password\u2603:123';
  await h.get('authForm').listeners.submit({preventDefault(){}});
  assert.equal(h.requests.at(-1).headers.Authorization,h.expectedBasic);
  assert.equal(h.storage.get('pm2-authorization'),h.expectedBasic);
  assert.equal(h.get('password').value,'');assert.equal(h.get('authDialog').open,false);
  h.get('password').value='wrong';await h.get('authForm').listeners.submit({preventDefault(){}});
  assert.equal(h.get('authDialog').open,true);
});

test('previously stored Bearer authorization is not sent',async()=>{
  const h=harness({'pm2-authorization':'Bearer old-secret'});await flush();
  assert.equal(h.requests[0].headers.Authorization,undefined);
  assert.equal(h.get('authDialog').open,true);
  h.get('username').value='admin\u2603';h.get('password').value='password\u2603:123';
  await h.get('authForm').listeners.submit({preventDefault(){}});
  assert.equal(h.storage.get('pm2-authorization'),h.expectedBasic);
});


test('project form offers unique saved groups and discovery preserves the chosen group',async()=>{
  const authorization='Basic '+Buffer.from('admin\u2603:password\u2603:123').toString('base64');
  const makeCandidate=(label,cwd,port,args)=>({label,source:cwd+'/package.json',portSource:'script',warnings:[],project:{name:'shop-web',cwd,command:'/usr/bin/npm',port,args,env:{MODE:'dev'}}});
  const candidates=[makeCandidate('dev','/srv/web',4100,['run','dev']),makeCandidate('start','/srv/web',4200,['run','start'])];
  const h=harness({'pm2-authorization':authorization},{
    '/api/config':{adminUsername:'owner',adminPassword:'secret',projects:[{group:'shop'},{group:'core'},{group:'shop'},{group:''}]},
    '/api/discover':{candidates,warnings:[]}
  });await flush();
  await h.get('add').listeners.click();assert.equal(h.get('projectDialog').open,true);
  assert.deepEqual(h.get('groupOptions').children.map(option=>option.value),['core','shop']);
  h.get('projectGroup').value='custom-new-group';h.get('sourcePath').value='/srv/web';
  await h.get('discover').listeners.click();
  assert.equal(h.requests.at(-1).path,'/api/discover');assert.deepEqual(JSON.parse(h.requests.at(-1).body),{path:'/srv/web'});
  assert.equal(h.get('discoveryResult').hidden,false);assert.equal(h.get('discoveryTarget').children.length,2);
  assert.equal(h.get('projectCwd').value,'/srv/web');assert.equal(h.get('projectPort').value,4100);
  assert.equal(h.get('projectGroup').value,'custom-new-group');
  h.get('discoveryTarget').value='1';h.get('discoveryTarget').listeners.change();
  assert.equal(h.get('projectPort').value,4200);assert.deepEqual(JSON.parse(h.get('projectArgs').value),['run','start']);
  assert.deepEqual(JSON.parse(h.get('projectEnv').value),{MODE:'dev'});assert.equal(h.get('projectGroup').value,'custom-new-group');
});


test('editing loads every field and updates the original project instead of adding a duplicate',async()=>{
  const authorization='Basic '+Buffer.from('admin\u2603:password\u2603:123').toString('base64');
  const saved={name:'api',group:'shop',cwd:'/srv/api',command:'/usr/bin/dotnet',args:['run'],env:{MODE:'dev'},port:5001,metricsUrl:'http://localhost:5001/metrics',requestMetric:'requests_total'};
  const config={adminUsername:'owner',adminPassword:'secret',listen:'127.0.0.1:8989',projects:[saved]};
  const h=harness({'pm2-authorization':authorization},{'/api/config':config,'/api/project':options=>{
    const data=JSON.parse(options.body);return {...config,projects:[data.project]};
  }});await flush();
  await vm.runInContext("openProjectForm('api')",h.context);
  assert.equal(h.get('projectTitle').textContent,'Edit project');assert.equal(h.get('projectGroup').value,'shop');
  assert.equal(h.get('projectMetricsURL').value,saved.metricsUrl);assert.equal(h.get('projectRequestMetric').value,'requests_total');
  h.get('projectName').value='renamed-api';h.get('projectPort').value='6001';
  await h.get('projectForm').listeners.submit({preventDefault(){},target:h.get('projectForm')});
  const request=h.requests.find(item=>item.path==='/api/project');assert.equal(request.method,'PUT');
  const data=JSON.parse(request.body);assert.equal(data.name,'api');assert.equal(data.project.name,'renamed-api');assert.equal(data.project.port,6001);
  assert.deepEqual(data.project.args,['run']);assert.equal(h.get('projectDialog').open,false);
  await h.get('add').listeners.click();assert.equal(h.get('projectTitle').textContent,'Add project');
  assert.equal(h.get('projectGroup').value,'shop');
});

test('deletion is confirmed before issuing a request and targets only the selected project',async()=>{
  const authorization='Basic '+Buffer.from('admin\u2603:password\u2603:123').toString('base64');
  const h=harness({'pm2-authorization':authorization},{'/api/project':{projects:[]}});await flush();
  vm.runInContext("confirmProjectDeletion('api')",h.context);
  assert.equal(h.get('deleteDialog').open,true);assert.equal(h.get('deleteName').textContent,'api');
  assert.equal(h.requests.some(item=>item.path==='/api/project'),false);
  h.get('deleteDialog').close();assert.equal(h.requests.some(item=>item.path==='/api/project'),false);
  vm.runInContext("confirmProjectDeletion('api')",h.context);
  await h.get('confirmDelete').listeners.click();
  const request=h.requests.find(item=>item.path==='/api/project');assert.equal(request.method,'DELETE');
  assert.deepEqual(JSON.parse(request.body),{name:'api'});assert.equal(h.get('deleteDialog').open,false);
});


test('server picker remembers navigation and selects files or directories in the correct field',async()=>{
  const authorization='Basic '+Buffer.from('admin\u2603:password\u2603:123').toString('base64');
  const home={path:'/home/dev',parent:'/home',entries:[{name:'app',path:'/home/dev/app',directory:true},{name:'Shop.sln',path:'/home/dev/Shop.sln',directory:false}]};
  const app={path:'/home/dev/app',parent:'/home/dev',entries:[{name:'package.json',path:'/home/dev/app/package.json',directory:false}]};
  const h=harness({'pm2-authorization':authorization},{'/api/browse?path=&hidden=false':home,'/api/browse?path=%2Fhome%2Fdev%2Fapp&hidden=false':app});await flush();
  await h.get('browseSource').listeners.click();assert.equal(h.get('browseDialog').open,true);
  assert.equal(h.get('browseEntries').children.length,2);
  await h.get('browseEntries').children[0].listeners.click();
  assert.equal(h.storage.get('pm2-last-directory'),app.path);
  h.get('browseEntries').children[0].listeners.click();
  assert.equal(h.get('sourcePath').value,'/home/dev/app/package.json');assert.equal(h.get('browseDialog').open,false);
  await h.get('browseCwd').listeners.click();assert.equal(h.get('browsePath').value,app.path);
  assert.equal(h.get('browseEntries').children.length,0);
  h.get('browseSelect').listeners.click();assert.equal(h.get('projectCwd').value,app.path);
  assert.equal(h.requests.filter(item=>item.path==='/api/discover').length,0);
});


test('server picker restores the saved directory across browser sessions and supports home, parent, and hidden entries',async()=>{
  const authorization='Basic '+Buffer.from('admin\u2603:password\u2603:123').toString('base64');
  const home={path:'/home/dev',parent:'/home',entries:[]};
  const saved={path:'/srv/app',parent:'/srv',entries:[]};
  const parent={path:'/srv',parent:'/',entries:[]};
  const h=harness({'pm2-authorization':authorization,'pm2-last-directory':saved.path},{'/api/browse?path=%2Fsrv%2Fapp&hidden=false':saved,'/api/browse?path=%2Fsrv&hidden=false':parent,'/api/browse?path=&hidden=false':home,'/api/browse?path=%2Fhome%2Fdev&hidden=true':home});await flush();
  await h.get('browseCwd').listeners.click();assert.equal(h.get('browsePath').value,saved.path);
  await h.get('browseUp').listeners.click();assert.equal(h.get('browsePath').value,parent.path);
  await h.get('browseHome').listeners.click();assert.equal(h.get('browsePath').value,home.path);
  h.get('browseHidden').checked=true;await h.get('browseHidden').listeners.change();
  assert.equal(h.requests.at(-1).path,'/api/browse?path=%2Fhome%2Fdev&hidden=true');
});

test('all statically referenced UI controls exist in the actual HTML',()=>{
  const root=require('node:path').join(__dirname,'../web');
  const source=fs.readFileSync(root+'/app.js','utf8'),html=fs.readFileSync(root+'/index.html','utf8');
  for(const match of source.matchAll(/\$\('([^']+)'\)/g)) assert.ok(html.includes('id="'+match[1]+'"'),'Missing HTML control: '+match[1]);
});

test('watch mode is inferred for old projects, can be disabled, and is sent when saving',async()=>{
  const authorization='Basic '+Buffer.from('admin\u2603:password\u2603:123').toString('base64');
  const project={name:'api',group:'shop',cwd:'/srv/api',command:'/usr/bin/dotnet',args:['watch','run'],env:{},port:5181};
  const h=harness({'pm2-authorization':authorization},{'/api/config':{projects:[project]},'/api/project':{projects:[project]}});await flush();
  await vm.runInContext("openProjectForm('api')",h.context);
  assert.equal(h.get('projectWatch').checked,true);assert.equal(h.get('watchLabel').hidden,false);
  h.get('projectWatch').checked=false;
  await h.get('projectForm').listeners.submit({preventDefault(){},target:h.get('projectForm')});
  const request=h.requests.find(item=>item.path==='/api/project');
  assert.equal(JSON.parse(request.body).project.watchFiles,false);
  h.get('projectCommand').value='/usr/bin/npm';h.get('projectArgs').value='["run","start"]';
  h.get('projectCommand').listeners.input();
  assert.match(h.get('watchHelp').textContent,/npm run build/);
});


test('Clear Logs requires confirmation and clears only the selected managed instance before refreshing',async()=>{
  const authorization='Basic '+Buffer.from('admin\u2603:password\u2603:123').toString('base64');
  const h=harness({'pm2-authorization':authorization},{'/api/logs?id=0':{stdout:'',stderr:'',warnings:[]},'/api/logs':{id:0}});await flush();
  await vm.runInContext("logs({name:'api',managed:true,instances:[{pm_id:0,pid:42}]})",h.context);
  await h.get('clearLogs').listeners.click();
  assert.equal(h.requests.filter(item=>item.method==='DELETE').length,0);
  h.context.confirm=()=>true;
  await h.get('clearLogs').listeners.click();
  const request=h.requests.find(item=>item.method==='DELETE');
  assert.equal(request.path,'/api/logs');assert.deepEqual(JSON.parse(request.body),{id:0});
  assert.equal(h.requests.at(-1).path,'/api/logs?id=0');assert.equal(h.get('stdout').textContent,'(empty)');
  assert.match(h.get('logWarnings').textContent,/Logs cleared/);assert.equal(h.get('clearLogs').disabled,false);
  await vm.runInContext("logs({name:'other',managed:false,instances:[{pm_id:0,pid:42}]})",h.context);
  assert.equal(h.get('clearLogs').disabled,true);
  await h.get('clearLogs').listeners.click();assert.equal(h.requests.filter(item=>item.method==='DELETE').length,1);
});


test('project port links use the dashboard hostname for IPv4, IPv6, and forwarded localhost',()=>{
  const h=harness();
  for(const host of ['192.168.1.10','dev.example','[2001:db8::1]','localhost']) {
    h.context.window.location.hostname=host;
    const cell=vm.runInContext('projectPortCell(5180)',h.context),link=cell.children[0];
    const url=new URL(link.href);
    assert.equal(url.hostname,host);assert.equal(url.port,'5180');assert.equal(url.protocol,'http:');assert.equal(url.pathname,'/');
    assert.equal(link.target,'_blank');assert.equal(link.rel,'noopener noreferrer');assert.equal(link.textContent,'5180');
  }
  const missing=vm.runInContext('projectPortCell(0)',h.context);
  assert.equal(missing.textContent,'—');assert.equal(missing.children.length,0);
});


test('Configuration exposes service status, enables handover, and preserves installation errors',async()=>{
  const authorization='Basic '+Buffer.from('admin\u2603:password\u2603:123').toString('base64');
  const status={available:true,installed:false,enabled:false,active:false,runningAsService:false,hint:'Enable linger for boot startup.'};
  const h=harness({'pm2-authorization':authorization},{'/api/config':{projects:[]},'/api/service':options=>options.method==='POST'?{handoff:true,status,message:'Enabled'}:status});await flush();
  await h.get('config').listeners.click();assert.equal(h.get('configDialog').open,true);
  assert.equal(h.get('enableService').disabled,false);assert.equal(h.get('disableService').disabled,true);
  await h.get('enableService').listeners.click();
  const request=h.requests.find(item=>item.path==='/api/service'&&item.method==='POST');
  assert.deepEqual(JSON.parse(request.body),{action:'enable'});
  assert.match(h.get('serviceStatus').textContent,/Switching/);assert.equal(h.get('enableService').disabled,true);
  const failed=harness({'pm2-authorization':authorization},{'/api/service':options=>{if(options.method==='POST')throw new Error('Cannot enable service');return status;}});await flush();
  await failed.get('enableService').listeners.click();assert.match(failed.get('serviceError').textContent,/Cannot enable service/);
});

test('Disable autostart keeps the running dashboard available',async()=>{
  const authorization='Basic '+Buffer.from('admin\u2603:password\u2603:123').toString('base64');
  let enabled=true;
  const h=harness({'pm2-authorization':authorization},{'/api/service':options=>{
    if(options.method==='POST')enabled=false;
    const status={available:true,installed:true,enabled,active:true,runningAsService:true};
    return options.method==='POST'?{handoff:false,status,message:'Autostart disabled.'}:status;
  }});await flush();
  await h.get('refreshService').listeners.click();assert.equal(h.get('disableService').disabled,false);
  await h.get('disableService').listeners.click();
  const request=h.requests.find(item=>item.method==='POST');assert.deepEqual(JSON.parse(request.body),{action:'disable'});
  assert.match(h.get('serviceStatus').textContent,/running as a user service/);
  assert.equal(h.get('enableService').disabled,false);assert.equal(h.get('disableService').disabled,true);
});
