"""Warm Pools - Manage pre-warmed sandbox pools.

Demonstrates warm pool operations:
1. List warm pools
2. Create a warm pool
3. Get warm pool details
4. Update pool size
5. Delete warm pool

Usage:
    export E2B_DOMAIN=localhost:8080
    export E2B_API_KEY=your-api-key
    python warm_pools.py
"""

import os
import requests


def main():
    base_url = os.environ.get("E2B_DOMAIN", "localhost:8080")
    if not base_url.startswith("http"):
        base_url = f"http://{base_url}"
    api_key = os.environ.get("E2B_API_KEY", "test-key")
    headers = {"X-API-Key": api_key, "Content-Type": "application/json"}

    # 1. List warm pools
    print("1. Listing warm pools...")
    resp = requests.get(f"{base_url}/warm-pools", headers=headers)
    pools = resp.json()
    print(f"   Existing pools: {len(pools)}")

    # 2. Create a warm pool
    print("2. Creating warm pool...")
    resp = requests.post(f"{base_url}/warm-pools", headers=headers, json={
        "templateID": "base",
        "size": 3,
    })
    pool = resp.json()
    pool_id = pool.get("warmPoolId", pool.get("id", ""))
    print(f"   Pool: {pool_id}")

    # 3. Get warm pool details
    if pool_id:
        print("3. Getting warm pool details...")
        resp = requests.get(f"{base_url}/warm-pools/{pool_id}", headers=headers)
        detail = resp.json()
        print(f"   Template: {detail.get('templateID', detail.get('templateId'))}")
        print(f"   Size: {detail.get('size')}")

        # 4. Update pool size
        print("4. Updating pool size...")
        resp = requests.post(f"{base_url}/warm-pools/{pool_id}/size", headers=headers, json={
            "size": 5,
        })
        print(f"   Updated: {resp.status_code}")

        # 5. Delete warm pool
        print("5. Deleting warm pool...")
        resp = requests.delete(f"{base_url}/warm-pools/{pool_id}", headers=headers)
        print(f"   Deleted: {resp.status_code}")

    print("\nDone.")


if __name__ == "__main__":
    main()
