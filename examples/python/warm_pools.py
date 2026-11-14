"""Warm Pools - Manage pre-warmed sandbox pools.

Demonstrates warm pool operations:
1. List warm pools
2. Create a warm pool
3. Get warm pool details
4. Update pool size
5. Delete warm pool (graceful)
6. Force delete (?force=true — immediate teardown)

Usage:
    export E2B_DOMAIN=localhost:8080
    export E2B_API_KEY=your-api-key
    python warm_pools.py
"""

import os
import sys
import time

import requests


def main():
    base_url = os.environ.get("E2B_DOMAIN", "localhost:8080")
    if not base_url.startswith("http"):
        base_url = f"http://{base_url}"
    api_key = os.environ.get("E2B_API_KEY", "test-key")
    headers = {"X-API-Key": api_key, "Content-Type": "application/json"}

    # Pools created by this script that have not been deleted yet; the
    # finally block best-effort removes them so a mid-script failure does
    # not leak live pools behind.
    created = []

    # Run-unique, RFC 1123-compatible pool IDs: force delete tombstones the
    # namespace briefly, so the deterministic auto-derived ID (wp-base-…)
    # would collide with the tombstone on an immediate re-run.
    def unique_pool_id(prefix):
        return f"{prefix}-{int(time.time() * 1000)}"

    try:
        # 1. List warm pools
        print("1. Listing warm pools...")
        resp = requests.get(f"{base_url}/warm-pools", headers=headers, timeout=30)
        resp.raise_for_status()
        print(f"   Existing pools: {len(resp.json())}")

        # 2. Create a warm pool
        print("2. Creating warm pool...")
        resp = requests.post(f"{base_url}/warm-pools", headers=headers, timeout=30, json={
            "templateID": "base",
            "targetSize": 3,
            "warmPoolID": unique_pool_id("wp-py"),
        })
        resp.raise_for_status()
        pool = resp.json()
        pool_id = pool["warmPoolID"]
        created.append(pool_id)
        print(f"   Pool: {pool_id} (template={pool.get('templateID')}, target={pool.get('targetSize')})")

        # 3. Get warm pool details
        print("3. Getting warm pool details...")
        resp = requests.get(f"{base_url}/warm-pools/{pool_id}", headers=headers, timeout=30)
        resp.raise_for_status()
        detail = resp.json()
        print(f"   Status: {detail.get('status')}, "
              f"target={detail.get('targetSize')}, current={detail.get('currentSize')}")

        # 4. Update pool size
        print("4. Updating pool size...")
        resp = requests.post(f"{base_url}/warm-pools/{pool_id}/size", headers=headers,
                             timeout=30, json={"targetSize": 5})
        resp.raise_for_status()
        print(f"   Updated: HTTP {resp.status_code}")

        # 5. Delete warm pool (graceful: drain in-flight work, then release idle)
        print("5. Deleting warm pool (graceful)...")
        resp = requests.delete(f"{base_url}/warm-pools/{pool_id}", headers=headers, timeout=120)
        resp.raise_for_status()
        created.remove(pool_id)
        print(f"   Deleted: HTTP {resp.status_code}")

        # 6. Force delete: create another pool and tear it down immediately.
        #    force=true skips the graceful wait (opensandbox: non-graceful
        #    shutdown + idle drain; agent-sandbox: cascade-delete unclaimed
        #    sandboxes). Claimed sandboxes are never affected.
        print("6. Force-deleting a warm pool...")
        resp = requests.post(f"{base_url}/warm-pools", headers=headers, timeout=30, json={
            "templateID": "base",
            "targetSize": 1,
            "warmPoolID": unique_pool_id("wp-py-force"),
        })
        resp.raise_for_status()
        force_id = resp.json()["warmPoolID"]
        created.append(force_id)
        resp = requests.delete(f"{base_url}/warm-pools/{force_id}",
                               headers=headers, params={"force": "true"}, timeout=120)
        resp.raise_for_status()
        created.remove(force_id)
        print(f"   Force deleted {force_id}: HTTP {resp.status_code}")

        print("\nDone.")
    finally:
        for pool_id in created:
            try:
                requests.delete(f"{base_url}/warm-pools/{pool_id}",
                                headers=headers, params={"force": "true"}, timeout=60)
            except requests.RequestException:
                pass  # best-effort; the original error (if any) must surface


if __name__ == "__main__":
    try:
        main()
    except requests.RequestException as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        sys.exit(1)
