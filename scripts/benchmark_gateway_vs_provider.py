#!/usr/bin/env python3
"""Benchmark gollem gateway latency/overhead vs direct provider calls.

Usage (similar to LiteLLM benchmark workflow):
  1) Start gollem gateway in one terminal.
  2) Run: python scripts/benchmark_gateway_vs_provider.py --requests 2000 --max-concurrent 200 --runs 3

Required env vars:
  PROVIDER_URL              Direct provider chat completions URL.

Optional env vars:
  GATEWAY_URL               gollem endpoint (default: http://localhost:8000/v1/chat/completions)
  GATEWAY_API_KEY           Gateway client key.
  PROVIDER_API_KEY          Provider key.
  PROVIDER_API_KEY_HEADER   Provider key header (default: api-key for Azure, use Authorization for OpenAI)
  PROVIDER_API_KEY_PREFIX   Prefix for provider key value (example: Bearer)
  BENCHMARK_MODEL           Model/deployment in payload (default: gpt4o)
"""

from __future__ import annotations

import argparse
import asyncio
import collections
import json
import os
import statistics
import sys
import time
from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional

import aiohttp


@dataclass
class RequestStats:
    success: bool
    latency_seconds: float
    status_code: int = 0
    error: str = ""


@dataclass
class BenchmarkResults:
    total_requests: int = 0
    successful_requests: int = 0
    failed_requests: int = 0
    latencies_seconds: List[float] = field(default_factory=list)
    errors: List[str] = field(default_factory=list)
    status_codes: Dict[int, int] = field(default_factory=dict)
    wall_time_seconds: float = 0.0

    def stats(self) -> Dict[str, Any]:
        base: Dict[str, Any] = {
            "total_requests": self.total_requests,
            "successful_requests": self.successful_requests,
            "failed_requests": self.failed_requests,
            "success_rate": (self.successful_requests / self.total_requests * 100.0)
            if self.total_requests
            else 0.0,
            "throughput_rps": (self.total_requests / self.wall_time_seconds)
            if self.wall_time_seconds > 0
            else 0.0,
            "successful_throughput_rps": (self.successful_requests / self.wall_time_seconds)
            if self.wall_time_seconds > 0
            else 0.0,
            "wall_time_seconds": self.wall_time_seconds,
            "status_codes": self.status_codes,
        }

        if not self.latencies_seconds:
            return base

        sorted_lats = sorted(self.latencies_seconds)
        base["latency_ms"] = {
            "mean": statistics.mean(sorted_lats) * 1000.0,
            "p50": percentile(sorted_lats, 50) * 1000.0,
            "p95": percentile(sorted_lats, 95) * 1000.0,
            "p99": percentile(sorted_lats, 99) * 1000.0,
            "min": sorted_lats[0] * 1000.0,
            "max": sorted_lats[-1] * 1000.0,
        }
        return base


def percentile(sorted_values: List[float], pct: int) -> float:
    if not sorted_values:
        return 0.0
    idx = int(len(sorted_values) * (pct / 100.0))
    if idx >= len(sorted_values):
        idx = len(sorted_values) - 1
    return sorted_values[idx]


def build_auth_headers(
    api_key: str,
    key_header: str,
    key_prefix: str,
) -> Dict[str, str]:
    headers = {"Content-Type": "application/json"}
    if not api_key:
        return headers

    if key_prefix:
        headers[key_header] = f"{key_prefix} {api_key}"
    else:
        headers[key_header] = api_key
    return headers


async def send_one(
    session: aiohttp.ClientSession,
    url: str,
    headers: Dict[str, str],
    payload: Dict[str, Any],
    timeout: aiohttp.ClientTimeout,
    semaphore: asyncio.Semaphore,
) -> RequestStats:
    async with semaphore:
        start = time.perf_counter()
        try:
            async with session.post(url, json=payload, headers=headers, timeout=timeout) as resp:
                body = await resp.read()
                elapsed = time.perf_counter() - start
                if resp.status == 200:
                    try:
                        json.loads(body)
                    except json.JSONDecodeError:
                        return RequestStats(False, elapsed, resp.status, "invalid JSON")
                    return RequestStats(True, elapsed, resp.status)
                short_error = body.decode("utf-8", errors="ignore")[:120]
                return RequestStats(False, elapsed, resp.status, f"HTTP {resp.status}: {short_error}")
        except asyncio.TimeoutError:
            return RequestStats(False, time.perf_counter() - start, 0, "timeout")
        except Exception as exc:  # noqa: BLE001
            return RequestStats(False, time.perf_counter() - start, 0, str(exc)[:120])


