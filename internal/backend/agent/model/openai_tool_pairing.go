// openai_tool_pairing.go 在请求发送前修复 assistant tool_calls 与 tool 结果的配对关系。
package modeladapter

import (
	"fmt"
	"strings"
)

// repairToolReplayPairingMessages 把历史消息重建成 provider 可接受的 tool 配对形态。
//
// OpenAI 及兼容网关对 tool 配对极其严格：只要有一个 assistant tool_call 后面
// 没有紧随的 tool 结果，或者存在一个找不到对应调用的 tool 消息，整个请求都会
// 以 400 "tool calls and tool results do not match" 拒绝；而且这类错误不可重试，
// 用户只能新建会话。历史里出现断链的常见来源：
//   - 交互工具（AskQuestion 等）被取消后结果未写入；
//   - orphan cancel / 中断收口只推进到一半；
//   - 上下文压缩、导入会话或旧版本数据裁剪掉了链路的一端；
//   - 同一批 tool 结果被其它角色消息隔开。
//
// 这里按 OpenAI 的顺序语义重建一次：tool 结果必须紧跟带 tool_calls 的 assistant
// 消息，且 call_id 一一对应。无法配对的一端直接裁掉，保证请求仍然合法，
// 而不是让整段会话永久卡死。返回被裁掉的 tool 结果与 tool_call 数量用于诊断。
func repairToolReplayPairingMessages(messages []Message) ([]Message, int, int) {
	if len(messages) == 0 {
		return nil, 0, 0
	}
	out := make([]Message, 0, len(messages))
	keep := make([]bool, 0, len(messages))

	// batchIndex 指向 out 中当前等待 tool 结果的 assistant 消息；-1 表示
	// 当前不在接收 tool 结果的窗口内。
	batchIndex := -1
	// batchCallIDs 是该 assistant 消息中合法的 call id 集合（非空且批内唯一）。
	batchCallIDs := map[string]struct{}{}
	// batchMatched 记录已被 tool 结果认领的 call id。
	batchMatched := map[string]bool{}
	droppedResults := 0
	droppedCalls := 0

	finishBatch := func() {
		if batchIndex < 0 {
			return
		}
		message := &out[batchIndex]
		if len(message.ToolCalls) > 0 {
			kept := make([]ToolCallDescriptor, 0, len(message.ToolCalls))
			seen := make(map[string]struct{}, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				id := strings.TrimSpace(call.ID)
				if id == "" {
					droppedCalls++
					continue
				}
				if _, duplicate := seen[id]; duplicate {
					droppedCalls++
					continue
				}
				seen[id] = struct{}{}
				if !batchMatched[id] {
					droppedCalls++
					continue
				}
				// 重新编号，保持请求字节确定（provider prefix cache 友好）。
				call.Index = len(kept)
				kept = append(kept, call)
			}
			message.ToolCalls = kept
			// 所有调用都被裁掉时，若消息本身没有可回放内容就整条丢弃。
			if len(kept) == 0 && !messageHasReplayableContent(*message) {
				keep[batchIndex] = false
			}
		}
		batchIndex = -1
		batchCallIDs = map[string]struct{}{}
		batchMatched = map[string]bool{}
	}

	for _, raw := range messages {
		message := raw
		switch strings.TrimSpace(message.Role) {
		case "assistant":
			finishBatch()
			out = append(out, message)
			keep = append(keep, true)
			if len(message.ToolCalls) == 0 {
				continue
			}
			batchIndex = len(out) - 1
			for _, call := range message.ToolCalls {
				id := strings.TrimSpace(call.ID)
				if id == "" {
					continue
				}
				if _, exists := batchCallIDs[id]; exists {
					continue
				}
				batchCallIDs[id] = struct{}{}
			}
		case "tool":
			id := strings.TrimSpace(message.ToolCallID)
			if batchIndex >= 0 && id != "" {
				if _, ok := batchCallIDs[id]; ok && !batchMatched[id] {
					batchMatched[id] = true
					out = append(out, message)
					keep = append(keep, true)
					continue
				}
			}
			// 孤儿 tool 结果、重复结果或位置不合法（未紧跟 assistant）的结果。
			droppedResults++
		default:
			finishBatch()
			out = append(out, message)
			keep = append(keep, true)
		}
	}
	finishBatch()

	filtered := make([]Message, 0, len(out))
	for index, message := range out {
		if keep[index] {
			filtered = append(filtered, message)
		}
	}
	return filtered, droppedResults, droppedCalls
}

// messageHasReplayableContent 判断一条 assistant 消息在移除 tool_calls 后
// 是否仍有值得发送的内容（正文、多模态片段、推理回放或名字）。
func messageHasReplayableContent(message Message) bool {
	if strings.TrimSpace(message.Content) != "" || len(message.ContentParts) > 0 {
		return true
	}
	if strings.TrimSpace(message.ReasoningContent) != "" ||
		strings.TrimSpace(message.ReasoningSignature) != "" ||
		strings.TrimSpace(message.ReasoningSignatureSource) != "" ||
		strings.TrimSpace(message.OpenAIResponsesReasoningID) != "" ||
		strings.TrimSpace(message.OpenAIResponsesReasoningStatus) != "" ||
		len(message.OpenAIResponsesReasoningSummary) > 0 {
		return true
	}
	return strings.TrimSpace(message.Name) != ""
}

// applyToolReplayPairingRepair 执行配对修复并把结果写入请求诊断 knob。
// 返回修复后的消息列表，调用方应使用该列表构造请求体。
func applyToolReplayPairingRepair(req StreamRequest, messages []Message) []Message {
	repaired, droppedResults, droppedCalls := repairToolReplayPairingMessages(messages)
	if droppedResults > 0 || droppedCalls > 0 {
		setRequestKnob(req, "tool_pairing_repair",
			fmt.Sprintf("dropped_tool_results=%d dropped_tool_calls=%d", droppedResults, droppedCalls))
	}
	return repaired
}
