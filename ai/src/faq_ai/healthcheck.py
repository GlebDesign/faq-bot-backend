import grpc
from grpc_health.v1 import health_pb2, health_pb2_grpc

with grpc.insecure_channel("127.0.0.1:50051") as channel:
    response = health_pb2_grpc.HealthStub(channel).Check(
        health_pb2.HealthCheckRequest(service="faq.ai.v1.AIService"), timeout=2)
    raise SystemExit(0 if response.status == health_pb2.HealthCheckResponse.SERVING else 1)
