import { app, BrowserWindow } from "electron";

import { APP_NAME, DESKTOP_DEFAULT_AGENT_ID, DESKTOP_UI_PORT, iconPngPath, parseDesktopArguments, WINDOW_HEIGHT, WINDOW_WIDTH } from "./config.mjs";

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
  await mainWindow.loadURL(process.env.MECHA_WEBUI_URL || `http://127.0.0.1:${process.env.MECHA_WEBUI_PORT || DESKTOP_UI_PORT}?router=${encodeURIComponent(options.router)}&agent=${encodeURIComponent(DESKTOP_DEFAULT_AGENT_ID)}`);
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
