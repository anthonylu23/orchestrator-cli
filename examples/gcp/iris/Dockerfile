FROM python:3.11-slim

ENV PYTHONUNBUFFERED=1

WORKDIR /app

COPY examples/gcp/iris/requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir -r /app/requirements.txt

COPY examples/iris_pytorch.py /app/iris_pytorch.py
COPY examples/gcp/iris/train_iris_gcs.py /app/train_iris_gcs.py

ENTRYPOINT ["python", "/app/train_iris_gcs.py"]
