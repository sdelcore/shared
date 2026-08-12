---
name: shared-sites
description: Building and deploying static sites/apps on the self-hosted shared platform — its /shared.js client API (document DB, AI chat and images, uploads, websocket channels, identity) and the deploy/rollback flow.
---

# shared-sites

Build a site as plain static files (an `index.html` plus whatever assets), add
`<script src="/shared.js"></script>`, and deploy the directory. The server hosts
each site at its own subdomain and gives every page a client API scoped to that
site automatically. There is no build step and no backend to write.

**No auth.** Single user, trusted LAN only. Anyone who can reach the server can
read and write every site's data. Do not expose it to the open internet, and do
not put secrets in site data.

## Client API (`/shared.js` → `window.shared`)

All calls are promise-based and scoped to the current site by its subdomain.

The served `/shared.js` is the source of truth for signatures, and it moves
ahead of this file. Run `curl $SHARED_SERVER/shared.js` and read the function
you are about to call. The callback-shaped APIs (`db.subscribe`, `ws.channel`)
fail silently when called wrongly, so a mismatch looks like "the feature does
not work" rather than an error.

### shared.db

Per-collection JSON document store. Docs get server-managed `id`, `createdAt`,
`updatedAt`.

```js
const posts = shared.db.collection('posts');
const doc = await posts.create({ title: 'hi' });   // POST → created doc
const all = await posts.list();                     // array, sorted by createdAt
const one = await posts.get(doc.id);
await posts.update(doc.id, { title: 'yo' });        // PUT → updated doc
await posts.delete(doc.id);

const sub = posts.subscribe({
  onCreate(doc) {},
  onUpdate(doc) {},
  onDelete(doc) {},
});
sub.close();   // stop listening
```

`subscribe` takes a handlers object, **not** a callback. A bare function is
accepted and then never fires, which is the most common way to ship a dead
realtime UI here. Each handler receives the document itself, so there is no
event wrapper and no `e.type` / `e.doc`. `subscribe` returns `{ close }`, not an
unsubscribe function. The socket auto-reconnects (1s backoff) and replays what
was missed through the same handlers; `onDelete` receives `{ id }` on that
replay and the full doc live.

### shared.ai

Proxy to an OpenAI-compatible chat API; the key stays on the server.

```js
// chat(messages, opts) — two positional args. A string is wrapped for you.
const reply = await shared.ai.chat('Summarize: ...');
const reply2 = await shared.ai.chat(
  [{ role: 'user', content: 'hi' }],
  { system: 'Be terse.', model: 'some-model', max_tokens: 256 },
);

// streaming — prefer it for anything long, and required for models that only
// support streaming. Still resolves to the full text at the end.
const full = await shared.ai.chat(q, { stream: true, onToken: t => out.append(t) });

// image generation — the PNG is saved to this site's uploads and the URL is
// permanent.
const { url } = await shared.ai.image('a red bicycle', { size: '1024x1024' });
```

Do not pass a single options object as the first argument to `chat`.
`{ messages, system }` is sent as the message list and the call fails. Message
roles must be `user` or `assistant`; put the system prompt in `opts.system`.

Server-side configuration, all environment variables on `sharedd`:

| Variable | Effect |
|---|---|
| `OPENAI_BASE_URL`, `OPENAI_API_KEY` | required; both AI endpoints 503 without them |
| `SHARED_AI_MODEL` | default chat model |
| `SHARED_AI_IMAGE_MODEL` | default image model; `ai.image` 503s until it is set or `model` is passed |
| `SHARED_AI_RATE` | AI requests per minute per site (default 30, burst 10, 0 disables) |

Do not hardcode model names in site code unless the user wants a per-call
override. A model the gateway does not serve fails with a 400 at request time.
Over the rate limit the call fails with 429, which is the server refusing, not
a bug in the site.

### shared.uploads

```js
const { url } = await shared.uploads.upload(fileInput.files[0]);
img.src = url;   // served from /uploads/<site>/<rand>-<name>
```

### shared.ws

Per-site broadcast channels. A message is relayed to every *other* member of the
same channel — not echoed back to the sender.

```js
const room = shared.ws.channel('lobby');   // default channel: 'default'
room.onMessage(msg => console.log(msg));    // JSON-parsed, or raw string
room.send({ hello: 'all' });                // objects are JSON-stringified
room.close();
```

`onMessage` is a method; `room.onmessage = fn` does nothing. Call it more than
once to register several listeners, and every listener gets each message. Sends
issued before the socket is open are dropped (there is no send queue), so send
after `onMessage` starts firing. The channel auto-reconnects on close.

### shared.identity

```js
const me = await shared.identity();   // { email, name }
```

## Deploy flow

```sh
shared init [dir]              # scaffold index.html + this skill (skips existing)
shared deploy <dir> --name mysite
```

Deploy packs the directory (dotfiles and `node_modules` excluded) into a gzipped
tarball and POSTs it. The site goes live immediately at
`http://<name>.<base-host><port>/` — e.g. `http://mysite.localhost:8787/`.
`--name` defaults to the lowercased directory base name; `--server` overrides
the target (default `http://localhost:8787`, or `$SHARED_SERVER`). The base host
lists every deployed site on its homepage.

Deploys are attributed (git email if configured, plus `user@hostname`) and
guarded against overwriting someone else's deploy: if the site changed since
your last deploy, the CLI asks before overwriting. Non-interactive runs get
"deploy cancelled" — re-run with `--force` if overwriting is intended.

Data is scoped strictly by the first label of the request Host, so one site
cannot reach another's db/uploads/ws. Site names must match
`^[a-z0-9][a-z0-9-]{0,62}$`.

## Managing sites

```sh
shared list                # deployed sites with size, views, last deployer
shared open mysite         # print + open the site URL
shared versions mysite     # saved prior deploys, newest first
shared rollback mysite     # swap in the newest version (reversible)
shared rm mysite           # delete the site, its db, uploads, and versions
shared backup [file]       # download a gzipped tarball of all server data
```

Each replacement deploy keeps the previous copy as a version (default 3 per
site, `SHARED_KEEP_VERSIONS`); rollback restores the newest and keeps the
current as a new version, so it is reversible.

## Tips

- Keep the site static and let `shared.db`/`ai`/`uploads`/`ws` be the backend.
- Build the whole feature client-side; there is no server code to add.
- Use `subscribe` for live UIs instead of polling.
- Smoke-test a platform call against the real server before blaming the site:
  `curl -X POST http://mysite.<base-host>/api/db/<collection> -H 'Content-Type:
  application/json' -d '{}'`, and the same for `/api/ai/chat`.
- This file is served by the running server at `/skill.md`. Fetch it from there
  to match the deployed version: `curl $SHARED_SERVER/skill.md`.
