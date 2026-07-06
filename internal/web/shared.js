/* shared.js — client SDK for shared sites. Served at /shared.js on every site. */
(function () {
  'use strict';

  async function request(path, options) {
    const res = await fetch(path, options);
    if (!res.ok) {
      let msg = res.status + ' ' + res.statusText;
      try {
        const body = await res.json();
        if (body && body.error) msg = body.error;
      } catch (_) {}
      throw new Error(msg);
    }
    if (res.status === 204) return null;
    return res.json();
  }

  function json(method, path, body) {
    return request(path, {
      method,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
  }

  function wsURL(path) {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    return proto + '//' + location.host + path;
  }

  function reconnectingSocket(url, onMessage, onOpen) {
    const MAX_QUEUE = 100;
    let ws = null;
    let closed = false;
    let everOpened = false;
    const queue = [];

    function flush() {
      while (queue.length && ws && ws.readyState === WebSocket.OPEN) {
        ws.send(queue.shift());
      }
    }

    function connect() {
      if (closed) return;
      ws = new WebSocket(url);
      ws.onopen = function () {
        flush();
        if (onOpen) onOpen(everOpened);
        everOpened = true;
      };
      ws.onmessage = onMessage;
      ws.onclose = function () {
        ws = null;
        if (!closed) setTimeout(connect, 1000);
      };
    }
    connect();

    return {
      send(data) {
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send(data);
        } else {
          if (queue.length >= MAX_QUEUE) queue.shift();
          queue.push(data);
        }
      },
      close() {
        closed = true;
        queue.length = 0;
        if (ws) ws.close();
      },
    };
  }

  const db = {
    collection(name) {
      const base = '/api/db/' + encodeURIComponent(name);
      return {
        async create(data) {
          return json('POST', base, data);
        },
        async list() {
          const body = await request(base);
          return body.docs || [];
        },
        async get(id) {
          return request(base + '/' + encodeURIComponent(id));
        },
        async update(id, data) {
          return json('PUT', base + '/' + encodeURIComponent(id), data);
        },
        async delete(id) {
          return request(base + '/' + encodeURIComponent(id), { method: 'DELETE' });
        },
        subscribe(handlers) {
          handlers = handlers || {};
          // Track id -> updatedAt so we can detect changes missed while the
          // socket was disconnected and replay them on reconnect.
          const snapshot = new Map();

          async function resync(isReconnect) {
            let docs;
            try {
              docs = await request(base);
            } catch (_) {
              return;
            }
            docs = (docs && docs.docs) || [];
            const seen = new Set();
            for (const doc of docs) {
              seen.add(doc.id);
              const prev = snapshot.get(doc.id);
              snapshot.set(doc.id, doc.updatedAt);
              if (!isReconnect) continue;
              if (prev === undefined) {
                if (handlers.onCreate) handlers.onCreate(doc);
              } else if (prev !== doc.updatedAt) {
                if (handlers.onUpdate) handlers.onUpdate(doc);
              }
            }
            for (const id of Array.from(snapshot.keys())) {
              if (seen.has(id)) continue;
              snapshot.delete(id);
              if (isReconnect && handlers.onDelete) handlers.onDelete({ id });
            }
          }

          const sock = reconnectingSocket(wsURL(base + '/subscribe'), function (msg) {
            let event;
            try {
              event = JSON.parse(msg.data);
            } catch (_) {
              return;
            }
            const doc = event.doc || {};
            if (event.type === 'created') {
              snapshot.set(doc.id, doc.updatedAt);
              if (handlers.onCreate) handlers.onCreate(doc);
            } else if (event.type === 'updated') {
              snapshot.set(doc.id, doc.updatedAt);
              if (handlers.onUpdate) handlers.onUpdate(doc);
            } else if (event.type === 'deleted') {
              snapshot.delete(doc.id);
              if (handlers.onDelete) handlers.onDelete(doc);
            }
          }, resync);
          return { close: sock.close };
        },
      };
    },
  };

  const ai = {
    async chat(messages, opts) {
      opts = opts || {};
      if (typeof messages === 'string') {
        messages = [{ role: 'user', content: messages }];
      }
      const body = { messages };
      if (opts.system) body.system = opts.system;
      if (opts.model) body.model = opts.model;
      if (opts.max_tokens) body.max_tokens = opts.max_tokens;
      const res = await json('POST', '/api/ai/chat', body);
      return res.content;
    },
  };

  const uploads = {
    async upload(file) {
      const form = new FormData();
      form.append('file', file);
      return request('/api/uploads', { method: 'POST', body: form });
    },
  };

  const ws = {
    channel(name) {
      const url = wsURL('/api/ws?channel=' + encodeURIComponent(name || 'default'));
      const listeners = [];
      const sock = reconnectingSocket(url, function (msg) {
        let data;
        try {
          data = JSON.parse(msg.data);
        } catch (_) {
          data = msg.data;
        }
        listeners.forEach(function (fn) { fn(data); });
      });
      return {
        send(obj) {
          sock.send(JSON.stringify(obj));
        },
        onMessage(fn) {
          listeners.push(fn);
        },
        close: sock.close,
      };
    },
  };

  window.shared = {
    db,
    ai,
    uploads,
    ws,
    identity() {
      return request('/api/identity');
    },
  };
})();
