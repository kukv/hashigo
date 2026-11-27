FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache openssh-client socat
COPY bin/hashigo /usr/local/bin/hashigo
COPY hashigo.yaml scenario.sh /e2e/
# シナリオは e2e_test.go が docker compose exec で段階ごとに呼ぶ。コンテナは常駐させておく
CMD ["sleep", "infinity"]
