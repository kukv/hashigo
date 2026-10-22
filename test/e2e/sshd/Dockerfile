FROM alpine:3.20
RUN apk add --no-cache openssh \
 && ssh-keygen -A \
 && adduser -D -s /bin/sh hashigo \
 && echo "hashigo:$(head -c 16 /dev/urandom | base64)" | chpasswd
COPY entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/bin/sh", "/entrypoint.sh"]
