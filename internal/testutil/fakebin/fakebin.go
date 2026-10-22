// Package fakebin はテストバイナリ自身を偽の ssh として振る舞わせる。
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

// Main は偽 ssh として動き、戻らない。
func Main() {
	if os.Getenv("FAKE_IGNORE_TERM") == "1" {
		signal.Ignore(syscall.SIGTERM)
	}
	if s := os.Getenv("FAKE_STDERR"); s != "" {
		fmt.Fprintln(os.Stderr, s)
	}
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] != "-L" || i+1 >= len(args) {
			continue
		}
		i++
		ln, err := net.Listen("tcp", listenAddr(args[i]))
		if err != nil {
			fmt.Fprintln(os.Stderr, "bind に失敗しました:", err)
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
			defer c.Close()
			io.Copy(c, c)
		}()
	}
}