async def run_benchmark(
    url: str,
    headers: Dict[str, str],
    payload: Dict[str, Any],
    requests: int,
    max_concurrent: int,
    timeout_seconds: int,
    warmup_requests: int,
) -> BenchmarkResults:
    timeout = aiohttp.ClientTimeout(total=timeout_seconds)
    connector = aiohttp.TCPConnector(
        limit=min(max_concurrent * 2, 500),
        limit_per_host=max_concurrent,
        force_close=False,
        enable_cleanup_closed=True,
    )

    semaphore = asyncio.Semaphore(max_concurrent)

    async with aiohttp.ClientSession(connector=connector) as session:
        if warmup_requests > 0:
            warmup_tasks = [
                send_one(session, url, headers, payload, timeout, semaphore)
                for _ in range(warmup_requests)
            ]
            await asyncio.gather(*warmup_tasks)

        start = time.perf_counter()
        tasks = [
            send_one(session, url, headers, payload, timeout, semaphore)
            for _ in range(requests)
        ]
        run_results = await asyncio.gather(*tasks)
        wall_elapsed = time.perf_counter() - start

    out = BenchmarkResults(total_requests=requests, wall_time_seconds=wall_elapsed)
    for r in run_results:
        if r.success:
            out.successful_requests += 1
            out.latencies_seconds.append(r.latency_seconds)
        else:
            out.failed_requests += 1
            out.errors.append(r.error)

        if r.status_code > 0:
            out.status_codes[r.status_code] = out.status_codes.get(r.status_code, 0) + 1

    return out


def aggregate_runs(results: List[BenchmarkResults]) -> BenchmarkResults:
    if not results:
        return BenchmarkResults()

    agg = BenchmarkResults()
    agg.total_requests = sum(r.total_requests for r in results)
    agg.successful_requests = sum(r.successful_requests for r in results)
    agg.failed_requests = sum(r.failed_requests for r in results)
    agg.wall_time_seconds = statistics.mean(r.wall_time_seconds for r in results)
    for r in results:
        agg.latencies_seconds.extend(r.latencies_seconds)
        agg.errors.extend(r.errors)
        for code, count in r.status_codes.items():
            agg.status_codes[code] = agg.status_codes.get(code, 0) + count
    return agg


def print_stats(label: str, result: BenchmarkResults) -> None:
    stats = result.stats()
    print(f"\n{'=' * 64}")
    print(f"{label}")
    print(f"{'=' * 64}")
    print(f"Requests:      {stats['total_requests']} (failed: {stats['failed_requests']})")
    print(f"Success rate:  {stats['success_rate']:.2f}%")
    print(f"Wall time:     {stats['wall_time_seconds']:.2f}s")
    print(f"Throughput:    {stats['throughput_rps']:.2f} req/s (attempted)")
    print(f"Throughput:    {stats['successful_throughput_rps']:.2f} req/s (successful)")

    if "latency_ms" in stats:
        latency = stats["latency_ms"]
        print("Latency (ms):")
        print(f"  mean: {latency['mean']:.2f}")
        print(f"  p50:  {latency['p50']:.2f}")
        print(f"  p95:  {latency['p95']:.2f}")
        print(f"  p99:  {latency['p99']:.2f}")

    if stats["status_codes"]:
        print("Status codes:")
        for code in sorted(stats["status_codes"]):
            print(f"  {code}: {stats['status_codes'][code]}")

    if result.errors:
        top = collections.Counter(result.errors).most_common(5)
        print("Top errors:")
        for err, count in top:
            print(f"  {count}x {err}")


