package launch

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/webui"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

// The fixture delays HTML, asset and connector TCP bytes, after WebSocket compression.
// Outbound writes enter a bounded simulated link buffer; transmission is paced
// at one shared 1 Mbit/s rate with 100 ms propagation each way.
type browserSlowListener struct {
	net.Listener
	link                        *browserSlowLink
	bytes, queued, peak, active atomic.Int64
}

type browserSlowLink struct {
	mu   sync.Mutex
	next time.Time
}

func (l *browserSlowListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	c := &browserSlowConn{Conn: conn, listener: l, writes: make(chan browserSlowWrite, 8), done: make(chan struct{})}
	l.active.Add(1)
	go c.transmit()
	return c, nil
}

type browserSlowWrite struct {
	data []byte
	at   time.Time
}
type browserSlowConn struct {
	net.Conn
	listener *browserSlowListener
	writes   chan browserSlowWrite
	done     chan struct{}
	once     sync.Once
}

func (c *browserSlowConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-c.done:
			return 0, net.ErrClosed
		}
	}
	return n, err
}

func (c *browserSlowConn) Write(p []byte) (int, error) {
	link := c.listener.link
	link.mu.Lock()
	start := time.Now().Add(100 * time.Millisecond)
	if link.next.After(start) {
		start = link.next
	}
	link.next = start.Add(time.Duration(len(p)) * time.Second / 125000)
	write := browserSlowWrite{append([]byte{}, p...), link.next}
	link.mu.Unlock()
	queued := c.listener.queued.Add(int64(len(p)))
	for peak := c.listener.peak.Load(); queued > peak && !c.listener.peak.CompareAndSwap(peak, queued); peak = c.listener.peak.Load() {
	}
	select {
	case c.writes <- write:
		return len(p), nil
	case <-c.done:
		c.listener.queued.Add(-int64(len(p)))
		return 0, net.ErrClosed
	}
}

