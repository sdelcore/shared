# shared

One small Go server that hosts many static sites with subdomain routing, an
rsync-style deploy flow, and a batteries-included client-side JS API — a document
DB with realtime subscriptions, an AI chat proxy, file uploads, websocket
channels, and identity. Drop a `<script src="/shared.js">` into any page and go.

**No auth. Single user, trusted network only.** Anyone who can reach the port
can read and write everything. Run it on your LAN or behind a VPN, not the
open internet.

## Attribution

This project is a self-hosted reimplementation of the ideas behind
[Quick](https://shopify.engineering/quick), Shopify's internal hosting platform
that "lets anyone at Shopify ship a site in seconds." The architecture
(deliberately simple single-server hosting, FTP-style deploys, a fixed client
API with db/ai/uploads/websockets/identity, no permissions by design) comes
from their engineering write-up; this codebase is an independent from-scratch
implementation for personal use, and is not affiliated with Shopify.

## Quickstart (NixOS)

```sh
direnv allow          # or: nix develop

# run the server
go run ./cmd/sharedd

# in another shell: build the CLI and deploy the example site
go build -o bin/ ./...
bin/shared deploy examples/hello --name hello

# open it
xdg-open http://hello.localhost:8787
```

The homepage at `http://localhost:8787` lists all deployed sites.

## Subdomain routing

Each site lives at `http://<name>.<base-host><port>/`. Modern browsers resolve
any `*.localhost` name to `127.0.0.1` without `/etc/hosts` entries, so
`http://hello.localhost:8787` just works out of the box. To serve under a real
domain on your LAN, set `SHARED_BASE_HOST` (e.g. `shared.lan`) and point a
wildcard DNS record at the box.

Per-site data endpoints (db, uploads, ws) scope strictly to the first label of
the request's `Host` header — one site cannot reach another site's data, even
from a visitor's browser. The deploy endpoint takes its target from `?site=`.
Site names must match `^[a-z0-9][a-z0-9-]{0,62}$`.

## Client API

Every site can load the shared client library:

```html
<script src="/shared.js"></script>
```

The library scopes everything to the current site automatically.

### shared.db

A JSON document store. Documents get server-managed `id`, `createdAt`, and
`updatedAt` fields.

```js
const posts = shared.db.collection('posts');

const doc  = await posts.create({ title: 'hi', body: '...' });
const all  = await posts.list();                  // sorted by createdAt
const one  = await posts.get(doc.id);
await posts.update(doc.id, { title: 'hello' });
await posts.delete(doc.id);

// realtime: fires on created / updated / deleted
const sub = posts.subscribe({
  onCreate: doc => console.log('created', doc),
  onUpdate: doc => console.log('updated', doc),
  onDelete: doc => console.log('deleted', doc),
});
// sub.close() to stop. On reconnect after a drop, subscribe re-syncs and
// replays any create/update/delete missed while disconnected.
```

### shared.ai

A proxy to an OpenAI-compatible chat API (OpenAI, or a gateway like LiteLLM) —
your key stays on the server.

```js
const reply = await shared.ai.chat('Summarize this in one line: ...');

// or full control:
const res = await shared.ai.chat({
  messages: [{ role: 'user', content: 'hi' }],
  system: 'Be terse.',
  max_tokens: 256,
});
```

Requires `OPENAI_BASE_URL` and `OPENAI_API_KEY` to be set on the server;
otherwise calls fail with an error message.

### shared.uploads

```js
const { url } = await shared.uploads.upload(fileInput.files[0]);
img.src = url;   // served from /uploads/<site>/<rand>-<name>
```

### shared.ws

Per-site broadcast channels. Each message is relayed to every *other* member
of the same channel.

```js
const room = shared.ws.channel('lobby');
room.onMessage(msg => console.log(msg));
room.send('hello everyone');
```

### shared.identity

```js
const me = await shared.identity();   // { email, name }
```

Returns the contents of `data/identity.json` if present, otherwise a default
derived from `SHARED_USER` (or `$USER`).

## HTTP API

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/deploy?site=N` | Body: gzipped tarball of site dir → `{"site","url"}` |
| `GET` | `/api/sites` | `{"sites":[{"name","updatedAt"}]}` |
| `GET` | `/api/db/{col}` | `{"docs":[...]}` |
| `POST` | `/api/db/{col}` | JSON body → created doc (201) |
| `GET` | `/api/db/{col}/{id}` | Doc, or 404 `{"error":...}` |
| `PUT` | `/api/db/{col}/{id}` | JSON body → updated doc |
| `DELETE` | `/api/db/{col}/{id}` | `{"deleted":true}` |
| `GET` | `/api/db/{col}/subscribe` | WebSocket; pushes `{"type","doc"}` events |
| `POST` | `/api/ai/chat` | `{"messages",...}` → `{"content","model","stop_reason"}` |
| `POST` | `/api/uploads` | multipart field `file` → `{"url"}` (201) |
| `GET` | `/api/identity` | `{"email","name"}` |
| `GET` | `/api/ws?channel=C` | WebSocket broadcast channel |

Per-site endpoints (db, uploads, ws) take the site from the request's
subdomain.

## Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `SHARED_ADDR` | `:8787` | Listen address |
| `SHARED_DATA` | `./data` | Data directory |
| `SHARED_BASE_HOST` | `localhost` | Base host for subdomain routing |
| `OPENAI_BASE_URL` | — | OpenAI-compatible base URL (e.g. `http://llm.tools.tap/v1`); enables `/api/ai/chat` |
| `OPENAI_API_KEY` | — | API key/token for the above (e.g. LiteLLM master key) |
| `SHARED_AI_MODEL` | `claude-opus-4-8` | Default AI model (e.g. `zen/kimi-k2.6`) |
| `SHARED_USER` | `$USER` | Name/email for the default identity |

## Data layout

```
data/
  sites/<name>/        deployed static files, one dir per site
  db/<site>/<col>.json document store, one JSON file per collection
  uploads/<site>/      uploaded files
  identity.json        optional identity override: {"email","name"}
```

Everything is plain files — back it up with whatever you already use.
