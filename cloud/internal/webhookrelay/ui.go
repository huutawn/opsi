package webhookrelay

import "net/http"

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("content-type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(uiHTML))
}

const uiHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Opsi</title>
<style>
:root{color-scheme:light;--ink:#17212b;--muted:#667085;--line:#d8dee6;--soft:#f5f7f9;--panel:#fff;--ok:#12715b;--warn:#9a5b00;--bad:#b42318;--blue:#1f5eff;--teal:#0f766e;--violet:#6d3dcc}
*{box-sizing:border-box}body{margin:0;font:14px/1.45 Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:var(--ink);background:#eef2f5;letter-spacing:0}button,input,select,textarea{font:inherit}button{border:1px solid var(--line);background:#fff;border-radius:6px;padding:8px 11px;cursor:pointer;min-height:36px}button.primary{border-color:#174de8;background:var(--blue);color:white}button.danger{border-color:#f0b8b3;color:var(--bad)}button:disabled{opacity:.45;cursor:not-allowed}.app{display:grid;grid-template-columns:232px 1fr;min-height:100vh}.side{background:#101820;color:#d9e3ee;padding:18px 14px;position:sticky;top:0;height:100vh}.brand{font-size:22px;font-weight:750;margin:2px 6px 24px}.nav-title{margin:18px 8px 7px;color:#91a2b6;font-size:11px;text-transform:uppercase}.nav button{width:100%;text-align:left;background:transparent;color:#d9e3ee;border:0;padding:8px;border-radius:6px}.nav button.active{background:#213140}.main{padding:20px;min-width:0}.top{display:flex;align-items:center;gap:10px;margin-bottom:14px;flex-wrap:wrap}.top input{height:36px;border:1px solid var(--line);border-radius:6px;padding:7px 9px;background:#fff}.top .grow{flex:1;min-width:180px}.grid{display:grid;gap:12px}.cols{grid-template-columns:1.1fr .9fr}.panel{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:14px;min-width:0}.panel h2,.panel h3{margin:0 0 10px;font-size:16px}.hero{display:grid;grid-template-columns:1fr auto;gap:12px;align-items:start}.status{display:inline-flex;align-items:center;gap:6px;padding:3px 8px;border-radius:999px;background:var(--soft);font-size:12px;color:var(--muted)}.status.ok{background:#e9f7f3;color:var(--ok)}.status.warn{background:#fff4df;color:var(--warn)}.status.bad{background:#fff0ee;color:var(--bad)}.metrics{display:grid;grid-template-columns:repeat(4,minmax(100px,1fr));gap:10px}.metric{border:1px solid var(--line);border-radius:8px;padding:10px;background:#fbfcfd}.metric b{display:block;font-size:20px}.muted{color:var(--muted)}.empty{border:1px dashed #b8c2cc;border-radius:8px;padding:22px;text-align:center;background:#fbfcfd}.table{width:100%;border-collapse:collapse}.table th,.table td{padding:8px;border-bottom:1px solid var(--line);text-align:left;vertical-align:top}.table th{font-size:12px;color:var(--muted);font-weight:600}.forms{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px}.forms label{display:grid;gap:5px;color:var(--muted);font-size:12px}.forms input,.forms select,.forms textarea{width:100%;border:1px solid var(--line);border-radius:6px;padding:8px;background:#fff;color:var(--ink);min-width:0}.forms textarea{min-height:74px;resize:vertical}.span2{grid-column:1/-1}.timeline{display:grid;gap:7px}.event{display:grid;grid-template-columns:44px 1fr auto;gap:8px;align-items:center;border-bottom:1px solid var(--line);padding:7px 0}.bar{height:8px;background:#edf1f5;border-radius:999px;overflow:hidden}.bar i{display:block;height:100%;background:var(--teal)}.topology{min-height:260px;position:relative;overflow:auto;background:linear-gradient(#fff,#f8fafb);border:1px solid var(--line);border-radius:8px;padding:14px}.topo-row{display:flex;gap:14px;align-items:stretch;min-width:760px}.topo-col{display:grid;gap:8px;min-width:170px}.node{border:1px solid var(--line);border-radius:8px;padding:10px;background:#fff}.edge{align-self:center;color:#98a2b3}.toast{position:fixed;right:18px;bottom:18px;max-width:420px;background:#111827;color:white;border-radius:8px;padding:12px;box-shadow:0 14px 40px #0004;display:none}.toast.show{display:block}.hidden{display:none!important}@media(max-width:900px){.app{grid-template-columns:1fr}.side{height:auto;position:relative}.cols,.forms{grid-template-columns:1fr}.metrics{grid-template-columns:repeat(2,minmax(0,1fr))}.hero{grid-template-columns:1fr}}
</style>
</head>
<body>
<div class="app">
<aside class="side">
<div class="brand">Opsi</div>
<nav class="nav">
<div class="nav-title">Setup</div><button data-view="projects" class="active">Projects</button><button data-view="servers">Servers / Nodes</button>
<div class="nav-title">Operate</div><button data-view="overview">Overview</button><button data-view="services">Services</button><button data-view="deployments">Deployments</button><button data-view="incidents">Incidents & RCA</button>
<div class="nav-title">Understand</div><button data-view="topology">Topology</button><button data-view="logs">Logs</button><button data-view="metrics">Metrics</button>
<div class="nav-title">Control</div><button data-view="secrets">Secrets</button><button data-view="audit">Audit</button><button data-view="settings">Settings</button>
</nav>
</aside>
<main class="main">
<div class="top">
<input id="org" value="org-1" aria-label="Org ID">
<input id="pat" class="grow" type="password" placeholder="PAT for secured Cloud" aria-label="PAT">
<select id="projectSelect" aria-label="Project"></select>
<button id="refresh">Refresh</button>
</div>
<section id="projects" class="view grid cols"></section>
<section id="overview" class="view grid cols hidden"></section>
<section id="servers" class="view grid hidden"></section>
<section id="services" class="view grid hidden"></section>
<section id="deployments" class="view grid hidden"></section>
<section id="topology" class="view grid hidden"></section>
<section id="audit" class="view grid hidden"></section>
<section id="secrets" class="view grid hidden"></section>
<section id="logs" class="view grid hidden"></section>
<section id="metrics" class="view grid hidden"></section>
<section id="incidents" class="view grid hidden"></section>
<section id="settings" class="view grid hidden"></section>
</main>
</div>
<div id="toast" class="toast"></div>
<script>
const state={projects:[],project:null,readiness:null,nodes:[],services:[],deployments:[],audit:[],sessions:[],boot:null,events:[],deployEvents:[],nodeDetail:null,serviceDetail:null,busy:false,view:"projects"};
const $=id=>document.getElementById(id);
const esc=v=>String(v??"").replace(/[&<>"']/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;"}[c]));
const rel=t=>t?new Date(t).toLocaleString():"-";
const idKey=p=>p+"-"+crypto.getRandomValues(new Uint32Array(2)).join("-");
function toast(msg){$("toast").textContent=msg;$("toast").classList.add("show");setTimeout(()=>$("toast").classList.remove("show"),4200)}
async function api(path,opt={}){
 const h={"content-type":"application/json","X-Request-ID":idKey("req")};
 const token=$("pat").value.trim(); if(token) h.Authorization="Bearer "+token;
 if(opt.write) h["Idempotency-Key"]=opt.key||idKey("idem");
 const res=await fetch(path,{method:opt.method||"GET",headers:h,body:opt.body?JSON.stringify(opt.body):undefined});
 const text=await res.text(); let data={}; try{data=text?JSON.parse(text):{};}catch{data={message:text};}
 if(!res.ok){throw Object.assign(new Error(data.message||data.error||data.error_code||"request failed"),{status:res.status,data});}
 return data;
}
async function load(){
 localStorage.setItem("opsi_org",$("org").value||"org-1");
 try{
  const list=await api("/api/orgs/"+encodeURIComponent($("org").value||"org-1")+"/projects");
  state.projects=list.projects||[];
  const selected=$("projectSelect").value||localStorage.getItem("opsi_project");
  state.project=state.projects.find(p=>p.id===selected)||state.projects[0]||null;
  fillProjects();
  if(state.project){
   localStorage.setItem("opsi_project",state.project.id);
   const pid=state.project.id;
   const [readiness,nodes,services,deployments,audit,sessions]=await Promise.all([
    api("/api/projects/"+pid+"/readiness"),
    api("/api/projects/"+pid+"/nodes"),
    api("/api/projects/"+pid+"/services"),
    api("/api/projects/"+pid+"/deployments"),
    api("/api/projects/"+pid+"/audit"),
    api("/api/projects/"+pid+"/bootstrap-sessions")
   ]);
   Object.assign(state,{readiness,nodes:nodes||[],services:services.services||[],deployments:deployments.deployments||[],audit:audit.events||[],sessions:sessions.sessions||[]});
   await reconnectStreams();
  }else Object.assign(state,{readiness:null,nodes:[],services:[],deployments:[],audit:[],sessions:[],events:[],deployEvents:[]});
  render();
 }catch(e){toast((e.data&&e.data.error_code?e.data.error_code+": ":"")+e.message); renderError(e);}
}
function fillProjects(){
 $("projectSelect").innerHTML=state.projects.map(p=>"<option value=\""+esc(p.id)+"\">"+esc(p.name)+"</option>").join("");
 if(state.project) $("projectSelect").value=state.project.id;
}
function switchView(name){state.view=name;document.querySelectorAll(".view").forEach(v=>v.classList.toggle("hidden",v.id!==name));document.querySelectorAll(".nav button").forEach(b=>b.classList.toggle("active",b.dataset.view===name));render();}
function statusClass(s){return s==="ready"||s==="healthy"||s==="succeeded"?"ok":(s==="blocked"||s==="failed"||s==="removed"||s==="cancelled"?"bad":"warn")}
function readinessPanel(){
 const r=state.readiness, p=state.project;
 if(!p) return "<div class=\"panel empty\"><h2>No projects</h2><p class=\"muted\">Create a project to start the production workflow.</p></div>";
 const msg=r?.status==="ready"?"Ready to deploy":r?.status==="bootstrapping"?"Bootstrap is running":"This project is not ready to deploy.";
 return "<div class=\"panel hero\"><div><h2>"+esc(p.name)+"</h2><p class=\"muted\">"+esc(msg)+"</p><span class=\"status "+statusClass(r?.status)+"\">"+esc(r?.status||p.status)+"</span></div><div><button class=\"primary\" onclick=\"switchView('servers')\">Add first server</button> <button onclick=\"switchView('services')\" "+(r?.can_deploy?"":"disabled")+">Deploy service</button></div></div>";
}
function metrics(){
 return "<div class=\"metrics\"><div class=\"metric\"><span class=\"muted\">Nodes</span><b>"+state.nodes.length+"</b></div><div class=\"metric\"><span class=\"muted\">Services</span><b>"+state.services.length+"</b></div><div class=\"metric\"><span class=\"muted\">Deployments</span><b>"+state.deployments.length+"</b></div><div class=\"metric\"><span class=\"muted\">Open incidents</span><b>0</b></div></div>";
}
function renderProjects(){
 $("projects").innerHTML="<div class=\"panel\"><h2>Projects</h2>"+projectForm()+projectTable()+"</div><div class=\"grid\">"+readinessPanel()+metrics()+"</div>";
}
function projectForm(){
 return "<form id=\"projectForm\" class=\"forms\"><label>Name<input name=\"name\" required></label><label>Slug<input name=\"slug\"></label><button class=\"primary span2\">Create project</button></form>";
}
function projectTable(){
 if(!state.projects.length) return "<div class=\"empty\">No projects yet</div>";
 return "<table class=\"table\"><thead><tr><th>Name</th><th>Environment</th><th>Readiness</th><th>Nodes</th><th>Services</th><th>Last deploy</th><th>Owner/team</th></tr></thead><tbody>"+state.projects.map(p=>"<tr><td>"+esc(p.name)+"</td><td>default</td><td><span class=\"status "+statusClass(p.status)+"\">"+esc(p.status)+"</span></td><td>"+(state.project?.id===p.id?state.nodes.length:"-")+"</td><td>"+(state.project?.id===p.id?state.services.length:"-")+"</td><td>"+esc(lastDeploy())+"</td><td>"+esc(p.created_by||p.org_id)+"</td></tr>").join("")+"</tbody></table>";
}
function renderOverview(){
 $("overview").innerHTML="<div class=\"grid\">"+readinessPanel()+metrics()+"</div><div class=\"panel\"><h2>Current Work</h2>"+deploymentsTable()+"</div>";
}
function renderServers(){
 $("servers").innerHTML="<div class=\"panel\"><h2>Servers / Nodes</h2>"+serverForm()+"</div><div class=\"panel\"><h2>Node List</h2>"+nodesTable()+"</div>"+nodeDetail()+"<div class=\"panel\"><h2>Bootstrap Timeline</h2>"+timeline()+"</div>";
}
function serverForm(){
 const canWorker=state.readiness?.can_deploy;
 return "<form id=\"serverForm\" class=\"forms\"><label>Server name<input name=\"name\"></label><label>Role<select name=\"role\"><option value=\"first_server\">First server</option><option value=\"worker\" "+(canWorker?"":"disabled")+">Worker</option></select></label><label>Public host<input name=\"public_host\" required></label><label>SSH port<input name=\"ssh_port\" type=\"number\" value=\"22\"></label><label>SSH username<input name=\"ssh_username\" value=\"root\"></label><label>Auth method<select name=\"auth_method\"><option value=\"password\">Password</option><option value=\"private_key\">Private key</option></select></label><label class=\"span2\">Private key/password<textarea name=\"secret\" autocomplete=\"off\"></textarea></label><p class=\"muted span2\">SSH credential is submitted once for bootstrap, never saved in browser, then discarded by Opsi after worker handoff.</p><button class=\"primary span2\" "+(state.busy?"disabled":"")+">"+(state.busy?"Starting...":"Start preflight and install")+"</button></form>";
}
function nodesTable(){
 if(!state.nodes.length) return "<div class=\"empty\"><h3>No servers connected</h3><p class=\"muted\">Add a VPS/server. Opsi will use SSH only during bootstrap, install K3s and the Agent, then discard the SSH credential.</p></div>";
 return "<table class=\"table\"><thead><tr><th>Name</th><th>Role</th><th>Status</th><th>Public host</th><th>Provider/region</th><th>CPU</th><th>Memory</th><th>Disk</th><th>K3s</th><th>Agent</th><th>Last seen</th><th>Actions</th></tr></thead><tbody>"+state.nodes.map(n=>"<tr><td>"+esc(n.name)+"</td><td>"+esc(n.role)+"</td><td><span class=\"status "+statusClass(n.status)+"\">"+esc(n.status)+"</span></td><td>"+esc(n.public_host)+"</td><td>"+esc([n.provider,n.region].filter(Boolean).join(' / ')||'-')+"</td><td>"+esc(n.cpu_cores||"-")+"</td><td>"+esc(n.memory_mb||"-")+"</td><td>"+esc(n.disk_total_gb||"-")+"</td><td>"+esc(n.k3s_status||n.k3s_role||"-")+"</td><td>"+esc(n.agent_version||n.agent_id||"-")+"</td><td>"+esc(rel(n.last_seen_at))+"</td><td><button onclick=\"diagnostics('"+esc(n.id)+"')\">Details</button> <button onclick=\"drain('"+esc(n.id)+"')\" "+(n.status==="removed"?"disabled":"")+">Drain</button> <button class=\"danger\" onclick=\"removeNode('"+esc(n.id)+"')\" "+(n.status==="removed"?"disabled":"")+">Remove</button></td></tr>").join("")+"</tbody></table>";
}
function nodeDetail(){
 const d=state.nodeDetail; if(!d) return "";
 const n=d.node||{};
 return "<div class=\"panel\"><h2>Node Detail</h2><div class=\"metrics\"><div class=\"metric\"><span class=\"muted\">Health</span><b>"+esc(n.status||"-")+"</b></div><div class=\"metric\"><span class=\"muted\">K3s</span><b>"+esc(n.k3s_status||n.k3s_role||"-")+"</b></div><div class=\"metric\"><span class=\"muted\">Agent</span><b>"+esc(n.agent_version||d.agent?.status||"-")+"</b></div><div class=\"metric\"><span class=\"muted\">Capacity</span><b>"+esc((n.cpu_cores||"-")+" CPU")+"</b></div></div><h3>Bootstrap history</h3>"+eventsList(d.open_bootstrap_events||[])+"<h3>Recent deploys</h3>"+deploymentRows(d.recent_deployment_jobs||[])+"<h3>Danger zone</h3><button class=\"danger\" onclick=\"removeNode('"+esc(n.id)+"')\">Remove node</button></div>";
}
function renderServices(){
 $("services").innerHTML="<div class=\"panel\"><h2>Add Service</h2>"+serviceForm()+"</div><div class=\"panel\"><h2>Services</h2>"+servicesTable()+"</div>"+serviceDetail();
}
function serviceForm(){
 return "<form id=\"serviceForm\" class=\"forms\"><label>Type<select name=\"type\"><option value=\"application\">Application service</option><option value=\"managed\">Managed backing service</option><option value=\"external\">External dependency</option></select></label><label>Name<input name=\"name\" required></label><label>Source<select name=\"source_type\"><option value=\"git\">Git</option><option value=\"image\">Image</option><option value=\"external\">External</option></select></label><label>Image<input name=\"image\"></label><label class=\"span2\">Repo URL<input name=\"repo_url\"></label><p class=\"muted span2\">Deploy Now is disabled until project readiness is ready.</p><button class=\"span2 primary\">Save draft</button></form>";
}
function servicesTable(){
 if(!state.services.length) return "<div class=\"empty\">No services. Save a draft, then deploy when the project is ready.</div>";
 const group=t=>state.services.filter(s=>s.type===t);
 return ["application","managed","external"].map(t=>"<h3>"+esc(t)+"</h3>"+serviceRows(group(t))).join("");
}
function serviceRows(rows){
 if(!rows.length) return "<p class=\"muted\">None</p>";
 return "<table class=\"table\"><tbody>"+rows.map(s=>"<tr><td><b>"+esc(s.name)+"</b><br><span class=\"muted\">"+esc(s.source_type)+" "+esc(s.repo_url||s.image||"")+"</span></td><td><span class=\"status "+statusClass(s.status)+"\">"+esc(s.status)+"</span></td><td><button onclick=\"openService('"+esc(s.id)+"')\">Open</button> <button onclick=\"deploy('"+esc(s.id)+"')\" "+(state.readiness?.can_deploy?"":"disabled")+">Deploy</button></td></tr>").join("")+"</tbody></table>";
}
function serviceDetail(){
 const s=state.serviceDetail; if(!s) return "";
 const deps=state.services.filter(x=>x.type!=="application"&&x.id!==s.id);
 const jobs=state.deployments.filter(d=>d.service_id===s.id);
 return "<div class=\"panel\"><h2>Service Detail</h2><div class=\"metrics\"><div class=\"metric\"><span class=\"muted\">Desired</span><b>"+esc(s.status)+"</b></div><div class=\"metric\"><span class=\"muted\">Runtime</span><b>"+esc(state.readiness?.can_deploy?"ready":"not ready")+"</b></div><div class=\"metric\"><span class=\"muted\">Image/version</span><b>"+esc(s.image||"draft")+"</b></div><div class=\"metric\"><span class=\"muted\">Incidents</span><b>0</b></div></div><h3>Deployments</h3>"+deploymentRows(jobs)+"<h3>Dependencies</h3>"+(deps.length?deps.map(d=>"<span class=\"status\">"+esc(d.name)+"</span>").join(" "):"<p class=\"muted\">No bindings yet.</p>")+"<h3>Secrets</h3><p class=\"muted\">Bindings only. Values stay masked and require OTP reveal through Agent vault.</p></div>";
}
function renderDeployments(){
 $("deployments").innerHTML="<div class=\"panel\"><h2>Deployments</h2>"+deploymentsTable()+"</div><div class=\"panel\"><h2>Deployment Progress</h2>"+deployTimeline()+"</div>";
}
function deploymentsTable(){
 if(!state.deployments.length) return "<div class=\"empty\">No deployments. Queued jobs will appear from the Cloud API.</div>";
 return "<table class=\"table\"><thead><tr><th>Request</th><th>Service</th><th>Status</th><th>Requested by</th><th>Created</th><th>Actions</th></tr></thead><tbody>"+state.deployments.map(d=>"<tr><td>"+esc(d.id)+"</td><td>"+esc(serviceName(d.service_id))+"</td><td><span class=\"status "+statusClass(d.status)+"\">"+esc(d.status)+"</span></td><td>"+esc(d.requested_by||"-")+"</td><td>"+esc(rel(d.created_at))+"</td><td><button onclick=\"loadDeployEvents('"+esc(d.id)+"')\">Events</button> <button disabled>Rollback</button></td></tr>").join("")+"</tbody></table>";
}
function deploymentRows(rows){
 if(!rows.length) return "<p class=\"muted\">None</p>";
 return "<table class=\"table\"><tbody>"+rows.map(d=>"<tr><td>"+esc(d.id)+"</td><td><span class=\"status "+statusClass(d.status)+"\">"+esc(d.status)+"</span></td><td>"+esc(rel(d.created_at))+"</td></tr>").join("")+"</tbody></table>";
}
function renderTopology(){
 let html="<div class=\"panel\"><h2>Topology</h2>";
 if(!state.nodes.length||!state.services.length) html+="<div class=\"empty\">Topology will appear after at least one healthy server and one deployed service.</div>";
 else html+="<div class=\"topology\"><div class=\"topo-row\"><div class=\"topo-col\"><h3>Project</h3><div class=\"node\">"+esc(state.project.name)+"<br><span class=\"muted\">default</span></div></div><div class=\"edge\">-&gt;</div><div class=\"topo-col\"><h3>Nodes</h3>"+state.nodes.map(n=>"<div class=\"node\">"+esc(n.name)+"<br><span class=\"status "+statusClass(n.status)+"\">"+esc(n.status)+"</span></div>").join("")+"</div><div class=\"edge\">-&gt;</div><div class=\"topo-col\"><h3>Services</h3>"+state.services.map(s=>"<div class=\"node\">"+esc(s.name)+"<br><span class=\"muted\">"+esc(s.type)+"</span></div>").join("")+"</div><div class=\"edge\">-&gt;</div><div class=\"topo-col\"><h3>Deployments</h3>"+state.deployments.map(d=>"<div class=\"node\">"+esc(d.id)+"<br><span class=\"status "+statusClass(d.status)+"\">"+esc(d.status)+"</span></div>").join("")+"</div></div></div>";
 $("topology").innerHTML=html+"</div>";
}
function renderAudit(){
 if(!state.audit.length){$("audit").innerHTML="<div class=\"panel\"><h2>Audit</h2><div class=\"empty\">No audit events for this project.</div></div>";return}
 $("audit").innerHTML="<div class=\"panel\"><h2>Audit</h2><table class=\"table\"><thead><tr><th>Actor</th><th>Action</th><th>Resource</th><th>Result</th><th>Time</th></tr></thead><tbody>"+state.audit.map(a=>"<tr><td>"+esc(a.actor_user_id||a.actor_type)+"</td><td>"+esc(a.action)+"</td><td>"+esc(a.resource_type)+"/"+esc(a.resource_id)+"</td><td>"+esc(a.result)+"</td><td>"+esc(rel(a.created_at))+"</td></tr>").join("")+"</tbody></table></div>";
}
function renderStatic(){
 $("secrets").innerHTML="<div class=\"panel\"><h2>Secrets</h2><div class=\"empty\">Secret values are masked. Reveal and rotate require owner permission plus OTP in the Agent vault UI.</div></div>";
 $("logs").innerHTML="<div class=\"panel\"><h2>Logs</h2><div class=\"empty\">Logs stream from Agent storage when deployed services exist.</div></div>";
 $("metrics").innerHTML="<div class=\"panel\"><h2>Metrics</h2><div class=\"empty\">Metrics stream from Agent telemetry after node inventory is healthy.</div></div>";
 $("incidents").innerHTML="<div class=\"panel\"><h2>Incidents & RCA</h2><div class=\"empty\">No incidents from sanitized Agent RCA context.</div></div>";
 $("settings").innerHTML="<div class=\"panel\"><h2>Settings</h2><p class=\"muted\">Project-scoped controls use the Cloud registry APIs. PAT is kept only in this input.</p></div>";
}
function timeline(){
 if(!state.events.length) return "<div class=\"empty\">Bootstrap event stream appears after Add Server starts.</div>";
 return "<p class=\"muted\">Reconnect-safe: latest session "+esc(state.boot?.id||"")+" loaded from Cloud.</p>"+eventsList(state.events);
}
function eventsList(events){
 if(!events.length) return "<p class=\"muted\">No events.</p>";
 return "<div class=\"timeline\">"+events.map(e=>"<div class=\"event\"><div>"+esc(e.progress_percent)+"%</div><div><b>"+esc(e.step)+"</b><br><span class=\"muted\">"+esc(e.message_redacted)+"</span><div class=\"bar\"><i style=\"width:"+Number(e.progress_percent||0)+"%\"></i></div></div><div>"+esc(rel(e.created_at))+"</div></div>").join("")+"</div>";
}
function deployTimeline(){
 if(!state.deployEvents.length) return "<div class=\"empty\">Select a deployment to reconnect to its redacted event stream.</div>";
 return eventsList(state.deployEvents);
}
function render(){
 renderProjects();renderOverview();renderServers();renderServices();renderDeployments();renderTopology();renderAudit();renderStatic();bindForms();
}
function renderError(e){document.querySelectorAll(".view").forEach(v=>v.innerHTML="<div class=\"panel\"><h2>Network or permission error</h2><p class=\"muted\">"+esc(e.message)+"</p><button onclick=\"load()\">Retry</button></div>")}
function bindForms(){
 const pf=$("projectForm"); if(pf) pf.onsubmit=async ev=>{ev.preventDefault();const f=new FormData(pf);try{await api("/api/orgs/"+encodeURIComponent($("org").value||"org-1")+"/projects",{method:"POST",write:true,body:{name:f.get("name"),slug:f.get("slug")}});pf.reset();await load()}catch(e){toast(e.message)}};
 const sf=$("serverForm"); if(sf) sf.onsubmit=async ev=>{ev.preventDefault();if(!state.project)return toast("select project");const f=new FormData(sf);const secret=String(f.get("secret")||"");const auth=f.get("auth_method");const body={role:f.get("role"),public_host:f.get("public_host"),ssh_port:Number(f.get("ssh_port")||22),ssh_username:f.get("ssh_username"),auth_method:auth}; if(auth==="private_key") body.ssh_private_key=secret; else body.ssh_password=secret; try{state.busy=true;render();const b=await api("/api/projects/"+state.project.id+"/bootstrap-sessions",{method:"POST",write:true,body}); state.boot=b; sf.reset(); await pollBoot(b.id); await load()}catch(e){toast(e.message)}finally{state.busy=false;render()}};
 const vf=$("serviceForm"); if(vf) vf.onsubmit=async ev=>{ev.preventDefault();if(!state.project)return toast("select project");const f=new FormData(vf);try{await api("/api/projects/"+state.project.id+"/services",{method:"POST",write:true,body:{name:f.get("name"),type:f.get("type"),source_type:f.get("source_type"),repo_url:f.get("repo_url"),image:f.get("image")}});vf.reset();await load()}catch(e){toast(e.message)}};
}
async function reconnectStreams(){
 const active=state.sessions.find(s=>["created","preflight","installing","waiting_agent"].includes(s.status))||state.sessions[0];
 if(active){state.boot=active;try{const ev=await api("/api/projects/"+state.project.id+"/bootstrap-sessions/"+active.id+"/events");state.events=ev||[]}catch{state.events=[]}}
 if(state.deployments[0]) await loadDeployEvents(state.deployments[0].id,false);
}
async function pollBoot(id){if(!state.project)return;for(let i=0;i<30;i++){const ev=await api("/api/projects/"+state.project.id+"/bootstrap-sessions/"+id+"/events");state.events=ev||[];render();if(state.events.some(e=>["succeeded","failed","cancelled","expired"].includes(e.step)))break;await new Promise(r=>setTimeout(r,1000));}}
async function diagnostics(id){try{state.nodeDetail=await api("/api/projects/"+state.project.id+"/nodes/"+id);render();switchView("servers")}catch(e){toast(e.message)}}
async function drain(id){try{await api("/api/projects/"+state.project.id+"/nodes/"+id+"/drain",{method:"POST",write:true});await load()}catch(e){toast(e.message)}}
async function removeNode(id){try{await api("/api/projects/"+state.project.id+"/nodes/"+id+"/remove",{method:"POST",write:true});await load()}catch(e){toast(e.message)}}
function openService(id){state.serviceDetail=state.services.find(s=>s.id===id)||null;render();switchView("services")}
async function loadDeployEvents(id,rerender=true){try{const data=await api("/api/projects/"+state.project.id+"/deployments/"+id+"/events");state.deployEvents=data.events||[];if(rerender)render()}catch(e){toast(e.message)}}
async function deploy(id){try{const d=await api("/api/projects/"+state.project.id+"/services/"+id+"/deployments",{method:"POST",write:true,body:{requested_by:"ui"}});await load();await loadDeployEvents(d.id,false);switchView("deployments")}catch(e){toast(e.message)}}
function serviceName(id){return state.services.find(s=>s.id===id)?.name||id}
function lastDeploy(){return state.deployments[0]?rel(state.deployments[0].created_at):"-"}
document.querySelectorAll(".nav button").forEach(b=>b.onclick=()=>switchView(b.dataset.view));
$("refresh").onclick=load;$("projectSelect").onchange=load;$("org").value=localStorage.getItem("opsi_org")||"org-1";
load();
</script>
</body>
</html>`
