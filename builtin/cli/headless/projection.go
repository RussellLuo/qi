package headless

import (
	"fmt"
	"io"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// projector is confined to the durable reader worker. No live assistant delta
// or execution-local tool value enters this application projection.
type projector struct {
	sink     io.Writer
	usage    jsonObject
	complete bool
}

func newProjector(sink io.Writer) *projector { return &projector{sink: sink, complete: true} }
func (p *projector) write(event jsonObject) error {
	return write(p.sink, boundJSONLine(event, maxStringBytes, maxEventBytes)+"\n")
}
func writeJSONError(sink io.Writer, err error) error {
	return newProjector(sink).write(object("type", "error", "message", err.Error()))
}
func (p *projector) session(id protocol.SessionID, cwd string) error {
	return p.write(object("type", "session", "sessionId", string(id), "cwd", cwd))
}
func (p *projector) final(text string) error {
	return write(p.sink, stringify(object("type", "final", "text", text))+"\n")
}
func stringValue(v any) string {
	if s, ok := v.(jsString); ok {
		return s.text()
	}
	return ""
}
func asObject(v any) jsonObject { o, _ := v.(jsonObject); return o }
func streamUsage(v any) jsonObject {
	records, _ := v.([]any)
	for i := len(records) - 1; i >= 0; i-- {
		record := asObject(records[i])
		if stringValue(record.get("type")) != "chunk" {
			continue
		}
		chunk := asObject(record.get("chunk"))
		if stringValue(chunk.get("type")) == "usage" {
			return asObject(chunk.get("usage"))
		}
	}
	return nil
}
func (p *projector) addUsage(next jsonObject) {
	if next == nil {
		p.complete = false
		return
	}
	if p.usage == nil {
		p.usage = next
		return
	}
	total := jsonObject{}
	for _, key := range []string{"inputTokens", "outputTokens", "totalTokens", "cacheReadTokens", "cacheWriteTokens", "reasoningTokens"} {
		a, okA := p.usage.get(key).(float64)
		b, okB := next.get(key).(float64)
		if okA && okB {
			total.set(jsText(key), a+b)
		}
	}
	p.usage = total
}
func (p *projector) push(event protocol.SessionEvent) error {
	switch event.Type {
	case protocol.TurnStarted, protocol.StepStarted, protocol.AssistantAttempt, protocol.AssistantMessage, protocol.StepEnded, protocol.TurnEnded, protocol.ToolCalled, protocol.ToolResulted:
	default:
		return nil
	}
	value, ok := parseJSON(string(event.Data))
	if !ok {
		return fmt.Errorf("invalid projection data at seq %d", event.Seq)
	}
	data := asObject(value)
	turn, step := data.get("turn"), data.get("step")
	switch event.Type {
	case protocol.TurnStarted:
		return p.write(object("type", "status", "phase", "turn_start", "turn", turn))
	case protocol.StepStarted:
		return p.write(object("type", "status", "phase", "step_start", "turn", turn, "step", step))
	case protocol.AssistantAttempt:
		p.addUsage(streamUsage(data.get("stream")))
	case protocol.AssistantMessage:
		usage := asObject(data.get("usage"))
		if usage == nil {
			usage = streamUsage(data.get("stream"))
		}
		p.addUsage(usage)
		blocks, _ := asObject(data.get("message")).get("content").([]any)
		for _, v := range blocks {
			b := asObject(v)
			kind := stringValue(b.get("type"))
			if kind == "reasoning" || kind == "text" {
				output := kind
				if output == "reasoning" {
					output = "thinking"
				}
				if err := p.write(object("type", output, "text", b.get("text"))); err != nil {
					return err
				}
			}
		}
	case protocol.StepEnded:
		output := object("type", "status", "phase", "step_end", "turn", turn, "step", step)
		if p.complete && p.usage != nil {
			output.set(jsText("usage"), p.usage)
		}
		p.usage = nil
		p.complete = true
		return p.write(output)
	case protocol.TurnEnded:
		return p.write(object("type", "status", "phase", "turn_end", "turn", turn, "reason", data.get("reason")))
	case protocol.ToolCalled:
		return p.write(object("type", "tool_call", "callId", data.get("callId"), "tool", data.get("name"), "input", parseArguments(stringValue(data.get("arguments")))))
	case protocol.ToolResulted:
		if event.SurfaceOp == nil || event.SurfaceOp.Op != "append" {
			return nil
		}
		message := asObject(data.get("message"))
		blocks, _ := message.get("content").([]any)
		result := jsString{}
		for _, v := range blocks {
			b := asObject(v)
			if stringValue(b.get("type")) == "text" {
				if text, ok := b.get("text").(jsString); ok {
					result = append(result, text...)
				}
			}
		}
		status := "completed"
		if message.get("isError") == true {
			status = "error"
		}
		return p.write(object("type", "tool_result", "callId", message.get("toolCallId"), "status", status, "result", result))
	}
	return nil
}
