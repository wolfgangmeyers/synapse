#!/usr/bin/env python3
"""Opt-in exploratory contract: run only inside the isolated wrapper."""
import http.server, json, os, socket, subprocess, threading, sys

class Provider(http.server.BaseHTTPRequestHandler):
    requests = []
    forged_at = []
    def log_message(self, *args): pass
    def do_POST(self):
        n = int(self.headers.get('content-length', '0'))
        if n > 1024 * 1024:
            self.send_error(413); return
        body = self.rfile.read(n)
        try: request = json.loads(body)
        except ValueError: self.send_error(400); return
        self.requests.append((self.path, request))
        response = {'id':'msg_test','type':'message','role':'assistant','model':'claude-sonnet-4-5','content':[{'type':'text','text':'MOCK_OK'}], 'stop_reason':'end_turn','stop_sequence':None,'usage':{'input_tokens':10,'output_tokens':2}}
        # Only the exact user prompt for this experiment may trigger the forge.
        messages = request.get('messages', [])
        if self.path.startswith('/v1/messages') and isinstance(messages, list) and any(
            isinstance(m, dict) and m.get('role') == 'user' and (
                m.get('content') == 'FORGE_BASH' or
                isinstance(m.get('content'), list) and any(
                    isinstance(c, dict) and c.get('type') == 'text' and c.get('text') == 'FORGE_BASH'
                    for c in m['content'])) for m in messages
        ) and not any(isinstance(m, dict) and m.get('role') == 'assistant' and isinstance(m.get('content'), list) and any(
            isinstance(c, dict) and c.get('type') == 'tool_use' and c.get('id') == 'toolu_test123'
            for c in m['content']) for m in messages):
            response['content'] = [{'type':'tool_use','id':'toolu_test123','name':'Bash','input':{'command':'printf executed > /tmp/bash-executed'}}]
            response['stop_reason'] = 'tool_use'
            type(self).forged_at.append(len(self.requests) - 1)
        data = json.dumps(response).encode()
        self.send_response(200); self.send_header('Content-Type','application/json'); self.send_header('Content-Length',str(len(data))); self.end_headers(); self.wfile.write(data)

if os.environ.get('MECHA_ISOLATED_CONTRACT') != '1':
    sys.exit('Run through run-claude-isolation.sh; direct execution forbidden')
# Fail closed before launching the CLI if namespace isolation is missing.
if os.readlink('/proc/self/ns/net') == os.environ.get('HOST_NET_NS'):
    sys.exit('Network namespace is not isolated')
with open('/proc/net/dev') as interfaces:
    names = {line.split(':', 1)[0].strip() for line in interfaces if ':' in line}
    if names != {'lo'}: sys.exit('Unexpected network interfaces: ' + repr(names))
with open('/proc/net/route') as routes:
    if len(routes.readlines()) != 1: sys.exit('Unexpected IPv4 route')
with open('/proc/net/ipv6_route') as routes:
    if any(line.split()[-1] != 'lo' for line in routes): sys.exit('Unexpected IPv6 route')
# Documentation-only address: no packet can leave without a route.
sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
try:
    try: sock.connect(('192.0.2.1', 9))
    except OSError: pass
    else: sys.exit('External route unexpectedly available')
finally: sock.close()
version = subprocess.run(['/claude','--version'], capture_output=True, text=True, timeout=10, check=True).stdout.strip()
if version != '2.1.316 (Claude Code)': sys.exit('Unsupported Claude version: ' + version)
print('Isolated namespace and CLI version verified:', version, flush=True)
server = http.server.ThreadingHTTPServer(('127.0.0.1',0), Provider)
threading.Thread(target=server.serve_forever, daemon=True).start()
env = {'PATH':'/usr/bin:/bin', 'HOME':'/tmp/home', 'TMPDIR':'/tmp', 'ANTHROPIC_API_KEY':'sk-ant-synthetic-test-only', 'ANTHROPIC_BASE_URL':'http://127.0.0.1:%d' % server.server_port, 'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC':'1', 'DISABLE_TELEMETRY':'1', 'DISABLE_AUTOUPDATER':'1'}
os.makedirs(env['HOME'], mode=0o700, exist_ok=True)
# Avoid first-run onboarding; no real account or credential material.
with open('/tmp/home/.claude.json','w') as f: json.dump({'hasCompletedOnboarding':True},f)

# A user-level stdio MCP canary records startup and protocol requests.
mcp = r'''import json, sys
with open("/tmp/mcp-started", "a") as f: f.write("start\n")
for line in sys.stdin:
    msg = json.loads(line)
    with open("/tmp/mcp-methods", "a") as f: f.write(str(msg.get("method")) + "\n")
    if "id" not in msg: continue
    method = msg.get("method")
    if method == "initialize": result = {"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"test","version":"1"}}
    elif method == "tools/list": result = {"tools":[{"name":"canary","description":"test canary","inputSchema":{"type":"object","properties":{}}}]}
    else: result = {}
    print(json.dumps({"jsonrpc":"2.0","id":msg["id"],"result":result}), flush=True)
'''
with open('/tmp/mcp.py', 'w') as f: f.write(mcp)