func (c *browserSlowConn) transmit() {
	defer func() {
		for {
			select {
			case write := <-c.writes:
				c.listener.queued.Add(-int64(len(write.data)))
			default:
				return
			}
		}
	}()
	for {
		select {
		case write := <-c.writes:
			timer := time.NewTimer(time.Until(write.at))
			select {
			case <-timer.C:
			case <-c.done:
				timer.Stop()
				c.listener.queued.Add(-int64(len(write.data)))
				return
			}
			n, err := c.Conn.Write(write.data)
			c.listener.queued.Add(-int64(len(write.data)))
			c.listener.bytes.Add(int64(n))
			if err != nil {
				c.Close()
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *browserSlowConn) Close() error {
	c.once.Do(func() { close(c.done); c.listener.active.Add(-1) })
	return c.Conn.Close()
}

func TestWebUIBrowserSlowHistory(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_WEBUI_BROWSER") != "1" {
		t.Skip("set MEKUGI_TEST_WEBUI_BROWSER=1 after make webui")
	}
	t.Setenv("AGENT_BROWSER_ENGINE", "chrome")
	browser := os.Getenv("MEKUGI_TEST_AGENT_BROWSER")
	if browser == "" {
		browser = "agent-browser"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	pacing := &browserSlowLink{}
	page := httptest.NewUnstartedServer(webui.Handler())
	pageLink := &browserSlowListener{Listener: page.Listener, link: pacing}
	page.Listener = pageLink
	page.Start()
	defer page.Close()
	machine := newWebUIBrowserMachine(t, ctx, page.URL, "Slow", strings.Repeat("c", 64))
	machine.remote.Close()
	remote := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { machine.bridge.Load().ServeHTTP(w, r) }))
	link := &browserSlowListener{Listener: remote.Listener, link: pacing}
	remote.Listener = link
	remote.Start()
	defer remote.Close()
	machine.remote = remote
	saved, _ := json.Marshal(map[string]string{"id": "slow", "label": "Slow", "endpoint": remote.URL, "token": strings.Repeat("c", 64)})
	configuration := `localStorage.setItem("mekugi.machine.v1:slow",` + fmt.Sprintf("%q", string(saved)) + `); if (!location.hash) location.hash="#machine=slow&thread=same-thread";`
	// A pre-navigation script observes the untouched application without adding
	// a synthetic resource request or weakening its content security policy.
	probe := filepath.Join(t.TempDir(), "probe.js")
	if err := os.WriteFile(probe, []byte(configuration+slowHistoryProbe), 0600); err != nil {
		t.Fatal(err)
	}
	main, agents := &activity.Publication{}, &activity.Publication{}
	random := rand.NewPCG(1, 2)
	for i := range 600 {
		var text strings.Builder
		fmt.Fprintf(&text, "Retained entry %d\n\n", i)
		for range 140 {
			fmt.Fprintf(&text, "%016x", random.Uint64())
		}
		if i == 599 {
			text.WriteString("\n\nLARGE-LATEST")
		}
		main.Entries = append(main.Entries, activity.Entry{Seq: uint64(i + 1), Agent: "Main", Kind: "text", Text: text.String(), Revision: 1})
		agents.Entries = append(agents.Entries, activity.Entry{Seq: uint64(i + 1), Agent: "/root/history", Kind: "text", Text: text.String(), Revision: 1})
	}
	// The newest oversized text starts with its visible preview and completes
	// automatically, independently of readiness and navigation.
	main.Entries[599].Text = "LARGE-LATEST\n\n" + main.Entries[599].Text + "\n\nLARGE-COMPLETE"
	if err := machine.publishFrame(sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Caller: "/root", Title: "Large thread", Workspace: machine.workdir}, Input: &sessionapi.Input{Stoppable: true}, Main: main, Agents: agents}); err != nil {
		t.Fatal(err)
	}
	machine.start("small")
	if err := machine.publishFrame(sessionapi.Frame{Session: sessionapi.Session{Thread: "small", Caller: "/root", Title: "Small thread", Workspace: machine.workdir}, Input: &sessionapi.Input{Stoppable: true}, Main: &activity.Publication{Entries: []activity.Entry{{Seq: 1, Agent: "Main", Kind: "text", Text: "SMALL-LATEST", Revision: 1}}}}); err != nil {
		t.Fatal(err)
	}
	identity, err := exec.CommandContext(ctx, browser, "session", "id", "--scope", "worktree", "--prefix", "slow-history").CombinedOutput()
	if err != nil {
		t.Fatalf("browser identity: %v", err)
	}
	name := strings.TrimSpace(string(identity))
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, browser, "--session", name, "close").Run()
	}()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, browser, append([]string{"--session", name}, args...)...).CombinedOutput()
		if err != nil {
			snapshot, _ := exec.CommandContext(ctx, browser, "--session", name, "snapshot", "-i").CombinedOutput()
			errors, _ := exec.CommandContext(ctx, browser, "--session", name, "errors").CombinedOutput()
			t.Fatalf("browser %s: %v: %s\n%s\n%s", args[0], err, out, snapshot, errors)
		}
		return strings.TrimSpace(string(out))
	}
	run("--init-script", probe, "open", page.URL)
	run("wait", "--fn", "!!window.readyResult")
	t.Log("empty-cache cold visit (separate):", run("eval", "window.readyResult"))
	if run("eval", "window.readyResult.assets.some(asset => asset.transfer > 0)") != "true" {
		t.Fatal("cold visit did not transfer application assets")
	}
	t.Logf("cold page/asset bytes=%d, connector bytes=%d", pageLink.bytes.Load(), link.bytes.Load())
	pageBytes, connectorBytes := pageLink.bytes.Load(), link.bytes.Load()
	run("reload")
	run("wait", "--fn", "!!window.readyResult")
	t.Log("cached-asset reload:", run("eval", "window.readyResult"))
	if run("eval", "window.readyResult.assets.length > 0 && window.readyResult.assets.every(asset => asset.transfer === 0)") != "true" {
		t.Fatal("reload did not reuse cached application assets")
	}
	t.Logf("reload page/asset bytes=%d, connector bytes=%d", pageLink.bytes.Load()-pageBytes, link.bytes.Load()-connectorBytes)
	if run("eval", "window.readyResult.ms < 1000") != "true" {
		t.Error("cached reload exceeded one second to the initial view and usable Stop")
	}
	run("wait", "--text", "LARGE-COMPLETE")
	run("fill", "#composer-text", "retained large draft")
	for round := range 3 {
		t.Log("switch to small", round, run("eval", `window.measureThread('small')`))
		if run("eval", "window.readyResult.ms < 1000") != "true" {
			t.Fatal("small switch exceeded one second")
		}
		t.Log("switch while large read is pending", round, run("eval", `window.switchDuringLargeRead()`))
		if run("eval", "window.readyResult.ms < 1000") != "true" {
			t.Fatal("replacing large observation delayed the small destination")
		}
		t.Log("large switch", round, run("eval", `window.measureThread('same-thread')`))
		if run("eval", "window.readyResult.ms < 1000") != "true" {
			t.Fatal("large switch exceeded one second")
		}
		if run("get", "value", "#composer-text") != "retained large draft" {
			t.Fatal("switching lost the source draft")
		}
	}
	run("wait", "--fn", "window.liveSubscriptions.size === 3")
	if run("eval", "window.maxSharedBytes <= 32768") != "true" {
		t.Fatal("shared delivery exceeded its envelope budget")
	}
	t.Logf("actual compressed connector bytes=%d, peak simulated send backlog=%d, active TCP connections=%d", link.bytes.Load(), link.peak.Load(), link.active.Load())
	if link.peak.Load() > 64<<10 {
		t.Fatal("navigation retained more than two bounded windows of send backlog")
	}
}

