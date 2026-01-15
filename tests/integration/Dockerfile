FROM ubuntu:22.04

# Install minimal dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Create test user for permission tests
RUN useradd -m testuser

WORKDIR /workspace
