# Client image for the integration test. Uses the same onvif-zeep-async
# version Frigate pins, so the SOAP dialect is identical.
FROM python:3.12-slim-bookworm

RUN pip install --no-cache-dir onvif-zeep-async==4.3.0

COPY test_frigate_init.py /test_frigate_init.py

ENTRYPOINT ["python", "-u", "/test_frigate_init.py"]
