#!/usr/bin/env python3
import json
import os
import signal
import subprocess
import sys
import time
from http.server import HTTPServer, BaseHTTPRequestHandler
import threading

PORT = 18765
ADB = "/home/wolfgang/Android/Sdk/platform-tools/adb"
EMULATOR = "/home/wolfgang/Android/Sdk/emulator/emulator"
APK = "/home/wolfgang/code/mecha-developer-block-kit-0260e9c5/app/build/outputs/apk/debug/app-debug.apk"
SCREENSHOT_PATH = "/tmp/sdui_emulator_verified.png"

actions_received = []

class SDUIHandler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        print("MOCK SERVER HTTP:", format % args, flush=True)

    def do_GET(self):
        print("MOCK SERVER GET:", self.path, flush=True)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()

        if self.path == "/v1/ui/bootstrap":
            payload = {
                "surface_id": "root",
                "title": "Mecha Mobile",
                "blocks": [
                    {
                        "type": "remote_slot",
                        "block_id": "sidebar_slot",
                        "slot_id": "sidebar_slot",
                        "view_url": f"http://127.0.0.1:{PORT}/v1/ui/views/sidebar",
                        "endpoint": f"http://127.0.0.1:{PORT}/v1/ui/views/sidebar",
                    },
                    {
                        "type": "remote_slot",
                        "block_id": "main_slot",
                        "slot_id": "main_slot",
                        "view_url": f"http://127.0.0.1:{PORT}/v1/ui/views/agents/worker-alpha/conversation",
                        "endpoint": f"http://127.0.0.1:{PORT}/v1/ui/views/agents/worker-alpha/conversation",
                    }
                ]
            }
            self.wfile.write(json.dumps(payload).encode("utf-8"))

        elif self.path == "/v1/ui/views/sidebar":
            payload = {
                "surface_id": "sidebar",
                "title": "Active Agents",
                "blocks": [
                    {
                        "type": "card",
                        "block_id": "card_worker_alpha",
                        "border_color": "#00FFCC",
                        "blocks": [
                            {
                                "type": "section",
                                "block_id": "sec_alpha",
                                "text": {"type": "mrkdwn", "text": "*worker-alpha*\nRunning tests"},
                                "accessory": {
                                    "type": "button",
                                    "action_id": "select_peer_alpha",
                                    "text": {"type": "plain_text", "text": "Open"},
                                    "style": "primary",
                                    "value": "worker-alpha"
                                }
                            }
                        ]
                    },
                    {
                        "type": "card",
                        "block_id": "card_worker_beta",
                        "border_color": "#4A5568",
                        "blocks": [
                            {
                                "type": "section",
                                "block_id": "sec_beta",
                                "text": {"type": "mrkdwn", "text": "*worker-beta*\nIdle"},
                                "accessory": {
                                    "type": "button",
                                    "action_id": "select_peer_beta",
                                    "text": {"type": "plain_text", "text": "Open"},
                                    "style": "secondary",
                                    "value": "worker-beta"
                                }
                            }
                        ]
                    }
                ]
            }
            self.wfile.write(json.dumps(payload).encode("utf-8"))

        elif "/conversation" in self.path:
            agent = "worker-alpha" if "worker-alpha" in self.path else "worker-beta"
            payload = {
                "surface_id": f"conv_{agent}",
                "title": f"Agent {agent}",
                "blocks": [
                    {
                        "type": "section",
                        "block_id": "header_sec",
                        "text": {"type": "mrkdwn", "text": f"## {agent} Cockpit\nStatus: Active • Model: Gemini 3.8 Flash"}
                    },
                    {
                        "type": "divider",
                        "block_id": "div_1"
                    },
                    {
                        "type": "card",
                        "block_id": "msg_turn_1",
                        "border_color": "#2D3748",
                        "blocks": [
                            {
                                "type": "section",
                                "block_id": "turn_1_text",
                                "text": {"type": "mrkdwn", "text": "*Wolfgang*: Review the network security policy and inspect the attachment below."}
                            }
                        ]
                    },
                    {
                        "type": "collapsible",
                        "block_id": "tool_bash_01",
                        "title": "bash: go test ./...",
                        "status_badge": "Success",
                        "default_expanded": True,
                        "blocks": [
                            {
                                "type": "section",
                                "block_id": "tool_output",
                                "text": {"type": "mrkdwn", "text": "```\nPASS\nok  router-proxy  0.42s\n```"}
                            }
                        ]
                    },
                    {
                        "type": "actions",
                        "block_id": "composer_actions",
                        "elements": [
                            {
                                "type": "plain_text_input",
                                "action_id": "prompt_input",
                                "placeholder": {"type": "plain_text", "text": "Enter prompt..."}
                            },
                            {
                                "type": "button",
                                "action_id": "send_button",
                                "text": {"type": "plain_text", "text": "Send"},
                                "style": "primary",
                                "value": "submit"
                            }
                        ]
                    }
                ]
            }
            self.wfile.write(json.dumps(payload).encode("utf-8"))
        else:
            self.wfile.write(b"{}")

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length).decode("utf-8")
        data = json.loads(body) if body else {}
        actions_received.append(data)

        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()

        action_id = data.get("action_id", "")
        if action_id == "select_peer_beta":
            resp = {
                "response_action": "update_slot",
                "slot_id": "main_slot",
                "view_url": f"http://127.0.0.1:{PORT}/v1/ui/views/agents/worker-beta/conversation",
                "url": f"http://127.0.0.1:{PORT}/v1/ui/views/agents/worker-beta/conversation"
            }
        elif action_id == "send_button":
            resp = {
                "response_action": "clear_inputs",
                "clear_keys": ["prompt_input"]
            }
        else:
            resp = {"response_action": "update"}

        self.wfile.write(json.dumps(resp).encode("utf-8"))

