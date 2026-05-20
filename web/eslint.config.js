import {
  defineConfigWithVueTs,
  vueTsConfigs,
} from "@vue/eslint-config-typescript";
import pluginVue from "eslint-plugin-vue";
import prettier from "@vue/eslint-config-prettier";

// Single flat-config; Phase 9b deliberately keeps one shared lint
// config across the SPA (no per-folder overrides).
export default defineConfigWithVueTs(
  {
    name: "app/files-to-lint",
    files: ["**/*.{ts,mts,tsx,vue}"],
  },
  {
    name: "app/files-to-ignore",
    ignores: [
      "**/dist/**",
      "**/node_modules/**",
      "src/gen/**",
      "**/coverage/**",
      "eslint.config.js",
      "e2e/**",
      "playwright.config.ts",
      "playwright-report/**",
      "test-results/**",
    ],
  },
  pluginVue.configs["flat/recommended"],
  vueTsConfigs.recommendedTypeChecked,
  {
    languageOptions: {
      parserOptions: {
        projectService: true,
      },
    },
    rules: {
      "vue/multi-word-component-names": "off",
      // The generated Connect-RPC clients use `unknown` heavily for
      // protobuf field arrays; allow that without nagging.
      "@typescript-eslint/no-unsafe-assignment": "off",
    },
  },
  prettier,
);
