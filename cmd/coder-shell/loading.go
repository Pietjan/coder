package main

import (
	"encoding/json"
	"html"
	"strings"
)

// progress is what the loading page receives over the webview bridge.
type progress struct {
	Stage string `json:"stage"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	Line  string `json:"line,omitempty"` // a log line to show
}

func (p progress) JSON() string {
	b, _ := json.Marshal(p)
	return string(b)
}

// loadingHTML is shown while the model loads and the daemon starts. It
// listens for progress messages and shows the stages with an elapsed
// timer, and a bar for the one stage that can count.
const loadingHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>coder</title>
<style>
  :root { color-scheme: dark; }
  body { margin: 0; height: 100vh; display: grid; place-items: center; background: #0b0f1a; color: #cbd5e1; font: 14px/1.5 system-ui, sans-serif; }
  main { width: min(560px, 90vw); }
  h1 { font-size: 16px; font-weight: 600; margin: 0 0 16px; color: #e2e8f0; }
  ol { list-style: none; padding: 0; margin: 0 0 16px; }
  li { display: flex; gap: 10px; align-items: baseline; padding: 4px 0; color: #64748b; }
  li.active { color: #e2e8f0; }
  li.done { color: #94a3b8; }
  li .t { margin-left: auto; font-variant-numeric: tabular-nums; font-size: 12px; }
  progress { width: 100%; height: 6px; accent-color: #6366f1; }
  pre { max-height: 160px; overflow: auto; background: #111827; border-radius: 6px; padding: 8px; font-size: 11px; color: #94a3b8; white-space: pre-wrap; }
</style></head>
<body><main>
  <h1>coder</h1>
  <ol id="stages"></ol>
  <progress id="bar" max="1" value="0" hidden></progress>
  <pre id="log"></pre>
</main>
<script>
  var stages = [], started = {}, current = null, t0 = Date.now();
  var ol = document.getElementById('stages'), bar = document.getElementById('bar'), log = document.getElementById('log');
  function fmt(ms) { var s = Math.floor(ms / 1000); return (s >= 60 ? Math.floor(s / 60) + 'm ' : '') + (s % 60) + 's'; }
  function render() {
    ol.innerHTML = '';
    stages.forEach(function (s) {
      var li = document.createElement('li');
      li.className = s === current ? 'active' : 'done';
      var name = document.createElement('span'); name.textContent = s;
      var t = document.createElement('span'); t.className = 't';
      var end = s === current ? Date.now() : (started[s].end || Date.now());
      t.textContent = fmt(end - started[s].start);
      li.appendChild(name); li.appendChild(t); ol.appendChild(li);
    });
  }
  setInterval(render, 1000);
  window.chrome.webview.addEventListener('message', function (e) {
    var p = e.data;
    if (p.line) { log.textContent += p.line + '\n'; log.scrollTop = log.scrollHeight; }
    if (p.stage && p.stage !== current) {
      if (current) started[current].end = Date.now();
      current = p.stage;
      if (stages.indexOf(p.stage) < 0) { stages.push(p.stage); started[p.stage] = { start: Date.now() }; }
    }
    if (p.total > 0) { bar.hidden = false; bar.max = p.total; bar.value = p.done; } else { bar.hidden = true; }
    render();
  });
</script>
</body></html>`

// errorHTML says what went wrong, with the log's tail and a way out.
func errorHTML(msg, logTail string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8"><title>coder</title><style>
  :root { color-scheme: dark; }
  body { margin: 0; height: 100vh; display: grid; place-items: center; background: #0b0f1a; color: #cbd5e1; font: 14px/1.5 system-ui, sans-serif; }
  main { width: min(640px, 90vw); }
  h1 { font-size: 16px; color: #f87171; margin: 0 0 12px; }
  pre { max-height: 300px; overflow: auto; background: #111827; border-radius: 6px; padding: 8px; font-size: 11px; white-space: pre-wrap; }
  button { margin-top: 12px; background: #6366f1; color: white; border: 0; border-radius: 6px; padding: 8px 14px; font: inherit; }
</style></head><body><main><h1>coder could not start</h1><p>`)
	b.WriteString(html.EscapeString(msg))
	b.WriteString(`</p><pre>`)
	b.WriteString(html.EscapeString(logTail))
	b.WriteString(`</pre><button onclick="window.chrome.webview.postMessage({quit:true})">Quit</button></main></body></html>`)
	return b.String()
}