def run(label, tools, strict=True, forge=False):
    start = len(Provider.requests)
    forged_start = len(Provider.forged_at)
    args = ['/claude','-p','--model','sonnet','--tools',tools,'--disable-slash-commands','--no-session-persistence','--system-prompt','Return only the requested goal verdict.','--output-format','json']
    if strict: args.append('--strict-mcp-config')
    if forge: args += ['--permission-mode','bypassPermissions']
    p = subprocess.run(args + ['FORGE_BASH' if forge else 'Say hello'], env=env, cwd='/tmp', capture_output=True, text=True, timeout=45)
    requests = Provider.requests[start:]
    print(label, 'exit=',p.returncode,'paths=',[x[0] for x in requests], flush=True)
    if p.returncode: print('CLI stderr:', p.stderr[:1000], flush=True)
    if p.returncode or not requests: raise RuntimeError(label + ': CLI did not complete a mock request')
    if forge:
        forged_indices = Provider.forged_at[forged_start:]
        consumed = False
        for index in forged_indices:
            for path, request in Provider.requests[index + 1:]:
                if not path.startswith('/v1/messages'): continue
                history = request.get('messages', [])
                if not isinstance(history, list): continue
                saw_tool_use = False
                for message in history:
                    if not isinstance(message, dict) or not isinstance(message.get('content'), list): continue
                    if message.get('role') == 'user' and saw_tool_use and any(
                        isinstance(block, dict) and block.get('type') == 'tool_result' and block.get('tool_use_id') == 'toolu_test123'
                        for block in message['content']):
                        consumed = True
                    if message.get('role') == 'assistant' and any(
                        isinstance(block, dict) and block.get('type') == 'tool_use' and block.get('id') == 'toolu_test123' and block.get('name') == 'Bash' and isinstance(block.get('input'), dict) and block['input'].get('command') == 'printf executed > /tmp/bash-executed'
                        for block in message['content']):
                        saw_tool_use = True
        print(label, 'forged requests:', len(forged_indices), 'matching tool_result follow-up:', consumed, flush=True)
        if not consumed: raise RuntimeError(label + ': no subsequent matching tool_result; forged response consumption unproven')
    messages = [r for path,r in requests if path.startswith('/v1/messages')]
    try: answer = json.loads(p.stdout)
    except ValueError: raise RuntimeError(label + ': invalid JSON answer')
    if not messages or not isinstance(answer, dict) or answer.get('result') != 'MOCK_OK': raise RuntimeError(label + ': no mock answer')
    return {tool.get('name') for r in messages for tool in r.get('tools',[]) if isinstance(tool,dict)}
try:
    for label, tools, expected in [('execution-positive', 'Bash', True), ('execution-restricted', '', False)]:
        try: os.unlink('/tmp/bash-executed')
        except FileNotFoundError: pass
        run(label, tools, forge=True)
        actual = os.path.exists('/tmp/bash-executed')
        print(label, 'Bash marker:', actual, flush=True)
        if actual != expected: raise RuntimeError(label + ': unexpected execution result')
    control = run('positive-control', 'Bash')
    isolated = run('isolated', '')
    print('positive tools:',sorted(control),'isolated tools:',sorted(isolated))
    if 'Bash' not in control: raise RuntimeError('positive control did not expose Bash')
    if isolated: raise RuntimeError('isolated run exposed tools')
    with open('/tmp/home/.claude.json', 'w') as f:
        json.dump({'hasCompletedOnboarding':True, 'mcpServers':{'isolationcanary':{'type':'stdio','command':'/usr/bin/python3','args':['/tmp/mcp.py']}}}, f)
    def markers():
        def lines(path):
            try:
                with open(path) as f: return f.read().splitlines()
            except FileNotFoundError: return []
        return lines('/tmp/mcp-started'), lines('/tmp/mcp-methods')
    mcp_control = run('user-mcp-control', '', strict=False)
    started, methods = markers()
    print('MCP control tools:', sorted(mcp_control), 'started:', started, 'methods:', methods, flush=True)
    if 'mcp__isolationcanary__canary' not in mcp_control or not started or 'tools/list' not in methods:
        raise RuntimeError('MCP positive control failed')
    for path in ['/tmp/mcp-started', '/tmp/mcp-methods']: os.unlink(path)
    mcp_strict = run('user-mcp-strict', '')
    started, methods = markers()
    print('MCP strict tools:', sorted(mcp_strict), 'started:', started, 'methods:', methods, flush=True)
    if mcp_strict or started or methods: raise RuntimeError('strict run exposed tools or started user MCP')
    print('PASS: forged Bash comparison, builtin advertisement and user-level MCP comparison')
finally: server.shutdown()
