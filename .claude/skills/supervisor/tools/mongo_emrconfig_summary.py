#!/usr/bin/env python3
"""Redacted RogerDocs Mongo EMRConfig inspector.

Loads /Users/wolfgang/mongodb_creds.sh plus Roger-Docs-API/dev.env, connects to
Mongo, and prints only non-secret summary data. Never prints credential values or
raw EMRConfig documents.
"""
from __future__ import annotations

import argparse
import json
import re
import shlex
import sys
from pathlib import Path
from urllib.parse import quote_plus

SECRET_KEY_RE = re.compile(r"(pass|password|user|username|credential|login|token|secret|key)", re.I)
PLACEHOLDER_RE = re.compile(r"\$\{([^}]+)\}|\$([A-Za-z_][A-Za-z0-9_]*)")
DEFAULT_CREDS = Path("/Users/wolfgang/mongodb_creds.sh")
DEFAULT_ROGER_DOCS = Path("/Users/wolfgang/code/Roger-Docs-API")


def parse_key_value_file(path: Path) -> dict[str, str]:
    values: dict[str, str] = {}
    if not path.exists():
        return values
    for raw_line in path.read_text(errors="ignore").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        if line.startswith("export "):
            line = line[len("export ") :].strip()
        try:
            parts = shlex.split(line, posix=True)
        except ValueError:
            parts = [line]
        for part in parts or [line]:
            if "=" not in part:
                continue
            key, value = part.split("=", 1)
            key = key.strip()
            if key:
                values[key] = value
    return values


def expand_template(template: str, values: dict[str, str]) -> str:
    def repl(match: re.Match[str]) -> str:
        key = match.group(1) or match.group(2)
        value = values.get(key)
        if value is None:
            return match.group(0)
        if key and re.search(r"USER|PASS|PASSWORD", key, re.I):
            return quote_plus(value)
        return value

    previous = None
    result = template
    while previous != result:
        previous = result
        result = PLACEHOLDER_RE.sub(repl, result)
    return result


def redacted_doc(doc: dict, db_name: str, collection: str, match_label: str) -> dict:
    keys = sorted(k for k in doc if k != "_id")
    credential_keys = [k for k in keys if SECRET_KEY_RE.search(k)]
    safe = {
        key: doc.get(key)
        for key in ["agency_id", "agency_system_name", "name", "products", "is_dev", "emr_type", "product", "url", "base_url"]
        if key in doc and not SECRET_KEY_RE.search(key)
    }
    safe.update(
        {
            "db": db_name,
            "collection": collection,
            "match": match_label,
            "_id": str(doc.get("_id")),
            "credential_keys_present": credential_keys,
            "all_keys": keys,
        }
    )
    return safe


