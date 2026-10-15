package fstool

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

type Line struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}

type ReadResult struct {
	Path       string `json:"path"`
	Offset     int    `json:"offset"`
	Lines      []Line `json:"lines"`
	TotalLines int    `json:"totalLines"`
}

// Read reads a bounded window but scans all input to validate UTF-8 and count
// lines. offset and limit must be positive; limit cannot exceed ReadLimit.
func (f *FS) Read(ctx context.Context, path string, offset, limit int) (ReadResult, bool, error) {
	return f.read(ctx, "", path, offset, limit)
}

func (f *FS) read(ctx context.Context, cwd, path string, offset, limit int) (ReadResult, bool, error) {
	if offset < 1 || limit < 1 || limit > f.options.ReadLimit {
		return ReadResult{}, false, fmt.Errorf("offset and limit must be positive integers; limit must be less than or equal to %d", f.options.ReadLimit)
	}
	done, err := f.begin(ctx)
	if err != nil {
		return ReadResult{}, false, err
	}
	defer done()
	target, err := f.resolve(ctx, cwd, path)
	if err != nil {
		return ReadResult{}, false, err
	}
	parent, err := os.OpenRoot(filepath.Dir(target.key))
	if err != nil {
		return ReadResult{}, false, err
	}
	defer parent.Close()
	file, err := openRegular(parent, filepath.Base(target.key))
	if err != nil {
		return ReadResult{}, false, err
	}
	defer file.Close()
	return scanWindow(ctx, file, target.display, offset, limit, f.options.ReadMaxLineLength, f.options.ReadMaxBytes)
}

func scanWindow(ctx context.Context, source io.Reader, path string, offset, limit, maxLine, maxBytes int) (ReadResult, bool, error) {
	result := ReadResult{Path: path, Offset: offset, Lines: []Line{}}
	reader := bufio.NewReader(source)
	// JavaScript string.length/substring count UTF-16 units. Retain at most
	// maxLine+1 units, including a possible lone surrogate at the boundary.
	// utf16.Decode renders that surrogate as U+FFFD for valid UTF-8 output.
	var line []uint16
	pending, capped, outputBytes := false, false, 0
	appendUnit := func(unit uint16) {
		if len(line) <= maxLine {
			line = append(line, unit)
		}
	}
	flush := func() {
		result.TotalLines++
		if !capped && result.TotalLines >= offset && len(result.Lines) < limit {
			display := line
			if len(display) > 0 && display[len(display)-1] == '\r' {
				display = display[:len(display)-1]
			}
			truncated := len(display) > maxLine
			if truncated {
				display = display[:maxLine]
			}
			text := string(utf16.Decode(display))
			if truncated {
				text += fmt.Sprintf("... (line truncated to %d chars)", maxLine)
			}
			bytes := len(text)
			if len(result.Lines) > 0 {
				bytes++
			}
			if bytes > maxBytes-outputBytes {
				capped = true
			} else {
				outputBytes += bytes
				result.Lines = append(result.Lines, Line{result.TotalLines, text})
			}
		}
		line, pending = line[:0], false
	}
	for {
		if err := ctx.Err(); err != nil {
			return ReadResult{}, false, err
		}
		r, size, err := reader.ReadRune()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ReadResult{}, false, err
		}
		if r == utf8.RuneError && size == 1 {
			return ReadResult{}, false, errors.New("file is not valid UTF-8")
		}
		if r == '\n' {
			flush()
			continue
		}
		pending = true
		if r <= 0xffff {
			appendUnit(uint16(r))
		} else {
			hi, lo := utf16.EncodeRune(r)
			appendUnit(uint16(hi))
			appendUnit(uint16(lo))
		}
	}
	if pending {
		flush()
	}
	if offset > result.TotalLines && !(result.TotalLines == 0 && offset == 1) {
		return ReadResult{}, false, fmt.Errorf("offset %d is out of range for %q (%d lines)", offset, path, result.TotalLines)
	}
	return result, capped, nil
}

func NewReadDefinition(f *FS) tools.Definition {
	return tools.Definition{
		Name: "read_file", Description: "Read a UTF-8 text file and return line-numbered content. Relative paths use the session working directory.",
		Parameters:  tools.JSONSchema(fmt.Sprintf(`{"type":"object","additionalProperties":false,"required":["file_path"],"properties":{"file_path":{"type":"string"},"offset":{"type":"number","description":"1-based first line to return. Defaults to 1."},"limit":{"type":"number","description":"Maximum number of lines to return. Defaults to %d."}}}`, f.options.ReadLimit)),
		Output:      tools.JSONSchema(`{"type":"object","additionalProperties":false,"required":["path","offset","lines","totalLines"],"properties":{"path":{"type":"string"},"offset":{"type":"integer"},"lines":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["number","text"],"properties":{"number":{"type":"integer"},"text":{"type":"string"}}}},"totalLines":{"type":"integer"}}}`),
		FailureMode: tools.ToolFailureResult,
		Execute: func(ctx context.Context, execution tools.Execution) (tools.ExecutionResult, error) {
			args, err := parseObject(execution.Arguments, "file_path", "offset", "limit")
			if err != nil {
				return tools.ExecutionResult{}, err
			}
			path, err := stringArg(args, "file_path")
			if err != nil {
				return tools.ExecutionResult{}, err
			}
			offset, err := integerArg(args, "offset", 1)
			if err != nil {
				return tools.ExecutionResult{}, err
			}
			limit, err := integerArg(args, "limit", f.options.ReadLimit)
			if err != nil {
				return tools.ExecutionResult{}, err
			}
			result, capped, err := f.read(ctx, execution.CWD, path, offset, limit)
			if err != nil {
				return tools.ExecutionResult{}, err
			}
			return executionResult(result, renderRead(result, capped))
		},
	}
}

func executionResult(value any, text string) (tools.ExecutionResult, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	return tools.ExecutionResult{Value: encoded, Content: []protocol.ContentBlock{protocol.TextBlock{Text: text}}}, nil
}

func parseObject(raw json.RawMessage, allowed ...string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("arguments must be valid UTF-8 JSON")
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	if args == nil {
		return nil, errors.New("arguments must be an object")
	}
	for key := range args {
		found := false
		for _, name := range allowed {
			if key == name {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown argument %q", key)
		}
	}
	return args, nil
}

func stringArg(args map[string]json.RawMessage, key string) (string, error) {
	raw, ok := args[key]
	if !ok || len(raw) == 0 || raw[0] != '"' {
		return "", fmt.Errorf("%s must be a string", key)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", err
	}
	return text, nil
}

func integerArg(args map[string]json.RawMessage, key string, fallback int) (int, error) {
	raw, ok := args[key]
	if !ok {
		return fallback, nil
	}
	var number json.Number
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, err
	}
	n, err := strconv.ParseFloat(string(number), 64)
	if err != nil || math.IsInf(n, 0) || n < 1 || math.Trunc(n) != n || n >= float64(int(^uint(0)>>1)) {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return int(n), nil
}
