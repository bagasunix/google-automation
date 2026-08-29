"""
Quick single-proxy smoke test.
Calls execute_task_sync directly — no gRPC server needed.
"""
import sys, os, types
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import main as worker_main

# Fake args: headless, skip cooldown
_args = types.SimpleNamespace(headed=False, no_cooldown=True)
worker_main._ARGS = _args

from generated.task_pb2 import TaskRequest

req = TaskRequest(
    task_id="smoke-proxy-test-001",
    article_title="bagasunix",
    article_url="https://bagasunix.com",
    domain="bagasunix.com",
    proxy_ip="",
    proxy_port=0,
    proxy_username="",
    proxy_password="",
    proxy_country="",
    engine="google",
    pre_search_queries=["bagasunix"],
    pre_search_enabled=True,
    pre_search_2_chance=0.0,
    serp_casual_click_chance=0.0,
    competitor_click_chance=0.0,
    distraction_exit_chance=0.0,
)

print("=== RUNNING SINGLE PROXY TEST ===")
print(f"Proxy: {req.proxy_ip}:{req.proxy_port} ({req.proxy_country})")
print(f"Domain: {req.domain}")
print()

result = worker_main.execute_task_sync(req)

print()
print("=== RESULT ===")
print(f"success:        {result.success}")
print(f"serp_position:  {result.serp_position}")
print(f"dwell_time:     {result.dwell_time_seconds}s")
print(f"scroll_depth:   {result.scroll_depth_percent}%")
print(f"internal_clicks:{result.internal_clicks}")
print(f"captcha_hit:    {result.captcha_hit}")
print(f"proxy_used:     {result.proxy_used}")
print(f"error:          {result.error[:200] if result.error else '-'}")
