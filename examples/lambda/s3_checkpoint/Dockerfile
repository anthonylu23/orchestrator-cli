FROM python:3.12-slim

RUN pip install --no-cache-dir awscli
WORKDIR /app
COPY train.py /app/train.py

ENTRYPOINT ["python", "/app/train.py"]
