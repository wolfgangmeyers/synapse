#!/usr/bin/env python3
"""Read-only GitHub PR watcher for the DNSid repositories."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


DEFAULT_REPOS = [
    "Identity-Digital/dnsid",
    "Identity-Digital/dnsid-infra",
    "Identity-Digital/dnsid-cookbook",
    "Identity-Digital/dnsid-go",
    "Identity-Digital/dnsid-py",
    "Identity-Digital/dnsid-ts",
]

DEFAULT_STATE_DIR = Path.home() / ".mecha-developer" / "pr-watch"
MAILBOX_SEND = Path.home() / ".claude/skills/supervisor/tools/mailbox_send.py"

QUIET_BOT_AUTHORS = {"app/dependabot", "dependabot[bot]"}
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


def env_int(name: str, default: int) -> int:
    value = os.environ.get(name)
    if not value:
        return default
    try:
        return int(value)
    except ValueError as exc:
        raise ValueError(f"{name} must be an integer, got {value!r}") from exc


def env_repos() -> list[str]:
    raw = os.environ.get("PR_WATCH_REPOS")
    if not raw:
        return DEFAULT_REPOS.copy()
    repos = [repo.strip() for repo in raw.split(",") if repo.strip()]
    return repos or DEFAULT_REPOS.copy()


REPOS = env_repos()
CADENCE_SECONDS = env_int("PR_WATCH_CADENCE_SECONDS", 15 * 60)
HEARTBEAT_SECONDS = env_int("PR_WATCH_HEARTBEAT_SECONDS", 60 * 60)
VIEWER = os.environ.get("PR_WATCH_VIEWER", "wolfgangmeyers")
MAILBOX_TO = os.environ.get("PR_WATCH_MAIL_TO", "supervisor")

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


def gh_json(args: list[str]) -> Any:
    return json.loads(run(["gh", *args]))


def load_state() -> dict[str, Any]:
    if not STATE_PATH.exists():
        return {"prs": {}, "last_heartbeat_at": None}
    with STATE_PATH.open("r", encoding="utf-8") as f:
        return json.load(f)


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


def fetch_repo(repo: str) -> list[dict[str, Any]]:
    prs = gh_json(
        [
            "pr",
            "list",
            "-R",
            repo,
            "--state",
            "open",
            "--limit",
            "100",
            "--json",
            ",".join(
                [
                    "number",
                    "title",
                    "url",
                    "author",
                    "updatedAt",
                    "createdAt",
                    "headRefOid",
                    "mergeStateStatus",
                    "reviewDecision",
                    "isDraft",
                    "statusCheckRollup",
                    "latestReviews",
                    "reviewRequests",
                ]
            ),
        ]
    )
    for pr in prs:
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
    return prs


def should_fetch_detail(pr: dict[str, Any]) -> bool:
    author = (pr.get("author") or {}).get("login") or ""
    if author == VIEWER:
        return False
    if pr.get("isDraft"):
        return has_bad_checks(pr)
    if author in QUIET_BOT_AUTHORS and not has_bad_checks(pr):
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


def external_items(items: list[dict[str, str]]) -> list[dict[str, str]]:
    return [item for item in items if item.get("author") != VIEWER]


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
    payload = {
        "headRefOid": pr.get("headRefOid") or "",
        "isDraft": bool(pr.get("isDraft")),
        "reviewRequired": pr.get("reviewDecision") == "REVIEW_REQUIRED",
        "viewerRequested": VIEWER in (pr.get("review_requests") or []),
        "badChecks": bad_check_signal(pr),
        "externalReviews": external_items(pr.get("reviews") or []),
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
    if prior.get("externalReviews") != external_items(pr.get("reviews") or []):
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


def classify(pr: dict[str, Any], prior: dict[str, Any] | None) -> dict[str, Any] | None:
    author = pr.get("author_login") or ""
    if author == VIEWER:
        return None

    checks = bad_checks(pr)
    notes = change_notes(pr, prior)
    if author in QUIET_BOT_AUTHORS and not checks and not keyword_hit(pr):
        return None
    if pr.get("isDraft") and not checks:
        return None

    reasons = []
    action = "launch /review-pr"
    wolfgang_current = wolfgang_is_current(pr)

    if VIEWER in pr.get("review_requests", []) and not wolfgang_current:
        reasons.append("Wolfgang is explicitly requested for review")

    if pr.get("reviewDecision") == "REVIEW_REQUIRED" and not pr.get("isDraft") and not wolfgang_current:
        reasons.append("review is required")

    if checks:
        reasons.append("checks need attention: " + ", ".join(checks[:3]))
        action = "inspect failing checks; re-review if the failure is product-relevant"

    if any(review["author"] == "greptile-apps" for review in external_items(pr.get("reviews") or [])) and not wolfgang_current:
        reasons.append("Greptile has reviewed/commented")

    kw = keyword_hit(pr)
    if kw and not wolfgang_current:
        reasons.append("touches watched area: " + ", ".join(kw[:5]))

    latest_wolfgang = latest_wolfgang_activity(pr)
    latest_author_comment = latest_time(pr.get("comments_meta") or [], author)
    if latest_wolfgang and latest_author_comment and latest_author_comment > latest_wolfgang:
        reasons.append("author replied after Wolfgang's last comment")
        action = "respond to feedback or re-review existing PR"

    if pr.get("reviewDecision") == "APPROVED" and not checks and latest_author_comment <= latest_wolfgang:
        return None

    if not reasons:
        return None

    if not notes:
        return None

    return {
        "repo": pr["repo"],
        "number": pr["number"],
        "title": pr["title"],
        "url": pr["url"],
        "author": author,
        "change": "; ".join(notes),
        "reasons": reasons,
        "action": action,
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
    run(["python3", str(MAILBOX_SEND), "--to", MAILBOX_TO, "--subject", subject, "--body", body])


def try_send_mail(subject: str, body: str) -> None:
    try:
        send_mail(subject, body)
    except Exception as exc:  # noqa: BLE001 - losing mail should not kill the polling loop.
        print(f"PR watch mail send failed: {exc}", file=sys.stderr)


def mail_body(findings: list[dict[str, Any]], errors: list[str], path: Path, now: str, quiet: bool) -> str:
    if quiet:
        error_note = "" if not errors else f"; {len(errors)} repo check error(s)"
        return (
            f"PR watch quiet heartbeat: checked {len(REPOS)} repos at {now}; "
            f"no actionable PR changes{error_note}. Report: {path}"
        )

    top = []
    for item in findings[:6]:
        top.append(f"{item['repo'].split('/')[-1]}#{item['number']}: {item['action']}")
    more = "" if len(findings) <= 6 else f"; +{len(findings) - 6} more"
    return (
        f"PR watch findings: {len(findings)} actionable PR(s) at {now}. "
        f"{'; '.join(top)}{more}. Report: {path}"
    )


def heartbeat_due(state: dict[str, Any], now: str) -> bool:
    last = state.get("last_heartbeat_at")
    if not last:
        return True
    last_ts = datetime.fromisoformat(last.replace("Z", "+00:00")).timestamp()
    now_ts = datetime.fromisoformat(now.replace("Z", "+00:00")).timestamp()
    return now_ts - last_ts >= HEARTBEAT_SECONDS


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
        prior = previous.get(key)
        finding = classify(pr, prior)
        if finding:
            findings.append(finding)
        next_prs[key] = signature(pr)

    report = render_report(findings, all_prs, errors, now)
    report_path = None if dry_run else write_report(report, now)

    quiet = False
    if not findings and heartbeat_due(state, now):
        quiet = True

    if not dry_run:
        state["last_pass_at"] = now
        state["prs"] = next_prs
        if findings or quiet:
            state["last_heartbeat_at"] = now
        state["last_report"] = str(report_path)
        save_state(state)

    if send and not dry_run and report_path and (findings or quiet):
        subject = "PR watch findings" if findings else "PR watch quiet"
        try_send_mail(subject, mail_body(findings, errors, report_path, now, quiet))

    if dry_run:
        print(report)
    else:
        print(f"{now}: {len(findings)} actionable finding(s); report={report_path}")

    return findings, report_path


def apply_args(args: argparse.Namespace) -> None:
    global REPOS, CADENCE_SECONDS, HEARTBEAT_SECONDS, VIEWER, MAILBOX_TO

    if args.repo:
        REPOS = args.repo
    if args.cadence_seconds is not None:
        CADENCE_SECONDS = args.cadence_seconds
    if args.heartbeat_seconds is not None:
        HEARTBEAT_SECONDS = args.heartbeat_seconds
    if args.state_dir:
        configure_paths(Path(args.state_dir))
    if args.viewer:
        VIEWER = args.viewer
    if args.mail_to:
        MAILBOX_TO = args.mail_to


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--loop", action="store_true", help="run continuously")
    parser.add_argument("--dry-run", action="store_true", help="print report without writing state or mail")
    parser.add_argument("--no-mail", action="store_true", help="write state/report without sending mail")
    parser.add_argument("--repo", action="append", help="owner/name repo to watch; repeat to override defaults")
    parser.add_argument("--cadence-seconds", type=int, help="seconds between loop passes")
    parser.add_argument("--heartbeat-seconds", type=int, help="seconds between quiet heartbeat messages")
    parser.add_argument("--state-dir", help="directory for watch-state.json and reports")
    parser.add_argument("--viewer", help="GitHub login whose review attention is being watched")
    parser.add_argument("--mail-to", help="mailbox recipient window")
    args = parser.parse_args()
    apply_args(args)

    while True:
        try:
            pass_once(send=not args.no_mail, dry_run=args.dry_run)
        except Exception as exc:  # noqa: BLE001 - long-running watcher should report and keep going.
            now = utc_now()
            message = f"PR watch error at {now}: {exc}"
            print(message, file=sys.stderr)
            if not args.no_mail and not args.dry_run:
                try_send_mail("PR watch error", message[:900])
        if not args.loop or args.dry_run:
            return 0
        sys.stdout.flush()
        time.sleep(CADENCE_SECONDS)


if __name__ == "__main__":
    raise SystemExit(main())
