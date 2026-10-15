package fstool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func newFS(t *testing.T, configure func(*Options)) (*FS, string) {
	t.Helper()
	dir := t.TempDir()
	opts := DefaultOptions(dir)
	if configure != nil {
		configure(&opts)
	}
	f, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	return f, dir
}

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func execute(t *testing.T, definition tools.Definition, args string) tools.ExecutionResult {
	t.Helper()
	result, err := definition.Execute(context.Background(), tools.Execution{Arguments: json.RawMessage(args)})
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	validateSchema(t, definition.Output, result.Value)
	if result.IsError || result.Meta != nil || len(result.Content) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	return result
}

func textOf(t *testing.T, result tools.ExecutionResult) string {
	t.Helper()
	block, ok := result.Content[0].(protocol.TextBlock)
	if !ok {
		t.Fatalf("expected TextBlock: %T", result.Content[0])
	}
	return block.Text
}

func compileSchema(t *testing.T, raw tools.JSONSchema) *jsonschema.Schema {
	t.Helper()
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("schema.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validateSchema(t *testing.T, schema tools.JSONSchema, raw json.RawMessage) {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if err := compileSchema(t, schema).Validate(value); err != nil {
		t.Fatal(err)
	}
}

// These fixed cases are transcribed from DeepSeek Harness
// packages/fs/tool-fs/tests/{read-render,tools}.spec.ts and the rendering in
// src/read-render.ts (local source inspected 2026-10-14). Unicode cases extend
// them using that source's UTF-16 length/substring and UTF-8 byte semantics.
// No DSH checkout, model, or network is used by the tests.
func TestReadDSHCases(t *testing.T) {
	tests := []struct {
		name, input, args            string
		readLimit, maxLine, maxBytes int
		lines                        []Line
		offset, total                int
		body                         string
	}{
		{"default", "hello\nworld", `{"file_path":"a.txt"}`, 0, 0, 0, []Line{{1, "hello"}, {2, "world"}}, 1, 2, "1: hello\n2: world\n\n(End of file - total 2 lines)"},
		{"page", "one\ntwo\nthree\nfour", `{"file_path":"a.txt","offset":2,"limit":2}`, 0, 0, 0, []Line{{2, "two"}, {3, "three"}}, 2, 4, "2: two\n3: three\n\n(Showing lines 2-3 of 4. Use offset=4 to continue.)"},
		{"configured default", "one\ntwo\nthree", `{"file_path":"a.txt"}`, 2, 0, 0, []Line{{1, "one"}, {2, "two"}}, 1, 3, "1: one\n2: two\n\n(Showing lines 1-2 of 3. Use offset=3 to continue.)"},
		{"empty", "", `{"file_path":"a.txt"}`, 0, 0, 0, []Line{}, 1, 0, "(End of file - total 0 lines)"},
		{"CRLF", "one\r\ntwo\r\n", `{"file_path":"a.txt"}`, 0, 0, 0, []Line{{1, "one"}, {2, "two"}}, 1, 2, "1: one\n2: two\n\n(End of file - total 2 lines)"},
		{"tail newline", "one\ntwo\n", `{"file_path":"a.txt"}`, 0, 0, 0, []Line{{1, "one"}, {2, "two"}}, 1, 2, "1: one\n2: two\n\n(End of file - total 2 lines)"},
		{"no tail newline", "one\ntwo", `{"file_path":"a.txt"}`, 0, 0, 0, []Line{{1, "one"}, {2, "two"}}, 1, 2, "1: one\n2: two\n\n(End of file - total 2 lines)"},
		{"blank lines", "\n\n", `{"file_path":"a.txt"}`, 0, 0, 0, []Line{{1, ""}, {2, ""}}, 1, 2, "1: \n2: \n\n(End of file - total 2 lines)"},
		{"long line", "abcdefghij", `{"file_path":"a.txt"}`, 0, 5, 0, []Line{{1, "abcde... (line truncated to 5 chars)"}}, 1, 1, "1: abcde... (line truncated to 5 chars)\n\n(End of file - total 1 lines)"},
		{"bytes include newline", "aaaa\nbbbb\ncccc", `{"file_path":"a.txt"}`, 0, 0, 9, []Line{{1, "aaaa"}, {2, "bbbb"}}, 1, 3, "1: aaaa\n2: bbbb\n\n(Output capped. Showing lines 1-2. Use offset=3 to continue.)"},
		{"byte cap first line", "hello\nworld", `{"file_path":"a.txt"}`, 0, 0, 4, []Line{}, 1, 2, "(Output capped. Showing lines 1-0. Use offset=1 to continue.)"},
		{"byte cap offset", "x\nhello\nworld", `{"file_path":"a.txt","offset":2}`, 0, 0, 4, []Line{}, 2, 3, "(Output capped. Showing lines 2-1. Use offset=2 to continue.)"},
		{"byte cap at EOF", "aaaa", `{"file_path":"a.txt"}`, 0, 0, 4, []Line{{1, "aaaa"}}, 1, 1, "1: aaaa\n\n(End of file - total 1 lines)"},
		{"Chinese chars", "你好世界", `{"file_path":"a.txt"}`, 0, 2, 0, []Line{{1, "你好... (line truncated to 2 chars)"}}, 1, 1, "1: 你好... (line truncated to 2 chars)\n\n(End of file - total 1 lines)"},
		{"Chinese bytes", "你好\n世", `{"file_path":"a.txt"}`, 0, 0, 6, []Line{{1, "你好"}}, 1, 2, "1: 你好\n\n(Output capped. Showing lines 1-1. Use offset=2 to continue.)"},
		{"non BMP units", "😀x", `{"file_path":"a.txt"}`, 0, 2, 0, []Line{{1, "😀... (line truncated to 2 chars)"}}, 1, 1, "1: 😀... (line truncated to 2 chars)\n\n(End of file - total 1 lines)"},
		{"non BMP split", "😀x", `{"file_path":"a.txt"}`, 0, 1, 0, []Line{{1, "�... (line truncated to 1 chars)"}}, 1, 1, "1: �... (line truncated to 1 chars)\n\n(End of file - total 1 lines)"},
		{"non BMP bytes", "😀\nx", `{"file_path":"a.txt"}`, 0, 0, 4, []Line{{1, "😀"}}, 1, 2, "1: 😀\n\n(Output capped. Showing lines 1-1. Use offset=2 to continue.)"},
		{"exact line cap CRLF", "abcde\r\n", `{"file_path":"a.txt"}`, 0, 5, 0, []Line{{1, "abcde"}}, 1, 1, "1: abcde\n\n(End of file - total 1 lines)"},
		{"numeric integer syntax", "x", `{"file_path":"a.txt","offset":1.0,"limit":1e0}`, 0, 0, 0, []Line{{1, "x"}}, 1, 1, "1: x\n\n(End of file - total 1 lines)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, dir := newFS(t, func(o *Options) {
				if tt.readLimit > 0 {
					o.ReadLimit = tt.readLimit
				}
				if tt.maxLine > 0 {
					o.ReadMaxLineLength = tt.maxLine
				}
				if tt.maxBytes > 0 {
					o.ReadMaxBytes = tt.maxBytes
				}
			})
			put(t, filepath.Join(dir, "a.txt"), tt.input)
			result := execute(t, NewReadDefinition(f), tt.args)
			var value ReadResult
			if err := json.Unmarshal(result.Value, &value); err != nil {
				t.Fatal(err)
			}
			want := ReadResult{filepath.Join(dir, "a.txt"), tt.offset, tt.lines, tt.total}
			if !reflect.DeepEqual(value, want) {
				t.Fatalf("value = %+v, want %+v", value, want)
			}
			wantText := "<path>" + filepath.Join(dir, "a.txt") + "</path>\n<type>file</type>\n<content>\n" + tt.body + "\n</content>"
			if got := textOf(t, result); got != wantText {
				t.Fatalf("content = %q, want %q", got, wantText)
			}
		})
	}
}