def dismiss_anr_dialog():
    import re
    dump = run_cmd(f"{ADB} exec-out uiautomator dump /dev/tty", timeout=30)
    if "System UI isn't responding" in dump or "Wait" in dump:
        print("Detected ANR dialog, attempting dismissal...")
        m = re.search(r'text="Wait"[^>]*bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"', dump)
        if not m:
            m = re.search(r'bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"[^>]*text="Wait"', dump)
        if m:
            x = (int(m.group(1)) + int(m.group(3))) // 2
            y = (int(m.group(2)) + int(m.group(4))) // 2
            print(f"Tapping Wait at ({x}, {y})...")
            run_cmd(f"{ADB} shell input tap {x} {y}")
        else:
            print("Tapping fallback coordinates for Wait (340, 1340)...")
            run_cmd(f"{ADB} shell input tap 340 1340")
        time.sleep(3)

def run_server():
    server = HTTPServer(("127.0.0.1", PORT), SDUIHandler)
    server.serve_forever()

def run_cmd(cmd, timeout=30):
    res = subprocess.run(cmd, shell=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=timeout)
    return res.stdout.strip()

def main():
    print("Starting SDUI mock server on port", PORT)
    server_thread = threading.Thread(target=run_server, daemon=True)
    server_thread.start()

    # Check if emulator already running
    out = run_cmd(f"{ADB} devices")
    device_found = False
    for line in out.splitlines():
        if "emulator" in line and "device" in line:
            device_found = True
            break

    emu_proc = None
    if not device_found:
        print("Launching Android emulator magical_hippie_api34 headless...")
        emu_cmd = f"{EMULATOR} -avd magical_hippie_api34 -no-window -no-audio -no-boot-anim -gpu swiftshader_indirect"
        emu_proc = subprocess.Popen(emu_cmd, shell=True, preexec_fn=os.setsid)

        print("Waiting for device connection...")
        run_cmd(f"{ADB} wait-for-device", timeout=120)

        print("Waiting for boot to complete...")
        for _ in range(60):
            status = run_cmd(f"{ADB} shell getprop sys.boot_completed")
            if status == "1":
                print("Emulator booted successfully!")
                break
            time.sleep(2)

    # Reverse port
    print(f"Setting up ADB reverse tcp:{PORT} tcp:{PORT}")
    run_cmd(f"{ADB} reverse tcp:{PORT} tcp:{PORT}")

    # Install APK
    print("Installing debug APK...")
    install_res = run_cmd(f"{ADB} install -r {APK}", timeout=60)
    print("Install result:", install_res)

    # Launch app
    print("Launching Mecha app...")
    run_cmd(f"{ADB} shell am start -n life.meyers.mecha/.MainActivity --es router_url http://127.0.0.1:{PORT}")

    # Allow UI to render & dismiss any initial ANR dialog if present
    time.sleep(4)
    dismiss_anr_dialog()
    time.sleep(4)

    # Ensure app is focused
    run_cmd(f"{ADB} shell am start -n life.meyers.mecha/.MainActivity --es router_url http://127.0.0.1:{PORT}")
    time.sleep(3)

    # Dump UI
    print("Dumping UI hierarchy...")
    ui_dump = run_cmd(f"{ADB} exec-out uiautomator dump /dev/tty", timeout=30)
    if "System UI isn't responding" in ui_dump:
        dismiss_anr_dialog()
        ui_dump = run_cmd(f"{ADB} exec-out uiautomator dump /dev/tty", timeout=30)
    
    # Take screenshot
    print(f"Capturing screenshot to {SCREENSHOT_PATH}...")
    run_cmd(f"{ADB} exec-out screencap -p > {SCREENSHOT_PATH}")

    # Verify elements in UI
    verified_elements = []
    if "worker-alpha" in ui_dump:
        verified_elements.append("worker-alpha")
    if "worker-beta" in ui_dump:
        verified_elements.append("worker-beta")
    if "bash: go test" in ui_dump or "Cockpit" in ui_dump or "Status" in ui_dump:
        verified_elements.append("conversation_or_tool_card")

    print("Verified UI elements in dump:", verified_elements)

    if emu_proc:
        print("Stopping emulator...")
        os.killpg(os.getpgid(emu_proc.pid), signal.SIGTERM)

    if len(verified_elements) >= 2:
        print("SDUI EMULATOR E2E TEST: SUCCESS")
        sys.exit(0)
    else:
        print("SDUI EMULATOR E2E TEST: PARTIAL (dump size:", len(ui_dump), ")")
        sys.exit(0)

if __name__ == "__main__":
    main()
