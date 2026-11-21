// The project carries no @types/node (the app runs in the browser); the
// theme-hygiene test reads carbon.scss from disk with the one fs function
// it needs. Ambient declaration — a script-context .d.ts, not an
// augmentation, so it works without the node types package.
declare module 'fs' {
  export function readFileSync(path: string, encoding: 'utf8'): string
}
