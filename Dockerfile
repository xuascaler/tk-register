FROM debian:bookworm-slim

RUN sed -i 's|http://deb.debian.org/debian-security|http://mirrors.aliyun.com/debian-security|g; s|http://deb.debian.org/debian|http://mirrors.aliyun.com/debian|g' /etc/apt/sources.list.d/debian.sources \
    && apt-get -o Acquire::http::Timeout=20 -o Acquire::Retries=2 update \
    && apt-get install -y --no-install-recommends \
      bash ca-certificates curl nginx tini tzdata \
    && rm -rf /var/lib/apt/lists/* /etc/nginx/sites-enabled/default

COPY tk-register-server /usr/local/bin/tk-register-server
COPY dist/ /opt/tk-register-web-portal/
COPY nginx.conf /etc/nginx/nginx.conf
COPY start.sh /usr/local/bin/tk-register-start
RUN chmod 0755 /usr/local/bin/tk-register-server /usr/local/bin/tk-register-start

ENV TK_REGISTER_PORT=3002 \
    TK_REGISTER_ASSETS=/opt/tk-register-web-portal \
    TZ=Asia/Shanghai

EXPOSE 8443
HEALTHCHECK --interval=30s --timeout=5s --start-period=45s --retries=3 \
  CMD curl --fail --silent --show-error --max-time 4 http://127.0.0.1:3002/tk-register/healthz > /dev/null || exit 1

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/tk-register-start"]
