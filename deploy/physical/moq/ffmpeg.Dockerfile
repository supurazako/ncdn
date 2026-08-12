FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ffmpeg fonts-dejavu-core \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

ENTRYPOINT ["ffmpeg"]
