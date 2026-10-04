/// <reference types="vite/client" />

declare module "node:path" {
  export function resolve(...paths: string[]): string;
}

declare module "node:fs/promises" {
  export function readFile(path: string, options?: unknown): Promise<string | Uint8Array>;
  export function writeFile(path: string, data: unknown, options?: unknown): Promise<void>;
  export function unlink(path: string): Promise<void>;
  export function rm(path: string, options?: unknown): Promise<void>;
  export function mkdir(path: string, options?: unknown): Promise<void>;
}

declare namespace NodeJS {
  interface ProcessEnv {
    [key: string]: string | undefined;
  }
}

declare var process: {
  env: NodeJS.ProcessEnv;
  cwd(): string;
};

