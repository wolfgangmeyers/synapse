import { writeFileSync } from "node:fs";
export const app = { setName() {}, whenReady: () => Promise.resolve(), on() {}, quit() {} };
export class BrowserWindow {
  constructor(options) { this.options = options; }
  once() {}
  on() {}
  async loadURL(url) { writeFileSync(process.env.DESKTOP_CAPTURE, JSON.stringify({ url, options: this.options })); }
}
