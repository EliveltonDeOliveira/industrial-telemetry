# Image of the anomaly service. Usage, from analytics/anomaly: docker build .
# Python 3.14 because confluent-kafka has no wheels for 3.15 yet.
FROM python:3.14.9-slim
ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PIP_ROOT_USER_ACTION=ignore \
    PROMETHEUS_DISABLE_CREATED_SERIES=true
WORKDIR /app
COPY requirements.txt .
RUN pip install -r requirements.txt
COPY anomaly ./anomaly
USER 65534:65534
ENTRYPOINT ["python", "-m", "anomaly"]
