// Package fakebin はテストバイナリ自身を偽の ssh / aws として振る舞わせる。
// テストの TestMain で `if fakebin.Enabled() { fakebin.Main() }` と呼ぶ。
package fakebin

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Enabled は偽バイナリとして起動されたかを返す。
func Enabled() bool { return os.Getenv("HASHIGO_FAKE") == "1" }

// Main は偽 ssh / 偽 aws として動き、戻らない。
// ssh の -L と、aws ssm start-session の --parameters にある localPortNumber で待ち受ける。
func Main() {
	if os.Getenv("FAKE_IGNORE_TERM") == "1" {
		signal.Ignore(syscall.SIGTERM)
	}
	if s := os.Getenv("FAKE_STDERR"); s != "" {
		_, _ = fmt.Fprintln(os.Stderr, s)
	}
	args := os.Args[1:]
	for i := 0; i < len(args)-1; i++ {
		var addr string
		switch args[i] {
		case "-L":
			addr = listenAddr(args[i+1])
		case "--parameters":
			p := localPortNumber(args[i+1])
			if p == "" {
				continue
			}
			addr = net.JoinHostPort("127.0.0.1", p)
		default:
			continue
		}
		i++
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "bind に失敗しました:", err)
			os.Exit(255)
		}
		go serve(ln)
	}
	if d, err := time.ParseDuration(os.Getenv("FAKE_EXIT_AFTER")); err == nil {
		time.Sleep(d)
		code := 1
		if c, err := strconv.Atoi(os.Getenv("FAKE_EXIT_CODE")); err == nil {
			code = c
		}
		os.Exit(code)
	}
	for {
		time.Sleep(time.Hour)
	}
}

// localPortNumber は host=db,portNumber=5432,localPortNumber=15432 から 15432 を取り出す。
func localPortNumber(params string) string {
	for _, kv := range strings.Split(params, ",") {
		if v, ok := strings.CutPrefix(kv, "localPortNumber="); ok {
			return v
		}
	}
	return ""
}

// listenAddr は -L の値から待ち受けアドレスを取り出す。
func listenAddr(spec string) string {
	parts := strings.Split(spec, ":")
	if len(parts) == 4 {
		return net.JoinHostPort(parts[0], parts[1])
	}
	return net.JoinHostPort("127.0.0.1", parts[0])
}

func serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = c.Close() }()
			_, _ = io.Copy(c, c)
		}()
	}
}
