# Railse hub 在 Fly.io 上的镜像：下载已发布的 linux/amd64 二进制，按 checksums.txt 校验后放进最小镜像。
# 构建参数 RAILSE_VERSION 对应 GitHub Release 的 tag（去掉 v）。
FROM alpine:3.20 AS fetch
ARG RAILSE_VERSION=0.1.0
ARG RAILSE_REPO=xufei547/railse-opc
RUN apk add --no-cache curl ca-certificates tar
WORKDIR /tmp/rel
RUN curl -fsSL "https://github.com/${RAILSE_REPO}/releases/download/v${RAILSE_VERSION}/railse_${RAILSE_VERSION}_linux_amd64.tar.gz" -o railse.tar.gz \
 && curl -fsSL "https://github.com/${RAILSE_REPO}/releases/download/v${RAILSE_VERSION}/checksums.txt" -o checksums.txt \
 && grep " railse_${RAILSE_VERSION}_linux_amd64.tar.gz\$" checksums.txt | sed 's/ .*/  railse.tar.gz/' | sha256sum -c - \
 && tar -xzf railse.tar.gz railse

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=fetch /tmp/rel/railse /usr/local/bin/railse
COPY entrypoint.sh /usr/local/bin/railse-entrypoint
RUN chmod 755 /usr/local/bin/railse-entrypoint
ENV RAILSE_HUB_HOME=/data
EXPOSE 8787
ENTRYPOINT ["/usr/local/bin/railse-entrypoint"]
