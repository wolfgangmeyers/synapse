import "@fortawesome/fontawesome-free/css/all.min.css";
import { createRoot } from "react-dom/client";
import App from "./App";

const rootElement = document.getElementById("root");

function browserStorage(): Storage | undefined {
  try {
    return globalThis.localStorage;
  } catch {
    return undefined;
  }
}

if (rootElement !== null) {
  const storage = browserStorage();
  createRoot(rootElement).render(<App {...(storage === undefined ? {} : { storage })} />);
}
