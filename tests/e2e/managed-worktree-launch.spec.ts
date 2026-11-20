import { execFileSync } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test, expect } from "@playwright/test";
import { workflowHarness } from "./workflow-implicit-goals-harness";

// WTL-001: immediate worktree option display on folder selection
test("displays create worktree toggle immediately when selecting main worktree", async ({ page }) => {
  test.setTimeout(120_000);

  // Initialize a real git repository as the main worktree fixture
  const gitRoot = await mkdtemp(join(tmpdir(), "worktree-e2e-repo-"));
  execFileSync("git", ["init", "-b", "main"], { cwd: gitRoot });
  execFileSync("git", ["config", "user.email", "test@example.com"], { cwd: gitRoot });
  execFileSync("git", ["config", "user.name", "Worktree Test"], { cwd: gitRoot });
  execFileSync("git", ["commit", "--allow-empty", "-m", "initial commit"], { cwd: gitRoot });

  const f = await workflowHarness();
  try {
    // Seed recent folders with the git repository path
    await page.addInitScript(({ repoDir }) => {
      localStorage.setItem("mecha.web.managedRecentFolders", JSON.stringify([repoDir]));
    }, { repoDir: gitRoot });

    await f.open(page);

    // Open Launch Agent dialog
    await page.getByRole("button", { name: "Managed actions", exact: true }).click();
    await page.getByRole("menuitem", { name: "Launch Agent", exact: true }).click();

    const dialog = page.getByRole("dialog", { name: "Launch managed agent" });
    await expect(dialog).toBeVisible();

    // Select the git repo from Recent folders
    await dialog.getByRole("button", { name: "Recent folders" }).click();
    const folderOption = page.getByRole("option", { name: `Select folder ${gitRoot}` });
    await expect(folderOption).toBeVisible();
    await folderOption.click();

    // Verify "Create a worktree" toggle appears promptly
    const worktreeToggle = dialog.getByRole("button", { name: /Create a worktree/i });
    await expect(worktreeToggle).toBeVisible({ timeout: 5000 });
    expect(await worktreeToggle.getAttribute("aria-pressed")).toBe("false");

    // Enter agent name and verify branch input defaults to slugified name
    await dialog.getByLabel("Name").fill("my-feature-worker");

    // Toggle worktree ON and verify branch input becomes visible with defaulted slug
    await worktreeToggle.click();
    await expect(worktreeToggle).toHaveAttribute("aria-pressed", "true");

    const branchInput = dialog.getByLabel("Branch name");
    await expect(branchInput).toBeVisible();
    await expect(branchInput).toHaveValue("my-feature-worker");

    expect(f.violations).toEqual([]);
  } finally {
    await f.close();
    await rm(gitRoot, { recursive: true, force: true });
  }
});
