import tseslint from "typescript-eslint";

export default tseslint.config({
  // Lint authored TypeScript only; mailbox-client.js is generated output.
  ignores: ["**/*.js"],
  files: ["**/*.ts"],
  languageOptions: {
    parser: tseslint.parser,
    parserOptions: {
      projectService: true,
      tsconfigRootDir: import.meta.dirname,
    },
  },
  plugins: {
    "@typescript-eslint": tseslint.plugin,
  },
  rules: {
    // TypeScript's explicit escape hatch is prohibited in this package.
    "@typescript-eslint/no-explicit-any": "error",
    // Assertions may not be used to claim a narrower type without validation.
    "@typescript-eslint/no-unsafe-type-assertion": "error",
  },
});
