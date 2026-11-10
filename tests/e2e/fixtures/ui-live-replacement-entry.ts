import { createUILiveClient } from '../../../web/router-ui-react/src/transport/ui-live-client';

const params = new URLSearchParams(location.search);
const events: any[] = [];
const sockets: WebSocket[] = [];
const NativeSocket = window.WebSocket;
// Keep the browser's native WebSocket and expose only its lifecycle to the test.
window.WebSocket = class extends NativeSocket {
  constructor(url: string | URL, protocols?: string | string[]) {
    super(url, protocols);
    sockets.push(this);
    const index = sockets.length;
    events.push({ kind: 'socket', index, at: performance.now() });
    this.addEventListener('open', () => events.push({ kind: 'open', index }));
    this.addEventListener('close', () => events.push({ kind: 'close', index, at: performance.now() }));
  }
};
let replaced = false;
const replace = () => {
  if (replaced) return;
  replaced = true;
  sockets[0].close(1000, 'replace during callback');
  events.push({ kind: 'replace', state: sockets[0].readyState });
  client.connect();
  events.push({ kind: 'after-replace', count: sockets.length });
};
const client = createUILiveClient({
  routerUrl: params.get('router')!, connectTimeoutMs: 1000, retryDelayMs: 50,
  onFrame: frame => {
    if (frame.type === 'managed_agents_snapshot') {
      events.push({ kind: 'frame', version: frame.projection_version, index: sockets.length });
      if (params.get('callback') === 'onFrame') replace();
    }
  },
  onConnect: () => events.push({ kind: 'connected', index: sockets.length }),
  onError: error => events.push({ kind: 'error', cause: (error as any).cause, at: performance.now() }),
  onRecovery: () => events.push({ kind: 'recovery' }),
});
client.subscribe?.(event => {
  if (typeof event !== 'string' && event.type === 'managed_agents_snapshot') {
    events.push({ kind: 'first-subscriber', version: event.projection_version, index: sockets.length });
    if (params.get('callback') === 'subscriber') replace();
  }
});
client.subscribe?.(event => {
  if (typeof event === 'string') events.push({ kind: 'subscriber', event, index: sockets.length });
  else if (event.type === 'managed_agents_snapshot') events.push({ kind: 'subscriber', version: event.projection_version, index: sockets.length });
});
(window as any).evidence = { events, sockets, client };
client.connect();
