package config

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/parser"
)

// Problem は設定ファイルの問題1件。Line が 0 なら位置が分からない。
type Problem struct {
	File string
	Line int
	Col  int
	Msg  string
	path []any
}

func (p Problem) String() string {
	switch {
	case p.File != "" && p.Line > 0:
		return fmt.Sprintf("%s:%d:%d: %s", p.File, p.Line, p.Col, p.Msg)
	case p.File != "":
		return fmt.Sprintf("%s: %s", p.File, p.Msg)
	}
	return p.Msg
}

// Problems は見つかった問題の一覧。
type Problems []Problem

// Lines は1件1行の表示用の文字列を返す。
func (ps Problems) Lines() []string {
	lines := make([]string, len(ps))
	for i, p := range ps {
		lines[i] = p.String()
	}
	return lines
}

func (ps Problems) Error() string { return strings.Join(ps.Lines(), "\n") }

var posRe = regexp.MustCompile(`(?s)^\[(\d+):(\d+)\]\s*(.*)$`)

// syntaxProblem は goccy/go-yaml のエラーを Problem にする。
func syntaxProblem(file string, err error) Problem {
	msg := strings.TrimSpace(yaml.FormatError(err, false, false))
	if m := posRe.FindStringSubmatch(msg); m != nil {
		line, _ := strconv.Atoi(m[1])
		col, _ := strconv.Atoi(m[2])
		return Problem{File: file, Line: line, Col: col, Msg: firstLine(m[3])}
	}
	return Problem{File: file, Msg: firstLine(msg)}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// locate は YAML 上のパスに対応する行と列を返す。そのパスが無ければ親をたどる。
func locate(data []byte, path []any) (int, int) {
	f, err := parser.ParseBytes(data, 0)
	if err != nil {
		return 0, 0
	}
	for n := len(path); n > 0; n-- {
		b := (&yaml.PathBuilder{}).Root()
		for _, e := range path[:n] {
			switch v := e.(type) {
			case string:
				b = b.Child(v)
			case int:
				b = b.Index(uint(v))
			}
		}
		node, err := b.Build().FilterFile(f)
		if err == nil && node != nil {
			pos := node.GetToken().Position
			return pos.Line, pos.Column
		}
	}
	return 0, 0
}

func sortProblems(ps Problems) {
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Col < b.Col
	})
}
