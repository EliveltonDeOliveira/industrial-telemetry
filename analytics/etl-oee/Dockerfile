# Image of the etl-oee service. Usage, from analytics/etl-oee: docker build .
# Python 3.14, the same as analytics/anomaly.
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
COPY etl_oee ./etl_oee
USER 65534:65534
ENTRYPOINT ["python", "-m", "etl_oee"]
