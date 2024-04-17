ARG BASE_VAULT_VERSION=1.16.0

# Clone sources from https://github.com/yandex-cloud/vault.git and build vault binary with support for Yandex KMS
FROM --platform=linux/amd64 golang:1.21.8
ARG BASE_VAULT_VERSION
WORKDIR /
RUN apt update -y && apt install -y build-essential
RUN wget https://nodejs.org/dist/v18.20.2/node-v18.20.2-linux-x64.tar.xz \
  && tar -oxf node-v18.20.2-linux-x64.tar.xz
ENV PATH="$PATH:/node-v18.20.2-linux-x64/bin"

RUN npm install -g yarn

RUN git clone --depth 1 -b v${BASE_VAULT_VERSION}+yckms https://github.com/paraddise/vault.git \
  && cd vault \
  && make bootstrap static-dist dev-ui

# Replace vault binary in official vault image with the binary built on the previous stage
FROM hashicorp/vault:${BASE_VAULT_VERSION}
COPY --from=0 /vault/bin/vault /bin/vault
