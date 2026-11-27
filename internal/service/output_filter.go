package service

import (
	"context"
	"encoding/json"
	"strings"

	"ragflow/internal/agent/canvas"
	"ragflow/internal/common"
	"ragflow/internal/compliance"
	"ragflow/internal/entity"
	"ragflow/internal/permission"

	"go.uber.org/zap"
)

func (s *ChatPipelineService) filterChatOutput(ctx context.Context, chat *entity.Chat, stream bool, input <-chan AsyncChatResult, filter *compliance.TextFilter) <-chan AsyncChatResult {
	if !filter.Enabled() {
		return input
	}
	out := make(chan AsyncChatResult, 16)
	go func() {
		defer close(out)
		answer, reasoning := filter.Stream(), filter.Stream()
		send := func(result AsyncChatResult) bool {
			if ctx.Err() != nil {
				return false
			}
			select {
			case out <- result:
				return true
			case <-ctx.Done():
				return false
			}
		}
		flush := func() bool {
			result := AsyncChatResult{Answer: answer.Flush(), Reasoning: reasoning.Flush()}
			return (result.Answer == "" && result.Reasoning == "") || send(result)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case result, ok := <-input:
				if !ok {
					flush()
					return
				}
				if stream && (result.Final || result.StartToThink || result.EndToThink) && !flush() {
					return
				}
				if result.Final || !stream {
					// Consumers use this marker to classify failures and decide
					// whether to persist the turn. Keep it outside text filtering.
					parts := strings.Split(result.Answer, "**ERROR**")
					for i := range parts {
						parts[i] = filter.Filter(parts[i])
					}
					result.Answer = strings.Join(parts, "**ERROR**")
					result.Reasoning = filter.Filter(result.Reasoning)
				} else {
					result.Answer = answer.Write(result.Answer)
					result.Reasoning = reasoning.Write(result.Reasoning)
				}
				if result.Reference != nil {
					result.Reference = filter.FilterFields(result.Reference).(map[string]any)
				}
				result.Prompt = filter.Filter(result.Prompt)
				if result.ThinkEvent != nil {
					step := *result.ThinkEvent
					step.Args = filter.Filter(step.Args)
					step.Cause = filter.Filter(step.Cause)
					step.Summary = filter.Filter(step.Summary)
					result.ThinkEvent = &step
				}
				if result.Final {
					if enabled, _ := chat.PromptConfig["tts"].(bool); enabled && result.Answer != "" {
						modelCtx := permission.WithTenantID(ctx, chat.TenantID)
						tts, err := s.ModelFactory.NewDefaultTTSModel(modelCtx)
						if err != nil {
							common.Warn("Filtered answer TTS model is unavailable", zap.Error(err))
						} else {
							result.AudioBinary = s.synthesizeTTS(compliance.WithTextFilter(modelCtx, &compliance.TextFilter{}), tts, ExtractVisibleAnswer(result.Answer))
						}
					}
				}
				if !send(result) {
					return
				}
			}
		}
	}()
	return out
}

func newAgentOutputFilter(filter *compliance.TextFilter) func(canvas.RunEvent) []canvas.RunEvent {
	text := filter.Stream()
	return func(event canvas.RunEvent) []canvas.RunEvent {
		var payload map[string]any
		if err := json.Unmarshal([]byte(event.Data), &payload); err != nil {
			common.Warn("Agent output event could not be filtered", zap.String("event", event.Type))
			event.Type = "error"
			event.Data = `{"message":"Output filtering failed"}`
			return []canvas.RunEvent{event}
		}
		var events []canvas.RunEvent
		flush := func() {
			if tail := text.Flush(); tail != "" {
				data, _ := json.Marshal(canvas.MessageEvent{Content: tail})
				message := event
				message.Type, message.Data = "message", string(data)
				events = append(events, message)
			}
		}
		if event.Type == "message" {
			if payload["start_to_think"] == true || payload["end_to_think"] == true {
				flush()
			}
			content, _ := payload["content"].(string)
			payload["content"] = text.Write(content)
			// Keep content out of whole-payload filtering: it has already
			// passed through the stream matcher.
			content = payload["content"].(string)
			delete(payload, "content")
			payload = filter.FilterFields(payload).(map[string]any)
			payload["content"] = content
		} else {
			if event.Type == "message_end" || event.Type == "workflow_finished" || event.Type == "error" {
				flush()
			}
			inputs, hasInputs := payload["inputs"]
			outputs, hasOutputs := payload["outputs"]
			delete(payload, "inputs")
			delete(payload, "outputs")
			payload = filter.FilterFields(payload).(map[string]any)
			if hasInputs {
				payload["inputs"] = filter.FilterValues(inputs)
			}
			if hasOutputs {
				payload["outputs"] = filter.FilterValues(outputs)
			}
		}
		data, err := json.Marshal(payload)
		if err != nil {
			common.Warn("Agent filtered output could not be encoded", zap.String("event", event.Type))
			event.Type = "error"
			event.Data = `{"message":"Output filtering failed"}`
		} else {
			event.Data = string(data)
		}
		return append(events, event)
	}
}

func filterAskOutput(ctx context.Context, input <-chan AskDelta, filter *compliance.TextFilter) <-chan AskDelta {
	if !filter.Enabled() {
		return input
	}
	out := make(chan AskDelta, 32)
	go func() {
		defer close(out)
		text := filter.Stream()
		send := func(delta AskDelta) bool {
			if ctx.Err() != nil {
				return false
			}
			select {
			case out <- delta:
				return true
			case <-ctx.Done():
				return false
			}
		}
		flush := func() bool {
			tail := text.Flush()
			return tail == "" || send(AskDelta{Kind: AskDeltaAnswer, Value: tail})
		}
		for {
			select {
			case <-ctx.Done():
				return
			case delta, ok := <-input:
				if !ok {
					flush()
					return
				}
				if delta.Kind != AskDeltaAnswer && !flush() {
					return
				}
				if delta.Kind == AskDeltaAnswer {
					delta.Value = text.Write(delta.Value)
				} else if delta.Kind != AskDeltaMarker {
					delta.Value = filter.Filter(delta.Value)
				}
				delta.Message = filter.Filter(delta.Message)
				delta.Refs = filter.FilterFields(delta.Refs)
				if !send(delta) {
					return
				}
			}
		}
	}()
	return out
}
