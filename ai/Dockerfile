FROM python:3.12-slim
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1
WORKDIR /app
COPY ai/requirements.lock.txt /app/requirements.lock.txt
RUN pip install -r requirements.lock.txt
COPY ai/pyproject.toml /app/pyproject.toml
COPY ai/src /app/src
RUN pip install --no-deps . && useradd --create-home --uid 10001 faqai
USER 10001
EXPOSE 50051
CMD ["python", "-m", "faq_ai.grpc_server"]
