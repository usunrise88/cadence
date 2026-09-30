# Cadence GPU worker — runtime `nemo-speech` (R40): the NeMo Speech 26.07 image pinned by digest, plus the worker
# harness, the runtime-neutral core step kinds and the NeMo pack (distribution cadence-nemo: the Nemotron 3.5 streaming
# family and its step kinds). PyTorch, NeMo, Lhotse and huggingface_hub come from the image and are never reinstalled.
# Build context is the repository root:
#   docker build -f worker/Dockerfile -t cadence/worker:dev .
FROM nvcr.io/nvidia/nemo-speech:26.07@sha256:b8b1c094f1bbd1a28acec4a742ae8984f420c021293dd68e7cadb21ac2963ef0

ENV PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1 PYTHONUNBUFFERED=1
COPY worker/pyproject.toml /src/worker/pyproject.toml
COPY worker/cadence_worker /src/worker/cadence_worker
COPY worker/packs/nemo/pyproject.toml /src/nemo/pyproject.toml
COPY worker/packs/nemo/cadence_nemo /src/nemo/cadence_nemo
# Only what the harness needs beyond the image (blake3); the image's httpx, pydantic, PyYAML and numpy satisfy the
# ranges. The pack installs from its own directory beside the harness it depends on.
RUN python -m pip install /src/worker /src/nemo && rm -rf /src/worker /src/nemo

COPY control-plane/defaults/defaults.yaml /opt/cadence/defaults.yaml
COPY worker/runtime/nemo-speech.json /etc/cadence/runtime.json

# Non-root with the control plane's uid (the content store's blobs are 0440); the card needs no root. The image's
# Python lives under /root/.local/share/uv: /root gets traverse-only permission so that uid can reach it. NeMo is
# installed editable from /workspace/nemo with mode 0660 files, so the tree is made world-readable (15 MB).
RUN chmod o+x /root && chmod -R o+rX /workspace \
 && groupadd --gid 65532 nonroot \
 && useradd --uid 65532 --gid 65532 --no-create-home --home-dir /var/lib/cadence --shell /usr/sbin/nologin nonroot \
 && mkdir -p /var/lib/cadence/cas /var/lib/cadence/scratch /worker-credential \
 && chown -R 65532:65532 /var/lib/cadence /worker-credential

ARG CADENCE_VERSION=dev
ENV CADENCE_VERSION=${CADENCE_VERSION} \
    CADENCE_DEFAULTS_FILE=/opt/cadence/defaults.yaml \
    CADENCE_RUNTIME_FILE=/etc/cadence/runtime.json \
    CADENCE_CAS_DIR=/var/lib/cadence/cas \
    CADENCE_WORKER_SCRATCH=/var/lib/cadence/scratch \
    HOME=/var/lib/cadence \
    HF_HOME=/var/lib/cadence/hf
USER 65532:65532
WORKDIR /var/lib/cadence
# NVIDIA's entrypoint stays (it checks the driver and prints the banner); the command is the worker.
CMD ["python", "-m", "cadence_worker", "serve"]