func TestReadValidation(t *testing.T) {
	f, dir := newFS(t, func(o *Options) { o.ReadLimit = 2 })
	put(t, filepath.Join(dir, "a.txt"), "one\ntwo")
	definition := NewReadDefinition(f)
	for _, args := range []string{
		`{}`, `null`, `[]`, `{"file_path":null}`, `{"file_path":1}`, `{"file_path":"   "}`,
		`{"file_path":"a.txt","offset":0}`, `{"file_path":"a.txt","offset":-1}`, `{"file_path":"a.txt","offset":1.5}`,
		`{"file_path":"a.txt","offset":null}`, `{"file_path":"a.txt","offset":"1"}`, `{"file_path":"a.txt","offset":1e1000}`,
		`{"file_path":"a.txt","offset":9223372036854775808}`, `{"file_path":"a.txt","offset":3}`,
		`{"file_path":"a.txt","limit":0}`, `{"file_path":"a.txt","limit":-3}`, `{"file_path":"a.txt","limit":1.5}`,
		`{"file_path":"a.txt","limit":null}`, `{"file_path":"a.txt","limit":true}`, `{"file_path":"a.txt","limit":3}`,
		`{"file_path":"a.txt","unknown":1}`, `{"file_path":"a.txt"} {}`, `{"file_path":"a.txt","offset":NaN}`,
	} {
		t.Run(args, func(t *testing.T) {
			if _, err := definition.Execute(context.Background(), tools.Execution{Arguments: json.RawMessage(args)}); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	put(t, filepath.Join(dir, "empty.txt"), "")
	if _, _, err := f.Read(context.Background(), "empty.txt", 2, 1); err == nil {
		t.Fatal("empty file offset 2 accepted")
	}
}

type smallChunks struct{ source io.Reader }

func (r smallChunks) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.source.Read(p)
}

func TestScanChunkBoundaries(t *testing.T) {
	for _, input := range []string{"one\r\ntwo\n", "你好\n😀世界", "😀"} {
		got, capped, err := scanWindow(context.Background(), smallChunks{strings.NewReader(input)}, "a.txt", 1, 2000, 2000, 51200)
		if err != nil {
			t.Fatal(err)
		}
		want, wantCapped, err := scanWindow(context.Background(), strings.NewReader(input), "a.txt", 1, 2000, 2000, 51200)
		if err != nil || !reflect.DeepEqual(got, want) || capped != wantCapped {
			t.Fatalf("chunk-dependent result: %+v, %+v, %v", got, want, err)
		}
	}
}

// A synthetic stream exceeds bufio.Scanner's default token cap by orders of
// magnitude. It supplies the bytes incrementally, without allocating the file.
type repeatedByte struct{ remaining int64 }

func (r *repeatedByte) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	for i := range p {
		p[i] = 'z'
	}
	r.remaining -= int64(len(p))
	return len(p), nil
}

func TestScanHugeLineAndAccurateTotal(t *testing.T) {
	reader := io.MultiReader(&repeatedByte{8 * 1024 * 1024}, strings.NewReader("\nend\n"))
	result, capped, err := scanWindow(context.Background(), reader, "big.txt", 1, 1, 5, 100)
	if err != nil || capped || result.TotalLines != 2 || !reflect.DeepEqual(result.Lines, []Line{{1, "zzzzz... (line truncated to 5 chars)"}}) {
		t.Fatalf("%+v capped=%v err=%v", result, capped, err)
	}
	f, dir := newFS(t, nil)
	put(t, filepath.Join(dir, "many.txt"), strings.Repeat("x\n", 10000))
	got, _, err := f.Read(context.Background(), "many.txt", 1, 1)
	if err != nil || len(got.Lines) != 1 || got.TotalLines != 10000 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestReadUTF8AndIOFailures(t *testing.T) {
	for _, input := range [][]byte{
		{0xff}, []byte("ok\n\xff"), []byte("long line " + strings.Repeat("x", 10000) + "\xff"), {0xe4, 0xbd},
	} {
		if _, _, err := scanWindow(context.Background(), bytes.NewReader(input), "a.txt", 1, 1, 2, 1); err == nil {
			t.Fatalf("invalid UTF-8 accepted: %x", input[:min(10, len(input))])
		}
	}
	f, dir := newFS(t, nil)
	put(t, filepath.Join(dir, "invalid.txt"), string([]byte{0xff}))
	if _, _, err := f.Read(context.Background(), "invalid.txt", 1, 1); err == nil {
		t.Fatal("invalid file accepted")
	}
	put(t, filepath.Join(dir, "valid.txt"), "�")
	if _, _, err := f.Read(context.Background(), "valid.txt", 1, 1); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("read failure")
	if _, _, err := scanWindow(context.Background(), brokenReader{boom}, "a.txt", 1, 1, 2, 100); !errors.Is(err, boom) {
		t.Fatalf("error=%v", err)
	}
}

type brokenReader struct{ err error }

func (r brokenReader) Read([]byte) (int, error) { return 0, r.err }

func TestDefinitionSchemas(t *testing.T) {
	f, _ := newFS(t, nil)
	for _, d := range []tools.Definition{NewReadDefinition(f), NewWriteDefinition(f)} {
		if d.FailureMode != tools.ToolFailureResult {
			t.Fatal("wrong failure mode")
		}
		for _, raw := range []tools.JSONSchema{d.Parameters, d.Output} {
			s := compileSchema(t, raw)
			if err := s.Validate(map[string]any{}); err == nil {
				t.Fatal("required fields missing")
			}
		}
		args := map[string]any{"file_path": "a.txt"}
		if d.Name == "write_file" {
			args["content"] = ""
		}
		if err := compileSchema(t, d.Parameters).Validate(args); err != nil {
			t.Fatal(err)
		}
		args["sandbox_permissions"] = "workspace-write"
		if err := compileSchema(t, d.Parameters).Validate(args); err == nil {
			t.Fatal("additional parameter accepted")
		}
	}
	d := NewReadDefinition(f)
	for _, raw := range []string{
		`{"path":"a","offset":1,"lines":[],"totalLines":0,"extra":1}`,
		`{"path":"a","offset":1,"lines":[{"number":1,"text":"x","extra":1}],"totalLines":1}`,
		`{"path":"a","offset":1,"lines":[{"number":1}],"totalLines":1}`,
	} {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatal(err)
		}
		if err := compileSchema(t, d.Output).Validate(v); err == nil {
			t.Fatal(fmt.Sprintf("invalid output accepted: %s", raw))
		}
	}
}
