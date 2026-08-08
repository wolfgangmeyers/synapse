# jira-mcp-cli

A standalone command-line MCP client for Jira through Atlassian's official Rovo MCP server. It talks directly to `https://mcp.atlassian.com/v1/mcp/authv2`; it does **not** add MCP tools to pi, Cursor, Claude Code, or another agent.

## Install / run

```sh
cd ~/code/mecha-developer/tools/jira-mcp-cli
npm install
npm run build
npm link                 # makes `jira-mcp` available on your PATH
jira-mcp login           # opens the Atlassian OAuth flow in your browser
jira-mcp tools
jira-mcp call <tool-name> '{"key":"value"}'
```

Useful commands:

```sh
jira-mcp status
jira-mcp tools --json
jira-mcp call getAccessibleAtlassianResources '{}'
jira-mcp call getVisibleJiraProjects '{"cloudId":"<cloudId-from-accessible-resources>"}'
jira-mcp create-issue ABC Task "Write the release notes" --cloud-id "<cloudId-from-accessible-resources>" --description "Draft and review release notes."
jira-mcp comment ABC-123 "This is ready for review." --cloud-id "<cloudId-from-accessible-resources>"
jira-mcp logout
```

## Authentication

`jira-mcp login` uses Atlassian's OAuth flow for the official Rovo MCP endpoint. It starts a callback server on `127.0.0.1:3391`, opens the Atlassian authorization URL in your default browser, waits for the browser redirect, then stores the returned OAuth tokens locally. If the browser does not open automatically, the command prints the authorization URL so you can open it manually.

Local credentials are stored at:

```txt
${XDG_CONFIG_HOME:-~/.config}/jira-mcp-cli/credentials.json
```

The config directory is created with `0700` permissions and the credentials file is written with `0600` permissions. Do not commit this file or copy it into the repository.

Live commands require:

- An Atlassian Cloud account with Jira access.
- Any required organization/admin enablement for the Atlassian Rovo MCP server.
- Jira project permissions for the action you are running.
- A known `cloudId` value for most Jira tool calls. Run `jira-mcp call getAccessibleAtlassianResources '{}'` after login and use the returned `cloudId` for the site you want to operate on.

## Jira Convenience Commands

The wrapper keeps the generic MCP commands and adds two Jira-specific helpers:

```sh
jira-mcp create-issue <project-key> <issue-type> <summary> \
  --cloud-id "<cloudId-from-accessible-resources>" \
  --description "Markdown description" \
  --field customfield_10000='"Example"' \
  --args '{"additionalProperty":"value"}'

jira-mcp comment <issue-id-or-key> <body> \
  --cloud-id "<cloudId-from-accessible-resources>"
```

By default these call `createJiraIssue` and `addCommentToJiraIssue`. Use `--tool` if Atlassian renames the live tool for your tenant, and use `--args` to merge additional top-level MCP arguments or override generated values. `--field name=json` adds entries under the generated `additional_fields` object.

## Other Practical Operations

The official Jira MCP surface is broader than the convenience commands. Use `jira-mcp tools` to confirm the current names and schemas, then call them directly. Practical operations worth using include:

- `searchJiraIssuesUsingJql` for JQL search.
- `getJiraIssue` for issue details.
- `getVisibleJiraProjects` for project discovery.
- `getJiraProjectIssueTypesMetadata` and `getJiraIssueTypeMetaWithFields` for create/edit metadata.
- `editJiraIssue` for field updates.
- `getTransitionsForJiraIssue` and `transitionJiraIssue` for workflow movement.
- `addWorklogToJiraIssue` for time tracking.
- `createIssueLink` and `getIssueLinkTypes` for issue links.

## Limitations

This first implementation wraps Atlassian's official OAuth MCP endpoint only. It intentionally does not include a Jira REST or API-token fallback because the official MCP server supports Jira search/read/write operations and matches this repository's MCP CLI pattern. If OAuth cannot work in a headless environment or the organization requires API-token authentication, add that as a separate fallback path without storing secrets in the repo.

No live Jira operations are run by tests. Offline tests validate the command shape, local config behavior, not-logged-in failure, and argument construction.

## Ready-to-Test Flow

Use this sequence for a live smoke test once you have an Atlassian account and Jira project permissions:

```sh
cd ~/code/mecha-developer/tools/jira-mcp-cli
npm install
npm test
npm link
jira-mcp login
jira-mcp status
jira-mcp tools --json
jira-mcp call getAccessibleAtlassianResources '{}'
jira-mcp create-issue <PROJECT_KEY> Task "CLI smoke test" --cloud-id "<cloudId-from-accessible-resources>" --description "Created by jira-mcp-cli smoke test."
jira-mcp comment <ISSUE_KEY> "Comment from jira-mcp-cli smoke test." --cloud-id "<cloudId-from-accessible-resources>"
```

The live smoke test still needs the Jira project key, the issue type name accepted by that project, the returned `cloudId`, and an issue key to comment on. The final two commands create and mutate live Jira data.
