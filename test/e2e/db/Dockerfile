FROM alpine:3.20
RUN apk add --no-cache socat
CMD ["socat", "TCP-LISTEN:5432,fork,reuseaddr", "EXEC:cat"]
