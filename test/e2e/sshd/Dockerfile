FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
RUN apk add --no-cache openssh \
 && ssh-keygen -A \
 && adduser -D -s /bin/sh hashigo \
 && echo "hashigo:$(head -c 16 /dev/urandom | base64)" | chpasswd
COPY entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/bin/sh", "/entrypoint.sh"]
