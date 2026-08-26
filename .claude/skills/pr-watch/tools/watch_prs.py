#!/usr/bin/env python3
"""Read-only GitHub PR watcher for Roger Labs repositories."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


DEFAULT_REPOS = [
    "Roger-Labs-Inc/roger-emr",
    "Roger-Labs-Inc/Roger-Docs-API",
    "Roger-Labs-Inc/steve-bot",
    "Roger-Labs-Inc/App",
    "Roger-Labs-Inc/interview-mobile-eng",
    "Roger-Labs-Inc/arq-ui",
]

DEFAULT_STATE_DIR = Path.home() / ".mecha-developer" / "pr-watch"
MAILBOX_SEND_V2 = Path(__file__).resolve().parents[2] / "supervisor" / "tools" / "mailbox_send.py"

QUIET_BOT_AUTHORS = {"app/dependabot", "dependabot", "dependabot[bot]"}
SECURITY_KEYWORDS = (
    "auth",
    "oidc",
    "oauth",
    "workos",
    "secret",
    "ssm",
    "kms",
    "credential",
    "registry",
    "sdk",
    "workflow",
    "release",
    "cookbook",
    "terraform",
    "rds",
    "bastion",
)



def env_repos() -> list[str]:
    raw = os.environ.get("PR_WATCH_REPOS")
    if not raw:
        return DEFAULT_REPOS.copy()
    repos = [repo.strip() for repo in raw.split(",") if repo.strip()]
    return repos or DEFAULT_REPOS.copy()


REPOS = env_repos()
VIEWER = os.environ.get("PR_WATCH_VIEWER", "wolfgangmeyers")
MAILBOX_TO = os.environ.get("PR_WATCH_MAIL_TO", "supervisor")
MAILBOX_FROM = os.environ.get("PR_WATCH_MAIL_FROM", "pr-watch")

STATE_DIR = Path(os.environ.get("PR_WATCH_STATE_DIR", str(DEFAULT_STATE_DIR))).expanduser()
STATE_PATH = STATE_DIR / "watch-state.json"
LAST_REPORT_PATH = STATE_DIR / "last-report.md"
REPORT_DIR = STATE_DIR / "reports"


def configure_paths(state_dir: Path) -> None:
    global STATE_DIR, STATE_PATH, LAST_REPORT_PATH, REPORT_DIR

    STATE_DIR = state_dir.expanduser()
    STATE_PATH = STATE_DIR / "watch-state.json"
    LAST_REPORT_PATH = STATE_DIR / "last-report.md"
    REPORT_DIR = STATE_DIR / "reports"


def utc_now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def run(cmd: list[str]) -> str:
    proc = subprocess.run(cmd, text=True, capture_output=True)
    if proc.returncode != 0:
        raise RuntimeError(
            f"command failed: {' '.join(cmd)}\nstdout: {proc.stdout}\nstderr: {proc.stderr}"
        )
    return proc.stdout


TRANSIENT_GH_HTTP_CODES = ("502", "503", "504")
RETRY_DELAYS_SECONDS = (0.2, 0.4)


def is_transient_gh_error(exc: Exception) -> bool:
    message = str(exc)
    codes = "|".join(TRANSIENT_GH_HTTP_CODES)
    return "gh" in message and bool(re.search(rf"\b(?:{codes})\b", message))


def gh_json(args: list[str]) -> Any:
    for attempt in range(len(RETRY_DELAYS_SECONDS) + 1):
        try:
            return json.loads(run(["gh", *args]))
        except Exception as exc:  # noqa: BLE001 - retry only known transient gh outages.
            if not is_transient_gh_error(exc) or attempt == len(RETRY_DELAYS_SECONDS):
                raise
            time.sleep(RETRY_DELAYS_SECONDS[attempt])
    raise AssertionError("unreachable")


def load_state() -> dict[str, Any]:
    if not STATE_PATH.exists():
        return {"prs": {}}
    with STATE_PATH.open("r", encoding="utf-8") as f:
        return json.load(f)


def normalize_error_signature(errors: list[str]) -> str:
    normalized = []
    for error in errors:
        compact = re.sub(r"\s+", " ", error).strip()
        if "gh" in compact and re.search(r"\bHTTP 5(?:02|03|04)\b", compact):
            repo, separator, _detail = compact.partition(": ")
            if separator:
                compact = f"{repo}: gh request failed (HTTP 5xx)"
        normalized.append(compact)
    return "\n".join(sorted(normalized))


def save_state(state: dict[str, Any]) -> None:
    STATE_DIR.mkdir(parents=True, exist_ok=True)
    tmp = STATE_PATH.with_suffix(".tmp")
    with tmp.open("w", encoding="utf-8") as f:
        json.dump(state, f, indent=2, sort_keys=True)
        f.write("\n")
    tmp.replace(STATE_PATH)


def short_sha(oid: str | None) -> str:
    return (oid or "")[:7]


def check_summary(pr: dict[str, Any]) -> list[dict[str, str]]:
    checks = []
    for check in pr.get("statusCheckRollup") or []:
        checks.append(
            {
                "name": check.get("name") or "",
                "workflowName": check.get("workflowName") or "",
                "status": check.get("status") or "",
                "conclusion": check.get("conclusion") or "",
                "completedAt": check.get("completedAt") or "",
            }
        )
    return checks


def review_summary(pr: dict[str, Any]) -> list[dict[str, str]]:
    reviews = []
    for review in pr.get("latestReviews") or []:
        author = (review.get("author") or {}).get("login") or ""
        reviews.append(
            {
                "author": author,
                "state": review.get("state") or "",
                "submittedAt": review.get("submittedAt") or "",
                "commitOid": (review.get("commit") or {}).get("oid") or "",
            }
        )
    return reviews


def review_requests(pr: dict[str, Any]) -> list[str]:
    requests = []
    for request in pr.get("reviewRequests") or []:
        reviewer = request.get("requestedReviewer") or request.get("reviewer") or request
        login = reviewer.get("login") or reviewer.get("slug") or reviewer.get("name") or ""
        if login:
            requests.append(login)
    return sorted(requests)


def comment_meta(repo: str, number: int) -> tuple[list[dict[str, str]], list[str]]:
    detail = gh_json(
        [
            "pr",
            "view",
            str(number),
            "-R",
            repo,
            "--json",
            "comments,files",
        ]
    )
    comments = []
    for comment in detail.get("comments") or []:
        comments.append(
            {
                "author": (comment.get("author") or {}).get("login") or "",
                "createdAt": comment.get("createdAt") or "",
                "url": comment.get("url") or "",
            }
        )
    return comments, [file.get("path") or "" for file in detail.get("files") or []]


PULL_REQUEST_PAGE_QUERY = """
query($owner: String!, $name: String!, $cursor: String = null) {
  repository(owner: $owner, name: $name) {
    pullRequests(states: OPEN, first: 100, after: $cursor, orderBy: {field: UPDATED_AT, direction: DESC}) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number
        title
        url
        author { login }
        updatedAt
        createdAt
        headRefOid
        mergeStateStatus
        reviewDecision
        isDraft
        statusCheckRollup {
          contexts(first: 100) {
            nodes {
              ... on CheckRun { name status conclusion completedAt }
              ... on StatusContext { name: context status: state }
            }
          }
        }
        latestReviews(first: 100) {
          nodes { author { login } state submittedAt commit { oid } }
        }
        reviewRequests(first: 100) {
          nodes {
            requestedReviewer {
              ... on User { login }
              ... on Team { slug name }
            }
          }
        }
      }
    }
  }
}
"""


def connection_nodes(value: Any) -> list[dict[str, Any]]:
    if isinstance(value, dict):
        return value.get("nodes") or []
    return value or []


def fetch_repo(repo: str) -> list[dict[str, Any]]:
    owner, name = repo.split("/", 1)
    prs = []
    cursor = None
    while True:
        args = [
            "api",
            "graphql",
            "-f",
            f"query={PULL_REQUEST_PAGE_QUERY}",
            "-f",
            f"owner={owner}",
            "-f",
            f"name={name}",
        ]
        if cursor:
            args.extend(["-f", f"cursor={cursor}"])
        response = gh_json(args)
        page = response["data"]["repository"]["pullRequests"]
        for pr in page.get("nodes") or []:
            status_rollup = pr.get("statusCheckRollup") or {}
            pr["statusCheckRollup"] = connection_nodes(status_rollup.get("contexts"))
            pr["latestReviews"] = connection_nodes(pr.get("latestReviews"))
            pr["reviewRequests"] = connection_nodes(pr.get("reviewRequests"))
            pr["repo"] = repo
            pr["author_login"] = (pr.get("author") or {}).get("login") or ""
            if should_fetch_detail(pr):
                comments, files = comment_meta(repo, int(pr["number"]))
            else:
                comments, files = [], []
            pr["comments_meta"] = comments
            pr["files"] = files
            pr["checks"] = check_summary(pr)
            pr["reviews"] = review_summary(pr)
            pr["review_requests"] = review_requests(pr)
            prs.append(pr)

        page_info = page.get("pageInfo") or {}
        if not page_info.get("hasNextPage"):
            return prs
        cursor = page_info.get("endCursor")
        if not cursor:
            raise RuntimeError(f"{repo}: GitHub returned a paginated PR page without an end cursor")


def should_fetch_detail(pr: dict[str, Any]) -> bool:
    author = (pr.get("author") or {}).get("login") or ""
    if author == VIEWER:
        return False
    if pr.get("isDraft"):
        return has_bad_checks(pr)
    if author in QUIET_BOT_AUTHORS:
        return False
    return True


def has_bad_checks(pr: dict[str, Any]) -> bool:
    for check in pr.get("statusCheckRollup") or pr.get("checks") or []:
        conclusion = check.get("conclusion")
        if conclusion and conclusion not in {"SUCCESS", "SKIPPED", "NEUTRAL"}:
            return True
    return False


def bad_checks(pr: dict[str, Any]) -> list[str]:
    bad = []
    for check in pr.get("checks") or []:
        conclusion = check.get("conclusion")
        if conclusion and conclusion not in {"SUCCESS", "SKIPPED", "NEUTRAL"}:
            bad.append(f"{check['name']} {conclusion}")
    return bad


def keyword_hit(pr: dict[str, Any]) -> list[str]:
    haystack = " ".join([pr.get("title") or "", " ".join(pr.get("files") or [])]).lower()
    return [kw for kw in SECURITY_KEYWORDS if kw in haystack]


def latest_time(items: list[dict[str, str]], author: str | None = None) -> str:
    values = []
    for item in items:
        if author is not None and item.get("author") != author:
            continue
        values.append(item.get("createdAt") or item.get("submittedAt") or "")
    return max(values) if values else ""


def latest_review(items: list[dict[str, str]], author: str) -> dict[str, str] | None:
    reviews = [
        item for item in items if item.get("author") == author and item.get("submittedAt")
    ]
    return max(reviews, key=lambda item: item["submittedAt"]) if reviews else None


def external_items(items: list[dict[str, str]]) -> list[dict[str, str]]:
    return [item for item in items if item.get("author") != VIEWER]


def external_review_signal(pr: dict[str, Any]) -> list[dict[str, str]]:
    return [
        {
            "author": review.get("author") or "",
            "state": review.get("state") or "",
            "submittedAt": review.get("submittedAt") or "",
        }
        for review in external_items(pr.get("reviews") or [])
    ]


def bad_check_signal(pr: dict[str, Any]) -> list[dict[str, str]]:
    checks = []
    for check in pr.get("checks") or []:
        status = check.get("status")
        conclusion = check.get("conclusion")
        if conclusion and conclusion not in {"SUCCESS", "SKIPPED", "NEUTRAL"}:
            checks.append(
                {
                    "name": check.get("name") or "",
                    "workflowName": check.get("workflowName") or "",
                    "status": status or "",
                    "conclusion": conclusion,
                }
            )
    return checks


def latest_external_activity(pr: dict[str, Any]) -> str:
    values = [pr.get("createdAt") or ""]
    values.append(latest_time(external_items(pr.get("comments_meta") or [])))
    values.append(latest_time(external_items(pr.get("reviews") or [])))
    return max([value for value in values if value] or [""])


def latest_wolfgang_activity(pr: dict[str, Any]) -> str:
    values = [
        latest_time(pr.get("comments_meta") or [], VIEWER),
        latest_time(pr.get("reviews") or [], VIEWER),
    ]
    return max([value for value in values if value] or [""])


def wolfgang_is_current(pr: dict[str, Any]) -> bool:
    latest_wolfgang = latest_wolfgang_activity(pr)
    latest_external = latest_external_activity(pr)
    return bool(latest_wolfgang and latest_external and latest_wolfgang >= latest_external)


def signature(pr: dict[str, Any]) -> dict[str, Any]:
    viewer_review = latest_review(pr.get("reviews") or [], VIEWER) or {}
    payload = {
        "headRefOid": pr.get("headRefOid") or "",
        "isDraft": bool(pr.get("isDraft")),
        "reviewRequired": pr.get("reviewDecision") == "REVIEW_REQUIRED",
        "viewerRequested": VIEWER in (pr.get("review_requests") or []),
        "viewerReviewAt": viewer_review.get("submittedAt") or "",
        "viewerReviewCommitOid": viewer_review.get("commitOid") or "",
        "badChecks": bad_check_signal(pr),
        "externalReviews": external_review_signal(pr),
        "externalComments": external_items(pr.get("comments_meta") or []),
    }
    encoded = json.dumps(payload, sort_keys=True, separators=(",", ":"))
    return {
        "hash": hashlib.sha256(encoded.encode("utf-8")).hexdigest(),
        **payload,
    }


def change_notes(pr: dict[str, Any], prior: dict[str, Any] | None) -> list[str]:
    if prior is None:
        return [f"first seen in watch state; head {short_sha(pr.get('headRefOid'))}, updated {pr.get('updatedAt')}"]
    notes = []
    if prior.get("headRefOid") != pr.get("headRefOid"):
        notes.append(
            f"head changed {short_sha(prior.get('headRefOid'))}->{short_sha(pr.get('headRefOid'))}"
        )
    if prior.get("badChecks") != bad_check_signal(pr) and bad_check_signal(pr):
        notes.append("failing checks changed")
    if prior.get("externalReviews") != external_review_signal(pr):
        notes.append("non-Wolfgang reviews changed")
    if prior.get("externalComments") != external_items(pr.get("comments_meta") or []):
        notes.append("non-Wolfgang comments changed")
    if not prior.get("viewerRequested") and VIEWER in (pr.get("review_requests") or []):
        notes.append("Wolfgang was requested for review")
    if (
        not prior.get("reviewRequired")
        and pr.get("reviewDecision") == "REVIEW_REQUIRED"
        and not wolfgang_is_current(pr)
    ):
        notes.append("PR became unreviewed/review-required")
    return notes


def head_changed_after_viewer_review(pr: dict[str, Any], prior: dict[str, Any] | None) -> bool:
    if (
        prior is None
        or prior.get("headRefOid") == pr.get("headRefOid")
        or pr.get("isDraft")
    ):
        return False

    viewer_review = latest_review(pr.get("reviews") or [], VIEWER)
    if not viewer_review:
        return False

    review_commit = viewer_review.get("commitOid") or ""
    if review_commit:
        return review_commit != (pr.get("headRefOid") or "")

    prior_review_at = prior.get("viewerReviewAt")
    if prior_review_at is None:
        return True
    return (viewer_review.get("submittedAt") or "") <= prior_review_at


def classify(pr: dict[str, Any]) -> dict[str, Any] | None:
    """Select only PRs Wolfgang is currently expected to review."""
    author = pr.get("author_login") or ""
    if author == VIEWER or author in QUIET_BOT_AUTHORS:
        return None
    if VIEWER not in pr.get("review_requests", []):
        return None

    viewer_review = latest_review(pr.get("reviews") or [], VIEWER)
    if viewer_review is None:
        reason = "Wolfgang is requested and has not reviewed"
    elif not viewer_review.get("commitOid"):
        # A review without its commit cannot prove that the current head is stale.
        return None
    elif viewer_review["commitOid"] != (pr.get("headRefOid") or ""):
        reason = "head changed after Wolfgang's latest review"
    else:
        return None

    return {
        "repo": pr["repo"],
        "number": pr["number"],
        "title": pr["title"],
        "url": pr["url"],
        "author": author,
        "change": f"head {short_sha(pr.get('headRefOid'))}, updated {pr.get('updatedAt')}",
        "head_sha": pr.get("headRefOid") or "",
        "reasons": [reason],
        "action": "run review-pr workflow",
    }


def render_report(
    findings: list[dict[str, Any]], all_prs: list[dict[str, Any]], errors: list[str], now: str
) -> str:
    lines = [
        "# PR Watch Report",
        "",
        f"Generated: {now}",
        f"Repos checked: {', '.join(REPOS)}",
        f"Open PRs seen: {len(all_prs)}",
        f"Actionable findings: {len(findings)}",
        "",
    ]
    if errors:
        lines.extend(["## Errors", ""])
        lines.extend(f"- {error}" for error in errors)
        lines.append("")
    if findings:
        lines.extend(["## Findings", ""])
        for item in findings:
            lines.extend(
                [
                    f"### {item['repo']}#{item['number']}: {item['title']}",
                    "",
                    f"- URL: {item['url']}",
                    f"- Author: {item['author']}",
                    f"- Why: {'; '.join(item['reasons'])}",
                    f"- Changed: {item['change']}",
                    f"- Recommended next action: {item['action']}",
                    "",
                ]
            )
    else:
        lines.extend(["## Findings", "", "No actionable PRs in this pass.", ""])

    lines.extend(["## Open PR Inventory", ""])
    for pr in sorted(all_prs, key=lambda item: (item["repo"], int(item["number"]))):
        checks = ", ".join(bad_checks(pr)) or "checks ok/none"
        lines.append(
            f"- {pr['repo']}#{pr['number']} {pr['title']} "
            f"({pr.get('author_login')}, draft={bool(pr.get('isDraft'))}, "
            f"review={pr.get('reviewDecision') or '-'}, merge={pr.get('mergeStateStatus') or '-'}, "
            f"{checks})"
        )
    lines.append("")
    return "\n".join(lines)


def write_report(report: str, now: str) -> Path:
    REPORT_DIR.mkdir(parents=True, exist_ok=True)
    path = REPORT_DIR / f"pr-watch-{now.replace(':', '').replace('-', '')}.md"
    path.write_text(report, encoding="utf-8")
    LAST_REPORT_PATH.write_text(report, encoding="utf-8")
    return path


def send_mail(subject: str, body: str) -> None:
    run([
        "python3", str(MAILBOX_SEND_V2), "--to", MAILBOX_TO,
        "--from", MAILBOX_FROM, "--subject", subject, "--body", body,
    ])


def try_send_mail(subject: str, body: str) -> None:
    try:
        send_mail(subject, body)
    except Exception as exc:  # noqa: BLE001 - losing mail should not kill the polling loop.
        print(f"PR watch mail send failed: {exc}", file=sys.stderr)


def mail_body(findings: list[dict[str, Any]], errors: list[str], path: Path, now: str) -> str:
    evidence = []
    for item in findings:
        evidence.append(
            f"{item['repo'].split('/')[-1]}#{item['number']}: "
            f"{'; '.join(item['reasons'])}; head {item['head_sha']}; {item['action']}"
        )
    return (
        f"PR watch findings: {len(findings)} actionable PR(s) at {now}. "
        f"{' | '.join(evidence)}. Report: {path}"
    )



def pass_once(send: bool = True, dry_run: bool = False) -> tuple[list[dict[str, Any]], Path | None]:
    now = utc_now()
    state = load_state()
    previous = state.get("prs") or {}
    all_prs = []
    errors = []
    failed_repos = set()

    for repo in REPOS:
        try:
            all_prs.extend(fetch_repo(repo))
        except Exception as exc:  # noqa: BLE001 - keep loop alive and report the repo failure.
            errors.append(f"{repo}: {exc}")
            failed_repos.add(repo)

    findings = []
    next_prs = {
        key: value
        for key, value in previous.items()
        if key.split("#", 1)[0] in failed_repos
    }
    for pr in all_prs:
        key = f"{pr['repo']}#{pr['number']}"
        finding = classify(pr)
        if finding:
            findings.append(finding)
        next_prs[key] = signature(pr)

    report = render_report(findings, all_prs, errors, now)
    report_path = None if dry_run else write_report(report, now)

    error_signature = normalize_error_signature(errors) if errors else ""
    prior_error_signature = state.get("error_signature") or ""
    if not dry_run:
        state["last_pass_at"] = now
        state["prs"] = next_prs
        state["last_report"] = str(report_path)
        if error_signature:
            state["error_signature"] = error_signature
        else:
            state.pop("error_signature", None)
        save_state(state)

    if send and not dry_run and report_path and findings:
        try_send_mail("PR watch findings", mail_body(findings, errors, report_path, now))
    if send and not dry_run and errors and error_signature != prior_error_signature:
        try_send_mail("PR watch error", "; ".join(errors)[:900])

    print(report, end="")

    return findings, report_path


def apply_args(args: argparse.Namespace) -> None:
    global REPOS, VIEWER, MAILBOX_TO, MAILBOX_FROM

    if args.repo:
        REPOS = args.repo
    if args.state_dir:
        configure_paths(Path(args.state_dir))
    if args.viewer:
        VIEWER = args.viewer
    if args.mail_to:
        MAILBOX_TO = args.mail_to
    if args.mail_from:
        MAILBOX_FROM = args.mail_from


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--dry-run", action="store_true", help="print report without writing state or mail")
    parser.add_argument("--no-mail", action="store_true", help="write state/report without sending mail")
    parser.add_argument("--repo", action="append", help="owner/name repo to watch; repeat to override defaults")
    parser.add_argument("--state-dir", help="directory for watch-state.json and reports")
    parser.add_argument("--viewer", help="GitHub login whose review attention is being watched")
    parser.add_argument("--mail-to", help="mailbox recipient agent ID")
    parser.add_argument("--mail-from", help="explicit V2 sender agent ID")
    args = parser.parse_args()
    apply_args(args)

    try:
        pass_once(send=not args.no_mail, dry_run=args.dry_run)
    except Exception as exc:  # noqa: BLE001 - report a single wakeup failure.
        now = utc_now()
        message = f"PR watch error at {now}: {exc}"
        print(message, file=sys.stderr)
        if not args.no_mail and not args.dry_run:
            try_send_mail("PR watch error", message[:900])
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
