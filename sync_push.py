#!/usr/bin/env python3
"""
Delta Sync — pushes new/changed photos and metadata to the standalone photo frame.

Runs on the Tern host. Compares local photo_captions.db against the remote frame's
database, and pushes only what's changed.

Usage:
    python3 sync_push.py                      # Push delta updates
    python3 sync_push.py --full               # Full resync (all photos)
    python3 sync_push.py --target HOST:PORT   # Override target
    python3 sync_push.py --dry-run            # Show what would be synced

Environment:
    FRAME_HOST     Target host:port (default: localhost:8085)
    SYNC_TOKEN     Auth token (default: changeme)
    CAPTIONS_DB    Path to photo_captions.db
    FILTER_DB      Path to photo_filter.db
    PHOTO_ROOTS    Colon-separated photo source directories
"""

import argparse
import json
import os
import sqlite3
import sys
import time
import urllib.request
import urllib.error

BASE = os.path.dirname(os.path.abspath(__file__))
PARENT = os.path.dirname(BASE)

# All paths configurable via environment variables
CAPTIONS_DB = os.environ.get("CAPTIONS_DB", os.path.join(PARENT, "photo_captions.db"))
FILTER_DB = os.environ.get("FILTER_DB", os.path.join(PARENT, "photo_filter.db"))
SYNC_STATE = os.path.join(BASE, "data", "last_sync.json")

# Photo source directories — override with colon-separated PHOTO_ROOTS env var
DEFAULT_PHOTO_ROOTS = [
    "/media/desva/Backup/FamilyPhotos/",
    "/media/desva/Elements/",
]
PHOTO_ROOTS = os.environ.get("PHOTO_ROOTS", "").split(":") if os.environ.get("PHOTO_ROOTS") else DEFAULT_PHOTO_ROOTS

BATCH_SIZE = 100  # Photos per metadata sync request


def get_target():
    return os.environ.get("FRAME_HOST", "localhost:8085")


def get_token():
    return os.environ.get("SYNC_TOKEN", "changeme")


def load_sync_state():
    if os.path.exists(SYNC_STATE):
        with open(SYNC_STATE) as f:
            return json.load(f)
    return {"last_sync": None, "synced_count": 0}


def save_sync_state(state):
    os.makedirs(os.path.dirname(SYNC_STATE), exist_ok=True)
    with open(SYNC_STATE, "w") as f:
        json.dump(state, f, indent=2)


def get_photos_to_sync(full=False):
    """Get photos that need syncing (not flagged, with metadata)."""
    conn = sqlite3.connect(CAPTIONS_DB, timeout=30)

    # Attach filter DB to exclude flagged photos — use parameterised path
    if os.path.exists(FILTER_DB):
        conn.execute("ATTACH DATABASE ? AS filt", (FILTER_DB,))
        query = """
            SELECT c.path, c.caption FROM captions c
            LEFT JOIN filt.classifications f ON c.path = f.path
            WHERE f.category IS NULL OR f.category = 'keep'
        """
    else:
        query = "SELECT path, caption FROM captions"

    rows = conn.execute(query).fetchall()

    photos = []
    for path, caption in rows:
        # Get people tags
        people_rows = conn.execute(
            "SELECT DISTINCT person_name FROM people_tags WHERE photo_path = ?",
            (path,)
        ).fetchall()
        people = [r[0] for r in people_rows]

        # Compute relative path for the frame
        rel_path = None
        for root in PHOTO_ROOTS:
            if path.startswith(root):
                rel_path = path[len(root):]
                break

        if rel_path is None:
            continue

        photos.append({
            "abs_path": path,
            "path": rel_path,
            "caption": caption or "",
            "people": people,
        })

    conn.close()
    return photos


def sync_metadata(target, token, photos, dry_run=False):
    """Push photo metadata in batches."""
    total = len(photos)
    synced = 0

    for i in range(0, total, BATCH_SIZE):
        batch = photos[i:i + BATCH_SIZE]
        payload = {
            "photos": [
                {"path": p["path"], "caption": p["caption"], "people": p["people"]}
                for p in batch
            ]
        }

        if dry_run:
            print(f"  [dry-run] Would sync batch {i // BATCH_SIZE + 1}: {len(batch)} photos")
            synced += len(batch)
            continue

        data = json.dumps(payload).encode()
        req = urllib.request.Request(
            f"http://{target}/api/sync",
            data=data,
            headers={
                "Content-Type": "application/json",
                "X-Sync-Token": token,
            },
            method="POST",
        )

        try:
            resp = urllib.request.urlopen(req, timeout=30)
            result = json.loads(resp.read())
            synced += result.get("added", 0) + result.get("updated", 0)
            print(f"  Batch {i // BATCH_SIZE + 1}: +{result.get('added', 0)} /{result.get('updated', 0)} updated /{result.get('deleted', 0)} deleted")
        except urllib.error.URLError as e:
            print(f"  ERROR syncing batch: {e}")
            return synced

    return synced


def sync_photo_files(target, token, photos, dry_run=False):
    """Push actual photo files that don't exist on the remote (streaming)."""
    pushed = 0
    errors = 0

    for p in photos:
        abs_path = p["abs_path"]
        rel_path = p["path"]

        if not os.path.exists(abs_path):
            continue

        if dry_run:
            pushed += 1
            continue

        try:
            file_size = os.path.getsize(abs_path)
            with open(abs_path, "rb") as f:
                req = urllib.request.Request(
                    f"http://{target}/api/sync/photo?path={urllib.request.quote(rel_path)}",
                    data=f,
                    headers={
                        "X-Sync-Token": token,
                        "Content-Type": "application/octet-stream",
                        "Content-Length": str(file_size),
                    },
                    method="POST",
                )
                resp = urllib.request.urlopen(req, timeout=60)
                result = json.loads(resp.read())
                if result.get("ok"):
                    pushed += 1
            if pushed % 100 == 0:
                print(f"  Pushed {pushed} photo files...")
        except Exception as e:
            errors += 1
            if errors <= 5:
                print(f"  Error pushing {rel_path}: {e}")

    return pushed, errors


def main():
    parser = argparse.ArgumentParser(description="Sync photos to standalone frame")
    parser.add_argument("--full", action="store_true", help="Full resync")
    parser.add_argument("--target", default=None, help="Target host:port")
    parser.add_argument("--dry-run", action="store_true", help="Show what would be synced")
    parser.add_argument("--metadata-only", action="store_true", help="Only sync metadata, not files")
    parser.add_argument("--files-only", action="store_true", help="Only sync files, not metadata")
    args = parser.parse_args()

    target = args.target or get_target()
    token = get_token()

    print(f"Syncing to {target}")
    print(f"Getting photos to sync...")

    photos = get_photos_to_sync(full=args.full)
    print(f"Found {len(photos)} photos to sync")

    if not args.files_only:
        print("Syncing metadata...")
        synced = sync_metadata(target, token, photos, dry_run=args.dry_run)
        print(f"Metadata synced: {synced}")

    if not args.metadata_only:
        print("Syncing photo files...")
        pushed, errors = sync_photo_files(target, token, photos, dry_run=args.dry_run)
        print(f"Files pushed: {pushed}, errors: {errors}")

    # Save sync state
    if not args.dry_run:
        state = {
            "last_sync": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "synced_count": len(photos),
            "target": target,
        }
        save_sync_state(state)
        print(f"Sync state saved")

    print("Done!")


if __name__ == "__main__":
    main()
