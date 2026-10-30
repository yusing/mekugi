# Server, connector and browser

Keep threads on an execution machine and control them from terminals or browsers.

## Server and remote control

Native launches start the local server automatically. To keep a machine available
for remote clients without opening a terminal UI, start it explicitly:

```sh
mekugi server
```

It runs in the foreground and prints its private Unix socket path. By default,
the socket is under the user cache at `mekugi/server/control.sock`; `--socket PATH`
selects another private location. Local clients use the same-user socket and the
versioned [Session API](../../apps/mekugi/internal/sessionapi/CONTRACT.md). Workspaces,
models and host attachment paths belong to this execution machine.

For remote clients, run a connector on that same machine:

```sh
mekugi connector --origin https://ui.example.com
```

The connector listens on `127.0.0.1:7778` and prints its token-file location,
without printing the token. Its default token file is `connector/token` beside
the server socket. Read it privately to configure the client's bearer credential;
keep it out of URLs and logs. `--token-file PATH` selects a different private file,
`--socket PATH` selects the server, and repeated `--origin` flags admit additional
UI origins. Origin permission never replaces token authentication.

Terminate WSS at a reverse proxy and keep its connection to the connector
on loopback or a protected private network. Forward WebSocket upgrades and
allow the API's
[attachment request budget](../../apps/mekugi/internal/sessionapi/CONTRACT.md#attachment-input).
Browser clients connect directly to each connector; the UI host receives neither
machine credentials nor control requests.

Clients authenticate in the first control message; credentials stay out of URLs
and upgrade headers. Send SIGHUP to the connector process to rotate its token.
Rotation closes connections using the old credential; clients need the new token. Closing the
connector or a client leaves server-owned work running. Stopping the server ends
its active work; saved threads remain resumable. Restart never automatically
replays requests or restarts work. See the [connector contract](../../apps/mekugi/internal/connector/SPEC.md)
for admission and deployment details.

## Browser UI

Run the assets host on any machine:

```sh
mekugi web --listen 127.0.0.1:7777
```

Open that address and connect the execution machine with a name, its connector
URL and its access token; later machines are added under **Machines** at the foot
of the sidebar. There is no default connection.
Configure the connector's allowed origin to match the page's exact origin, for
example `mekugi connector --origin http://127.0.0.1:7777` for local use. For remote
use, serve the page over HTTPS and the connector through a WSS reverse proxy.
Machines and access tokens are saved in this browser for the page's origin, so
reload reconnects them and restores the thread selected in the page address. Use
a trusted browser. Removing a machine under **Machines** deletes its saved token;
clearing this site's browser data forgets all machines. Browser storage failures
show a warning while connections remain usable for the current page.

Install Mekugi using your browser's **Install app**, **Add to Dock** or
**Add to Home Screen** action, where supported. Installation requires HTTPS or
a localhost/loopback address. The installed app still needs the UI host and its
execution-machine connectors online.

The sidebar lists each machine's threads. **New thread** starts one from its
first message; the composer's **+** menu also resumes a saved thread or opens a
replay. Workspaces, models and attachment paths refer to the chosen machine.
Upload files from the browser or attach host paths in order, up to 32 MiB of
decoded uploads per message. As in the terminal, `/`, `@` and `$` complete
commands, files and skills, and a leading `!` runs shell input; Enter sends or
steers, and Tab queues while Main works. Presets saved on the execution machine
are shared with the terminal [composer](terminal-ui.md#composer). Answer secret
prompts in the native terminal.

Active processes stay above the composer, with **Stop** for each command whose
host exposes individual process control. Other commands remain visible with a
disabled Stop and an explanation. `/session` opens the shared session metrics;
`/status` shows grouped settings and usage. Web links open in a new tab, while
Markdown file links open details from a connected live thread on the selected
execution machine.

Submissions rejected after admission offer **Retry intact** or **Discard**. Retry reuses the original
attachment snapshots. Resolve unsent input before leaving the thread or reconnecting;
its recovery belongs to that control view. A refused first message leaves the opened
thread available with its draft.

Closing the page or disconnecting a machine leaves its work running. **End thread**,
in the thread's session details, explicitly stops that thread. On its execution machine, `mekugi resume THREAD_ID`
continues it from a native terminal. Replay is read-only, shares playback controls
between viewers, and requires the live thread to be ended first. Completed playback
stays open until ended. If a request loses its acknowledgement, check that machine's
state before resubmitting; the browser does not retry mutations automatically.

Source checkouts need `make webui` before embedding browser assets. See
[development checks](../contributing/development.md#build-and-test) and the
[browser specification](../../apps/mekugi/internal/webui/SPEC.md).

### Docker deployment

The root `Dockerfile` builds browser assets with Bun and embeds them in a CGO-enabled
Go binary. `compose.yaml` runs separate web UI and connector containers. The
execution server, Codex, credentials and workspaces stay on the execution machine.

For a Docker Engine on that machine with Tailscale connected, start `mekugi server`, then:

```sh
cp .env.example .env
# Edit .env: set MEKUGI_SERVER_DIR to the printed socket's parent directory,
# and MEKUGI_UID / MEKUGI_GID to the server user's numeric id -u / id -g.
# Set TAILNET_IP from tailscale ip -4 and WEBUI_ORIGIN to http://TAILNET_IP:7777.
install -d -m 700 .docker/connector
docker compose up --build -d
```

The socket directory must be private (0700). The connector must run with the
same host UID as the server; container user-namespace mappings must preserve that
identity. Host Unix-socket sharing requires a compatible Docker host; Docker
Desktop's VM cannot generally use a native macOS/Windows server socket.
The directory mount follows socket replacement when the server restarts.

From a permitted tailnet device, open `http://TAILNET_IP:7777` and add
`http://TAILNET_IP:7778` as a machine, substituting the configured IP.
Read `.docker/connector/token` privately for the access token. It is generated at
first startup and persists across container replacement. Keep `.env` and
`.docker/` private and out of version control. If changing `CONNECTOR_STATE_DIR`,
create that directory with the same ownership and permissions first.

Published ports bind only to the configured Tailscale IPv4 address. Tailnet access
rules must permit the browser device to reach both ports. Tailscale encrypts this
traffic; the listeners themselves use HTTP and WebSocket. For HTTPS/WSS, terminate
TLS at a reverse proxy, set `WEBUI_ORIGIN` to the exact page origin and use its
connector URL. Forward WebSocket upgrades and the attachment budget described
above. Changing the UI address or port also requires updating `WEBUI_ORIGIN`.
Rotate credentials with `docker compose kill -s SIGHUP connector`; enter the new
token in the browser. `docker compose down` stops these two services and retains
the token directory and server-owned work.
