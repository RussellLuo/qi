package fstool

import (
	"fmt"
	"strings"
)

func envelope(path, body string) string {
	return fmt.Sprintf("<path>%s</path>\n<type>file</type>\n<content>\n%s\n</content>", path, body)
}

// Formats mirror DSH tool-fs read-render.ts and write.ts, using the backend's
// absolute display path.
func renderRead(result ReadResult, capped bool) string {
	end := result.Offset - 1
	if len(result.Lines) > 0 {
		end = result.Lines[len(result.Lines)-1].Number
	}
	var footer string
	switch {
	case capped:
		footer = fmt.Sprintf("(Output capped. Showing lines %d-%d. Use offset=%d to continue.)", result.Offset, end, end+1)
	case end < result.TotalLines:
		footer = fmt.Sprintf("(Showing lines %d-%d of %d. Use offset=%d to continue.)", result.Offset, end, result.TotalLines, end+1)
	default:
		footer = fmt.Sprintf("(End of file - total %d lines)", result.TotalLines)
	}
	var body strings.Builder
	for _, line := range result.Lines {
		fmt.Fprintf(&body, "%d: %s\n", line.Number, line.Text)
	}
	if len(result.Lines) > 0 {
		body.WriteByte('\n')
	}
	body.WriteString(footer)
	return envelope(result.Path, body.String())
}

func renderWrite(result WriteResult) string {
	verb := "Created"
	if result.Operation == "update" {
		verb = "Updated"
	}
	return envelope(result.Path, verb+" file")
}
