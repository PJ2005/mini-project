package ui

import (
	"encoding/json"
	"net/http"

	"interlink/internal/pipeline"
	"interlink/internal/registry"
)

type UIServer struct {
	pipelines []*pipeline.Pipeline
	reg       *registry.Registry
}

func New(pipelines []*pipeline.Pipeline, reg *registry.Registry) *UIServer {
	return &UIServer{pipelines: pipelines, reg: reg}
}

func (u *UIServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", u.handleIndex)
	mux.HandleFunc("GET /api/ui/pipelines", u.handlePipelines)
	mux.HandleFunc("GET /api/ui/devices", u.handleDevices)
	return mux
}

func (u *UIServer) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

func (u *UIServer) handlePipelines(w http.ResponseWriter, _ *http.Request) {
	stats := make([]pipeline.Stats, 0, len(u.pipelines))
	for _, p := range u.pipelines {
		stats = append(stats, p.Stats())
	}
	writeJSON(w, stats)
}

func (u *UIServer) handleDevices(w http.ResponseWriter, _ *http.Request) {
	devices := make([]registry.Device, 0)
	if u.reg != nil {
		if out, err := u.reg.ListDevices(); err == nil {
			devices = out
		}
	}
	writeJSON(w, devices)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>InterLink UI</title>
<style>
:root { --bg:#fff; --fg:#111; --muted:#666; --card:#fafafa; --border:#e0e0e0; --ok:#2e7d32; --off:#8a8a8a; --err:#c62828; }
@media (prefers-color-scheme: dark) {
  :root { --bg:#121212; --fg:#ececec; --muted:#a7a7a7; --card:#1c1c1c; --border:#343434; --ok:#66bb6a; --off:#9e9e9e; --err:#ef5350; }
}
* { box-sizing: border-box; }
body { margin: 0; font: 14px system-ui, -apple-system, Segoe UI, sans-serif; background: var(--bg); color: var(--fg); }
main { display: flex; flex-wrap: wrap; gap: 12px; padding: 12px; }
section { flex: 1 1 360px; min-width: 320px; }
h2 { margin: 0 0 8px; font-size: 16px; }
.card { background: var(--card); border: 1px solid var(--border); border-radius: 8px; padding: 10px; margin-bottom: 8px; }
.row { display: flex; justify-content: space-between; gap: 8px; align-items: baseline; }
.small { color: var(--muted); font-size: 12px; }
.dot { display:inline-block; width:8px; height:8px; border-radius:50%; margin-right:6px; }
.green { background: var(--ok); }
.gray { background: var(--off); }
.err { color: var(--err); }
.code { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
</style>
</head>
<body>
<main>
  <section>
    <h2>Pipelines</h2>
    <div id="pipelines"></div>
  </section>
  <section>
    <h2>Devices</h2>
    <div id="devices"></div>
  </section>
</main>
<script>
const elPipes = document.getElementById('pipelines');
const elDevices = document.getElementById('devices');
const rel = ms => {
  if (!ms) return 'never';
  const s = Math.max(0, Math.floor((Date.now() - ms) / 1000));
  if (s < 60) return s + 's ago';
  if (s < 3600) return Math.floor(s/60) + 'm ago';
  if (s < 86400) return Math.floor(s/3600) + 'h ago';
  return Math.floor(s/86400) + 'd ago';
};
async function fetchJSON(url){
  const r = await fetch(url, {cache:'no-store'});
  if (!r.ok) throw new Error(url + ' ' + r.status);
  return r.json();
}
function renderPipelines(items){
  if (!items.length) { elPipes.innerHTML = '<div class="card small">No pipelines configured.</div>'; return; }
  elPipes.innerHTML = items.map(function(p){
    var errCls = p.error_count > 0 ? 'err' : '';
    return '<div class="card">'
      + '<div class="row"><strong>' + p.name + '</strong><span class="small code">' + p.source_type + ' -> ' + p.sink_type + '</span></div>'
      + '<div class="small">Processed: <span class="code">' + p.messages_processed + '</span></div>'
      + '<div class="small">Last: ' + rel(p.last_message_at) + '</div>'
      + '<div class="small ' + errCls + '">Errors: <span class="code">' + p.error_count + '</span></div>'
      + '</div>';
  }).join('');
}
function renderDevices(items){
  if (!items.length) { elDevices.innerHTML = '<div class="card small">No devices registered.</div>'; return; }
  elDevices.innerHTML = items.map(function(d){
    var dot = d.status === 'active' ? 'green' : 'gray';
    var last = d.last_seen ? rel(Date.parse(d.last_seen)) : 'never';
    return '<div class="card">'
      + '<div class="row"><strong class="code">' + d.device_id + '</strong><span class="small">' + d.protocol + '</span></div>'
      + '<div class="small"><span class="dot ' + dot + '"></span>' + d.status + '</div>'
      + '<div class="small">Last seen: ' + last + '</div>'
      + '</div>';
  }).join('');
}
async function refreshPipelines(){
  try { renderPipelines(await fetchJSON('/api/ui/pipelines')); }
  catch { elPipes.innerHTML = '<div class="card small err">Failed to load pipelines.</div>'; }
}
async function refreshDevices(){
  try { renderDevices(await fetchJSON('/api/ui/devices')); }
  catch { elDevices.innerHTML = '<div class="card small err">Failed to load devices.</div>'; }
}
refreshPipelines();
refreshDevices();
setInterval(refreshPipelines, 3000);
setInterval(refreshDevices, 5000);
</script>
</body>
</html>`
