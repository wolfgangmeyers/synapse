import { app, BrowserWindow } from "electron";

import { startRouterWebUi } from "../../tools/router_web_ui.mjs";
import { APP_NAME, DESKTOP_DEFAULT_AGENT_ID, DESKTOP_UI_PORT, iconPngPath, parseDesktopArguments, WINDOW_HEIGHT, WINDOW_WIDTH } from "./config.mjs";

let uiServer;
let mainWindow;

function usage() {
  return "Usage: electron . [-- --router http://127.0.0.1:8765]";
}

async function createMainWindow() {
  const options = parseDesktopArguments(process.argv.slice(2));
  if (options.help) {
    console.log(usage());
    app.quit();
    return;
  }

  uiServer = await startRouterWebUi({ host: "127.0.0.1", port: DESKTOP_UI_PORT, router: options.router, defaultAgentId: DESKTOP_DEFAULT_AGENT_ID, output: console.log });
  mainWindow = new BrowserWindow({
    title: APP_NAME,
    width: WINDOW_WIDTH,
    height: WINDOW_HEIGHT,
    minWidth: 920,
    minHeight: 620,
    icon: iconPngPath,
    show: false,
    webPreferences: {
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  });
  mainWindow.once("ready-to-show", () => mainWindow?.show());
  mainWindow.on("closed", () => { mainWindow = undefined; });
  await mainWindow.loadURL(uiServer.httpUrl);
}

app.name = APP_NAME;
app.setName(APP_NAME);

app.whenReady().then(() => {
  void createMainWindow().catch((error) => {
    console.error(`Mecha failed to start: ${error.message}`);
    app.quit();
  });
});

app.on("activate", () => {
  if (BrowserWindow.getAllWindows().length === 0) {
    void createMainWindow().catch((error) => {
      console.error(`Mecha failed to start: ${error.message}`);
      app.quit();
    });
  }
});

app.on("window-all-closed", () => {
  app.quit();
});

app.on("before-quit", async (event) => {
  if (uiServer === undefined) return;
  event.preventDefault();
  const server = uiServer;
  uiServer = undefined;
  try {
    await server.close();
  } catch (error) {
    console.error(`Mecha UI server shutdown failed: ${error.message}`);
  } finally {
    app.quit();
  }
});
