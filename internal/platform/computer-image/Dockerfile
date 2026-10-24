# syntax=docker/dockerfile:1
# Pinned language tools; Ubuntu packages receive Noble security fixes at build.
FROM ghcr.io/astral-sh/uv:0.13.5 AS uv
FROM node:24.22.1-bookworm-slim AS node
FROM golang:1.27.3-bookworm AS go
FROM rust:1.99.1-slim-bookworm AS rust
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 LC_ALL=C.UTF-8 TZ=Etc/UTC \
    HOME=/root SHELL=/bin/bash \
    VIRTUAL_ENV=/opt/venv UV_PYTHON_DOWNLOADS=never \
    PLAYWRIGHT_BROWSERS_PATH=/opt/playwright \
    RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo \
    PATH=/opt/venv/bin:/usr/local/go/bin:/usr/local/cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

RUN apt-get update && apt-get install -y --no-install-recommends \
    bash bash-completion ca-certificates tzdata locales \
    curl wget git git-lfs openssh-client rsync unzip zip tar gzip bzip2 xz-utils zstd \
    ripgrep fd-find jq tree file less nano vim-tiny patch diffutils findutils \
    procps psmisc lsof htop tmux iproute2 iputils-ping dnsutils netcat-openbsd \
    build-essential clang lld cmake ninja-build pkg-config gdb \
    libssl-dev libffi-dev zlib1g-dev libbz2-dev liblzma-dev libsqlite3-dev \
    python3 python3-dev python3-venv python-is-python3 \
    openjdk-21-jdk-headless maven sqlite3 postgresql-client \
    libreoffice-writer libreoffice-calc libreoffice-impress pandoc poppler-utils \
    ffmpeg imagemagick ghostscript tesseract-ocr tesseract-ocr-eng tesseract-ocr-chi-sim tesseract-ocr-chi-tra \
    fonts-noto-core fonts-noto-cjk fonts-noto-color-emoji fontconfig xvfb \
    && ln -s /usr/bin/fdfind /usr/local/bin/fd \
    && git lfs install --system \
    && rm -rf /var/lib/apt/lists/*

COPY --from=uv /uv /uvx /usr/local/bin/
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s /usr/local/lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s /usr/local/lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && npm install -g pnpm@10.31.0 && npm cache clean --force
COPY --from=go /usr/local/go /usr/local/go
COPY --from=rust /usr/local/rustup /usr/local/rustup
COPY --from=rust /usr/local/cargo /usr/local/cargo

# A shared venv keeps the system Python managed by apt while making the common
# document/data tools available to every Agent without per-Agent bootstrap.
RUN uv venv --python /usr/bin/python3 /opt/venv \
    && uv pip install --python /opt/venv/bin/python \
       numpy==2.5.4 pandas==3.1.0 scipy==1.18.1 matplotlib==3.11.2 pillow==13.0.0 \
       python-docx==1.2.0 python-pptx==1.0.2 openpyxl==3.1.5 \
       pypdf==6.21.2 pdfplumber==0.11.10 reportlab==5.1.0 \
       requests==2.34.2 beautifulsoup4==4.15.0 pyyaml==6.0.3 playwright==1.64.1 \
    && python -m playwright install --with-deps chromium \
    && uv cache clean && rm -rf /var/lib/apt/lists/* /root/.cache

WORKDIR /var/lib/astral
# Keep this last: daemon releases reuse the expensive environment layers.
COPY --chmod=755 astral /usr/local/bin/astral
ENTRYPOINT ["/usr/local/bin/astral"]
