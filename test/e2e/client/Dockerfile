FROM alpine:3.20
RUN apk add --no-cache openssh-client socat
COPY bin/hashigo /usr/local/bin/hashigo
COPY hashigo.yaml scenario.sh /e2e/
ENTRYPOINT ["/bin/sh", "/e2e/scenario.sh"]