const slowHistoryProbe = `
window.liveSubscriptions = new Map(); window.pendingShared = new Set(); window.maxSharedBytes = 0; window.transportMilestones = [];
window.goal = {thread:new URLSearchParams(location.hash.slice(1)).get('thread'),started:0};
const Original = WebSocket;
window.WebSocket = class extends Original {
  owned = new Map();
  calls = new Map();
  constructor(...args) { super(...args); window.transportMilestones.push({event:'socket-created',ms:performance.now()}); this.addEventListener('open',()=>window.transportMilestones.push({event:'socket-open',ms:performance.now()})); this.addEventListener('close',()=>{ for(const id of this.owned.keys()) window.liveSubscriptions.delete(id); }); }
  send(text) { const call=JSON.parse(text); this.calls.set(String(call.id),call.method); if(call.method==='initialize'||call.method==='session/subscribe'||call.method==='view/attach') window.transportMilestones.push({event:'send:'+call.method,ms:performance.now()}); if (call.method==='session/subscribe'||call.method==='view/attach') { this.owned.set(call.params.subscription,{method:call.method,thread:call.params.thread}); } const result = super.send(text); if (call.method==='session/subscribe' && call.params.thread) { window.pendingShared.add(call.params.subscription); } return result; }
  addEventListener(type,listener,...options) { if(type!=='message') return super.addEventListener(type,listener,...options); return super.addEventListener(type,event=>{ const message=JSON.parse(event.data); if(message.id!==undefined) { const method=this.calls.get(String(message.id)); this.calls.delete(String(message.id)); if(method==='initialize'||method==='session/subscribe'||method==='view/attach') window.transportMilestones.push({event:'reply:'+method,ms:performance.now()}); if(message.result?.subscription) { const info=this.owned.get(message.result.subscription); window.liveSubscriptions.set(message.result.subscription,info); if(info?.method==='session/subscribe'&&info.thread==='same-thread') window.largeRequest?.(message.result.subscription); } } if(message.method==='subscription/closed') window.liveSubscriptions.delete(message.params.subscription); if(message.method==='subscription/event') window.pendingShared.delete(message.params.subscription); if(message.method==='subscription/event'&&window.liveSubscriptions.get(message.params.subscription)?.method==='session/subscribe'&&message.params.kind!=='sessions') window.maxSharedBytes=Math.max(window.maxSharedBytes,new TextEncoder().encode(event.data).length); listener.call(this,event); },...options); }
};
new MutationObserver(()=>{ const goal=window.goal; if(!goal) return; const marker=goal.thread==='small'?'SMALL-LATEST':'LARGE-LATEST'; if(!document.querySelector('[aria-label="Transcript"]')?.textContent.includes(marker)||document.querySelector('button[aria-label="Stop"]')?.disabled!==false) return; window.readyResult={thread:goal.thread,ms:performance.now()-goal.started,maxSharedBytes:window.maxSharedBytes,...(goal.started===0?{transport:window.transportMilestones,html:performance.getEntriesByType('navigation').map(n=>({responseEnd:n.responseEnd,transfer:n.transferSize})),assets:performance.getEntriesByType('resource').filter(r=>new URL(r.name).pathname.startsWith('/assets/')).map(r=>({name:new URL(r.name).pathname.split('/').at(-1),transfer:r.transferSize,decoded:r.decodedBodySize}))}:{})}; window.goal=undefined; goal.resolve?.(window.readyResult); }).observe(document,{childList:true,subtree:true,attributes:true});
window.measureThread=thread=>new Promise(resolve=>{ window.readyResult=undefined; window.goal={thread,started:performance.now(),resolve}; const link=[...document.querySelectorAll('nav a')].find(link=>new URLSearchParams(new URL(link.href).hash.slice(1)).get('thread')===thread); if(!link) throw Error('Missing thread link'); link.click(); });
window.switchDuringLargeRead=async()=>{ const sent=new Promise(resolve=>window.largeRequest=resolve); const started=performance.now(); void window.measureThread('same-thread'); const subscription=await sent; window.largeRequest=undefined; const pending=window.pendingShared.has(subscription); const admitted=window.liveSubscriptions.has(subscription); const ready=!!window.readyResult; const elapsed=performance.now()-started; if(!admitted||!pending||ready) throw Error('Large thread was not admitted and pending at interruption'); const destination=await window.measureThread('small'); return {...destination,interrupted:{admitted,pending,ready,elapsed}}; };
`