def print_comparison(gateway: BenchmarkResults, provider: BenchmarkResults) -> None:
    g = gateway.stats()
    p = provider.stats()

    print(f"\n{'=' * 64}")
    print("gollem Overhead vs Direct Provider")
    print(f"{'=' * 64}")

    throughput_delta = g["successful_throughput_rps"] - p["successful_throughput_rps"]
    throughput_pct = (throughput_delta / p["successful_throughput_rps"] * 100.0) if p["successful_throughput_rps"] else 0.0
    print(f"Successful throughput delta: {throughput_delta:+.2f} req/s ({throughput_pct:+.2f}%)")

    if "latency_ms" not in g or "latency_ms" not in p:
        print("Insufficient successful responses to compute latency overhead.")
        return

    print("Latency overhead (gateway - direct):")
    for metric in ["mean", "p50", "p95", "p99"]:
        g_val = g["latency_ms"][metric]
        p_val = p["latency_ms"][metric]
        delta = g_val - p_val
        pct = (delta / p_val * 100.0) if p_val else 0.0
        print(f"  {metric:>4}: {delta:+.2f} ms ({pct:+.2f}%)")


def parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description="Benchmark gollem gateway vs direct provider endpoint")
    p.add_argument("--requests", type=int, default=2000, help="Requests per run per endpoint")
    p.add_argument("--max-concurrent", type=int, default=200, help="Max concurrent requests")
    p.add_argument("--runs", type=int, default=3, help="Number of benchmark runs")
    p.add_argument("--timeout", type=int, default=60, help="Per-request timeout in seconds")
    p.add_argument("--warmup", type=int, default=25, help="Warm-up requests per endpoint (first run only)")
    p.add_argument(
        "--parallel",
        action="store_true",
        help="Run gateway/provider benchmarks in parallel (default is sequential for cleaner numbers)",
    )
    return p


async def main() -> int:
    args = parser().parse_args()

    provider_url = os.getenv("PROVIDER_URL", "").strip()
    gateway_url = os.getenv("GATEWAY_URL", "http://localhost:8000/v1/chat/completions").strip()

    if not provider_url:
        print("Error: PROVIDER_URL is required", file=sys.stderr)
        return 1

    gateway_key = os.getenv("GATEWAY_API_KEY", "").strip()
    provider_key = os.getenv("PROVIDER_API_KEY", "").strip()

    provider_key_header = os.getenv("PROVIDER_API_KEY_HEADER", "api-key").strip() or "api-key"
    provider_key_prefix = os.getenv("PROVIDER_API_KEY_PREFIX", "").strip()

    benchmark_model = os.getenv("BENCHMARK_MODEL", "gpt4o").strip() or "gpt4o"

    gateway_headers = build_auth_headers(gateway_key, "Authorization", "Bearer")
    provider_headers = build_auth_headers(provider_key, provider_key_header, provider_key_prefix)

    payload = {
        "model": benchmark_model,
        "messages": [{"role": "user", "content": "Respond with exactly: ok"}],
        "max_tokens": 8,
        "temperature": 0,
    }

    print(f"Gateway URL:   {gateway_url}")
    print(f"Provider URL:  {provider_url}")
    print(f"Runs:          {args.runs}")
    print(f"Requests/run:  {args.requests}")
    print(f"Concurrency:   {args.max_concurrent}")
    print(f"Mode:          {'parallel' if args.parallel else 'sequential'}")

    all_gateway: List[BenchmarkResults] = []
    all_provider: List[BenchmarkResults] = []

    for run in range(1, args.runs + 1):
        warmup = args.warmup if run == 1 else 0
        print(f"\n--- Run {run}/{args.runs} ---")

        if args.parallel:
            gw_res, pv_res = await asyncio.gather(
                run_benchmark(
                    gateway_url,
                    gateway_headers,
                    payload,
                    args.requests,
                    args.max_concurrent,
                    args.timeout,
                    warmup,
                ),
                run_benchmark(
                    provider_url,
                    provider_headers,
                    payload,
                    args.requests,
                    args.max_concurrent,
                    args.timeout,
                    warmup,
                ),
            )
        else:
            gw_res = await run_benchmark(
                gateway_url,
                gateway_headers,
                payload,
                args.requests,
                args.max_concurrent,
                args.timeout,
                warmup,
            )
            await asyncio.sleep(2)
            pv_res = await run_benchmark(
                provider_url,
                provider_headers,
                payload,
                args.requests,
                args.max_concurrent,
                args.timeout,
                warmup,
            )

        all_gateway.append(gw_res)
        all_provider.append(pv_res)

    gateway_agg = aggregate_runs(all_gateway)
    provider_agg = aggregate_runs(all_provider)

    print_stats("gollem gateway", gateway_agg)
    print_stats("direct provider", provider_agg)
    print_comparison(gateway_agg, provider_agg)
    return 0


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
