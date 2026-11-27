FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
RUN apk add --no-cache openssh-client socat
COPY bin/hashigo /usr/local/bin/hashigo
COPY hashigo.yaml scenario.sh /e2e/
# シナリオは e2e_test.go が docker compose exec で段階ごとに呼ぶ。コンテナは常駐させておく
CMD ["sleep", "infinity"]