def main() -> int:
    parser = argparse.ArgumentParser(description="Inspect EMRConfig in RogerDocs Mongo with redacted output.")
    parser.add_argument("--creds", type=Path, default=DEFAULT_CREDS)
    parser.add_argument("--roger-docs", type=Path, default=DEFAULT_ROGER_DOCS)
    parser.add_argument("--database", action="append", dest="databases", help="Database to inspect; may be repeated")
    parser.add_argument("--all-databases", action="store_true", help="Inspect every visible database instead of only NursingApp-like databases")
    parser.add_argument("--database-regex", help="Only inspect database names matching this regex")
    parser.add_argument("--collection", default="EMRConfig")
    parser.add_argument("--host", help="Override the MongoDB SRV host while keeping local credentials and database/template defaults")
    parser.add_argument("--uri-database", help="Database path to include in a --host URI override; omit to let MongoDB use the driver default auth source")
    parser.add_argument("--search", default="concierge", help="Case-insensitive agency/name search term for sample docs")
    parser.add_argument("--sample-limit", type=int, default=20)
    parser.add_argument("--write-credentials", type=Path, help="Write matched credential values to this local file with chmod 600")
    parser.add_argument("--list-agency-ids", action="store_true", help="List non-secret agency_id values for inspected EMRConfig collections")
    parser.add_argument("--json", action="store_true", help="Emit machine-readable JSON")
    args = parser.parse_args()

    try:
        from pymongo import MongoClient
    except ModuleNotFoundError:
        print("pymongo is required; run with: uv run --with pymongo python3 mongo_emrconfig_summary.py", file=sys.stderr)
        return 2

    dev_env = parse_key_value_file(args.roger_docs / "dev.env")
    creds = parse_key_value_file(args.creds)
    values = {**dev_env, **creds}
    # The local creds file is authoritative for auth values. dev.env is a template
    # source and may contain stale MONGODB_PASS/MONGODB_USER defaults.
    if creds.get("MONGODB_PASSWORD"):
        values["MONGODB_PASS"] = creds["MONGODB_PASSWORD"]
        values["MONGODB_PASSWORD"] = creds["MONGODB_PASSWORD"]
    if creds.get("MONGODB_PASS"):
        values["MONGODB_PASS"] = creds["MONGODB_PASS"]
        values["MONGODB_PASSWORD"] = creds["MONGODB_PASS"]
    if creds.get("MONGODB_USER"):
        values["MONGODB_USER"] = creds["MONGODB_USER"]

    template = values.get("MONGODB_URL", "")
    if args.host:
        db_path = f"/{args.uri_database}" if args.uri_database else ""
        template = f"mongodb+srv://${{MONGODB_USER}}:${{MONGODB_PASS}}@{args.host}{db_path}"
    url = expand_template(template, values)
    output: dict = {
        "credential_source": str(args.creds),
        "template_source": str(args.roger_docs / "dev.env"),
        "credential_keys_present": {key: bool(creds.get(key)) for key in sorted(creds)},
        "default_db": values.get("MONGODB_DB"),
        "connection": {"template_present": bool(template), "expanded": bool(url and "${" not in url), "host_override": args.host or None},
        "databases": [],
    }
    if not template or "${" in url or re.search(r"\$[A-Za-z_]", url):
        output["connection"]["error"] = "missing or unexpanded MONGODB_URL"
        print(json.dumps(output, indent=2) if args.json else output["connection"]["error"])
        return 1

    try:
        client = MongoClient(url, serverSelectionTimeoutMS=10_000)
        client.admin.command("ping")
        output["connection"]["ping"] = "ok"
    except Exception as exc:  # intentionally no URI/secret details
        output["connection"]["error"] = f"{type(exc).__name__}: {str(exc).splitlines()[0][:220]}"
        print(json.dumps(output, indent=2) if args.json else output["connection"]["error"])
        return 2

    if args.databases:
        db_names = args.databases
    else:
        names = client.list_database_names()
        if args.all_databases:
            db_names = names
        else:
            db_names = [name for name in names if "NursingApp" in name or name.lower().startswith("nursing")]
            for name in [values.get("MONGODB_DB"), "NursingAppDB", "NursingAppDB-prod", "NursingApp-prod", "NursingApp"]:
                if name and name not in db_names:
                    db_names.append(name)
    if args.database_regex:
        database_pattern = re.compile(args.database_regex)
        db_names = [name for name in db_names if database_pattern.search(name)]

    regex = {"$regex": args.search, "$options": "i"}
    for db_name in db_names:
        db_info = {"name": db_name, "collections": []}
        output["databases"].append(db_info)
        db = client[db_name]
        try:
            collections = db.list_collection_names()
        except Exception as exc:
            db_info["error"] = f"{type(exc).__name__}: {str(exc).splitlines()[0][:160]}"
            continue
        emr_collections = [c for c in collections if c == args.collection or c.lower() == args.collection.lower() or "emrconfig" in c.lower()]
        for collection_name in emr_collections:
            collection = db[collection_name]
            search_query = {"$or": [{"agency_id": regex}, {"agency_system_name": regex}, {"name": regex}]}
            counts = {
                "estimated_total": collection.estimated_document_count(),
                "agency_id_Conciergesandbox_exact": collection.count_documents({"agency_id": "Conciergesandbox"}),
                "agency_id_Conciergesandbox_ci": collection.count_documents({"agency_id": {"$regex": "^Conciergesandbox$", "$options": "i"}}),
                f"search_{args.search}": collection.count_documents(search_query),
            }
            raw_samples = list(collection.find(search_query).limit(max(args.sample_limit, 0)))
            samples = [redacted_doc(doc, db_name, collection_name, f"search_{args.search}") for doc in raw_samples]
            collection_info = {"name": collection_name, "counts": counts, "samples": samples}
            if args.list_agency_ids:
                collection_info["agency_ids"] = sorted(
                    str(value)
                    for value in collection.distinct("agency_id")
                    if value is not None
                )
            db_info["collections"].append(collection_info)
            if args.write_credentials and raw_samples:
                credential_records = []
                for doc in raw_samples:
                    record = {
                        "db": db_name,
                        "collection": collection_name,
                        "_id": str(doc.get("_id")),
                        "agency_id": doc.get("agency_id"),
                        "agency_system_name": doc.get("agency_system_name"),
                        "name": doc.get("name"),
                    }
                    for key, value in doc.items():
                        if key != "_id" and SECRET_KEY_RE.search(key):
                            record[key] = value
                    credential_records.append(record)
                args.write_credentials.parent.mkdir(parents=True, exist_ok=True)
                args.write_credentials.write_text(json.dumps(credential_records, indent=2, default=str) + "\n")
                args.write_credentials.chmod(0o600)
                output.setdefault("credential_exports", []).append(
                    {"path": str(args.write_credentials), "records": len(credential_records), "db": db_name, "collection": collection_name}
                )

    if args.json:
        print(json.dumps(output, indent=2, default=str))
    else:
        print(f"credential_source={output['credential_source']}")
        print(f"template_source={output['template_source']}")
        print(f"ping={output['connection'].get('ping')}")
        for db_info in output["databases"]:
            if db_info.get("error"):
                print(f"db={db_info['name']} error={db_info['error']}")
                continue
            for collection in db_info["collections"]:
                print(f"db={db_info['name']} collection={collection['name']} counts={json.dumps(collection['counts'])}")
                if "agency_ids" in collection:
                    print("  agency_ids=" + json.dumps(collection["agency_ids"]))
                for sample in collection["samples"]:
                    print("  doc=" + json.dumps(sample, default=str))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
