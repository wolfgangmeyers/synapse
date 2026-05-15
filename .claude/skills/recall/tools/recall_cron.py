#!/usr/bin/env python3
"""Cron job: evaluate due recall windows and deliver relevant memories."""

import logging
import os
import sys
from pathlib import Path

from recall_lib import (
    capture_window_context,
    clear_scheduled_window,
    deliver_memory,
    due_windows,
    evaluate_relevance,
    get_active_stores_for_window,
    isoformat_utc,
    load_config,
    load_session,
    load_store_snapshots,
    now_utc,
    save_session,
    should_deliver,
)


LOG_FILE = Path(os.path.expanduser("~/.mecha-wolfgang/recall-cron.log"))


def setup_logging():
    LOG_FILE.parent.mkdir(parents=True, exist_ok=True)
    logging.basicConfig(
        filename=LOG_FILE,
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S",
    )


def filter_stores_by_activation(config, window):
    """Filter config stores to only those activated for this window.

    Returns a copy of config with stores filtered. If no stores are activated
    for this window, returns None (skip recall entirely).
    """
    active = get_active_stores_for_window(window)
    if active is None:
        return None
    if not active:
        return None
    filtered = dict(config)
    filtered["stores"] = [s for s in config["stores"] if s["name"] in active]
    return filtered


def process_window(window, config, now):
    context = capture_window_context(window, config["capture_lines"])
    if context is None:
        logging.info("window missing, clearing recall schedule: %s", window)
        clear_scheduled_window(window)
        return

    filtered_config = filter_stores_by_activation(config, window)
    if filtered_config is None:
        logging.info("no stores activated for %s; clearing schedule", window)
        clear_scheduled_window(window)
        return

    stores = load_store_snapshots(filtered_config)
    if not stores:
        logging.info("no recall stores configured; clearing schedule for %s", window)
        clear_scheduled_window(window)
        return

    selected = evaluate_relevance(window, context, stores, filtered_config["model"])
    session = load_session(window)
    delivered = []
    for memory in selected:
        if not should_deliver(session, memory["id"], filtered_config["cooldown_messages"]):
            continue
        if not deliver_memory(window, memory):
            logging.warning("delivery failed for %s to %s", memory["id"], window)
            continue
        session["memories_recalled"][memory["id"]] = {
            "message_count": session.get("message_count", 0),
            "recalled_at": isoformat_utc(now),
        }
        delivered.append(memory["id"])
    save_session(window, session)
    clear_scheduled_window(window)
    logging.info("processed recall window %s: delivered=%s", window, ",".join(delivered) if delivered else "none")


def main():
    setup_logging()
    try:
        config = load_config()
        if not config["enabled"]:
            logging.info("recall disabled")
            return
        now = now_utc()
        windows = due_windows(now=now)
        if not windows:
            return
        for window, _ in windows:
            try:
                process_window(window, config, now)
            except Exception as exc:
                logging.exception("failed processing recall window %s: %s", window, exc)
    except Exception as exc:
        logging.exception("recall cron failed: %s", exc)
        sys.exit(1)


if __name__ == "__main__":
    main()
