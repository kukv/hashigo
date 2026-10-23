FROM alpine:3.20
RUN apk add --no-cache openssh-client socat
COPY bin/hashigo /usr/local/bin/hashigo
COPY hashigo.yaml scenario.sh /e2e/
# シナリオは e2e_test.go が docker compose exec で段階ごとに呼ぶ。コンテナは常駐させておく
CMD ["sleep", "infinity"]
