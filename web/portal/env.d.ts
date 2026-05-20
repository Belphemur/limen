/// <reference types="vite/client" />

declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  // Generic component shape — we intentionally don't introspect props here;
  // each .vue file declares its own typed props via defineProps.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any, @typescript-eslint/no-empty-object-type
  const component: DefineComponent<{}, {}, any>
  export default component
}

// Side-effect imports for self-hosted variable fonts. The packages ship CSS
// without TS type declarations, so we stub them as empty modules.
declare module '@fontsource-variable/inter'
declare module '@fontsource-variable/outfit'
