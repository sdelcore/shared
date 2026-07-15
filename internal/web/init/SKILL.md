---
name: shared-sites
description: Building and deploying static sites/apps on the self-hosted shared platform — its /shared.js client API (document DB, AI chat, uploads, websocket channels, identity) and the deploy/rollback flow.
---

# shared-sites

Build a site as plain static files (an `index.html` plus whatever assets), add
`<script src="/shared.js"></script>`, and deploy the directory. The server hosts
each site at its own subdomain and gives every page a client API scoped to that
site automatically.

**No auth.** Single user, trusted LAN only. Anyone who can reach the server can
read and write every site's data. Do not expose it to the open internet.

## Client API (`/shared.js` → `window.shared`)

All calls are promise-based and scoped to the current site by its subdomain.

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

`subscribe` takes a handlers object (not a callback); each handler receives the
doc. It opens a websocket that auto-reconnects (1s backoff) on drop.

### shared.ai

Proxy to an OpenAI-compatible chat API; the key stays on the server. Returns the
reply text (a string).

```js
const reply = await shared.ai.chat('Summarize: ...');
const reply2 = await shared.ai.chat(
  [{ role: 'user', content: 'hi' }],
  { system: 'Be terse.', model: 'some-model', max_tokens: 256 },
);
```

Needs `OPENAI_BASE_URL` and `OPENAI_API_KEY` set on the server, else it errors.

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

Sends issued before the socket is open are dropped (no send queue); it
auto-reconnects on close, so send after `onMessage` starts firing.

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
the target (default `http://localhost:8787`, or `$SHARED_SERVER`).

Deploys are attributed (git email if configured, plus `user@hostname`) and
guarded against overwriting
someone else's deploy: if the site changed since your last deploy, the CLI
asks before overwriting. Non-interactive runs get "deploy cancelled" —
re-run with `--force` if overwriting is intended.

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
