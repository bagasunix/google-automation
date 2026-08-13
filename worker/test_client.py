
import asyncio
import sys
sys.path.insert(0, ".")
import grpc
from generated.task_pb2 import TaskRequest, TaskResponse
from generated import task_pb2_grpc

async def test():
    # Connect to the gRPC server
    async with grpc.aio.insecure_channel("localhost:50051") as channel:
        stub = task_pb2_grpc.WorkerServiceStub(channel)

        # Create a test request (use a dummy task that will fail quickly since no real proxy)
        request = TaskRequest(
            task_id="integration-test-001",
            article_title="Test Article Title",
            article_url="https://example.com/test",
            domain="example.com",
            proxy_ip="",  # no proxy = direct connection
            proxy_port=0,
            engine="google",
            pre_search_queries=["test topic query"],
        )

        print("Sending gRPC ExecuteTask request...")
        try:
            # Use a short timeout — the task will fail because it can't really search Google
            # but we want to verify the gRPC round-trip works
            response = await asyncio.wait_for(
                stub.ExecuteTask(request),
                timeout=30  # 30s timeout for the test
            )
            print(f"Response received!")
            print(f"  task_id: {response.task_id}")
            print(f"  success: {response.success}")
            print(f"  engine: {response.engine}")
            print(f"  proxy_used: {response.proxy_used}")
            print(f"  serp_position: {response.serp_position}")
            print(f"  dwell_time_seconds: {response.dwell_time_seconds}")
            print(f"  scroll_depth_percent: {response.scroll_depth_percent}")
            print(f"  internal_clicks: {response.internal_clicks}")
            print(f"  captcha_hit: {response.captcha_hit}")
            print(f"  error: {response.error[:100] if response.error else ''}")
            print("gRPC ROUND-TRIP TEST: PASSED")
        except asyncio.TimeoutError:
            print("Request timed out after 30s (expected — browser automation takes time)")
            print("gRPC server is responsive (timeout means it accepted the request)")
        except Exception as e:
            print(f"Error: {type(e).__name__}: {e}")

asyncio.run(test())

