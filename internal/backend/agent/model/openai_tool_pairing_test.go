// openai_tool_pairing_test.go 验证发送前的 tool_calls / tool 结果配对修复。
package modeladapter

import (
	"encoding/json"
	"testing"
)

func pairingTestCall(id string) ToolCallDescriptor {
	return ToolCallDescriptor{
		ID:   id,
		Type: "function",
		Function: ToolCallFunctionShape{
			Name:      "Read",
			Arguments: `{"path":"a.go"}`,
		},
	}
}

func pairingTestToolResult(id string) Message {
	return Message{Role: "tool", ToolCallID: id, Name: "Read", Content: "ok"}
}

func pairingToolCallIDs(messages []Message) []string {
	var ids []string
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			ids = append(ids, call.ID)
		}
	}
	return ids
}

func pairingToolResultIDs(messages []Message) []string {
	var ids []string
	for _, message := range messages {
		if message.Role == "tool" {
			ids = append(ids, message.ToolCallID)
		}
	}
	return ids
}

func TestRepairOpenAIToolPairingKeepsValidChain(t *testing.T) {
	input := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: []ToolCallDescriptor{pairingTestCall("call-a"), pairingTestCall("call-b")}},
		pairingTestToolResult("call-a"),
		pairingTestToolResult("call-b"),
	}
	repaired, droppedResults, droppedCalls := repairToolReplayPairingMessages(input)
	if droppedResults != 0 || droppedCalls != 0 {
		t.Fatalf("合法链路不应被修改，got results=%d calls=%d", droppedResults, droppedCalls)
	}
	if len(repaired) != len(input) {
		t.Fatalf("消息数量发生变化: got %d want %d", len(repaired), len(input))
	}
	if ids := pairingToolCallIDs(repaired); len(ids) != 2 || ids[0] != "call-a" || ids[1] != "call-b" {
		t.Fatalf("tool_call id 异常: %v", ids)
	}
}

func TestRepairOpenAIToolPairingDropsDanglingCall(t *testing.T) {
	input := []Message{
		{Role: "assistant", ToolCalls: []ToolCallDescriptor{pairingTestCall("call-a"), pairingTestCall("call-b")}},
		pairingTestToolResult("call-a"),
	}
	repaired, droppedResults, droppedCalls := repairToolReplayPairingMessages(input)
	if droppedResults != 0 || droppedCalls != 1 {
		t.Fatalf("应只删除 1 个悬空 call，got results=%d calls=%d", droppedResults, droppedCalls)
	}
	if ids := pairingToolCallIDs(repaired); len(ids) != 1 || ids[0] != "call-a" {
		t.Fatalf("悬空 call 未删除: %v", ids)
	}
	if ids := pairingToolResultIDs(repaired); len(ids) != 1 || ids[0] != "call-a" {
		t.Fatalf("tool 结果异常: %v", ids)
	}
}

func TestRepairOpenAIToolPairingDropsOrphanToolResult(t *testing.T) {
	input := []Message{
		{Role: "user", Content: "hi"},
		pairingTestToolResult("call-orphan"),
	}
	repaired, droppedResults, droppedCalls := repairToolReplayPairingMessages(input)
	if droppedResults != 1 || droppedCalls != 0 {
		t.Fatalf("应删除 1 条孤儿 tool 结果，got results=%d calls=%d", droppedResults, droppedCalls)
	}
	if len(pairingToolResultIDs(repaired)) != 0 {
		t.Fatalf("孤儿 tool 结果未被删除: %v", repaired)
	}
}

func TestRepairOpenAIToolPairingDropsDuplicateToolResult(t *testing.T) {
	input := []Message{
		{Role: "assistant", ToolCalls: []ToolCallDescriptor{pairingTestCall("call-a")}},
		pairingTestToolResult("call-a"),
		pairingTestToolResult("call-a"),
	}
	repaired, droppedResults, _ := repairToolReplayPairingMessages(input)
	if droppedResults != 1 {
		t.Fatalf("重复结果应删除 1 条，got %d", droppedResults)
	}
	if ids := pairingToolResultIDs(repaired); len(ids) != 1 {
		t.Fatalf("重复 tool 结果未去重: %v", ids)
	}
}

func TestRepairOpenAIToolPairingDropsSeparatedToolResult(t *testing.T) {
	// tool 结果没有紧跟 assistant（中间插入 user 消息）时，OpenAI 同样拒绝，
	// 因此结果与对应 call 都要裁掉。
	input := []Message{
		{Role: "assistant", ToolCalls: []ToolCallDescriptor{pairingTestCall("call-a")}},
		{Role: "user", Content: "继续"},
		pairingTestToolResult("call-a"),
	}
	repaired, droppedResults, droppedCalls := repairToolReplayPairingMessages(input)
	if droppedResults != 1 || droppedCalls != 1 {
		t.Fatalf("错位结果应同时删除调用与结果，got results=%d calls=%d", droppedResults, droppedCalls)
	}
	if len(repaired) != 1 || repaired[0].Role != "user" {
		t.Fatalf("未清空错位链路: %#v", repaired)
	}
}

func TestRepairOpenAIToolPairingDropsEmptyAssistant(t *testing.T) {
	input := []Message{
		{Role: "assistant", ToolCalls: []ToolCallDescriptor{pairingTestCall("call-a")}},
		{Role: "user", Content: "hi"},
	}
	repaired, _, droppedCalls := repairToolReplayPairingMessages(input)
	if droppedCalls != 1 {
		t.Fatalf("应删除 1 个悬空 call，got %d", droppedCalls)
	}
	if len(repaired) != 1 || repaired[0].Role != "user" {
		t.Fatalf("空 assistant 消息应整条删除: %#v", repaired)
	}
}

func TestRepairOpenAIToolPairingKeepsAssistantContent(t *testing.T) {
	input := []Message{
		{Role: "assistant", Content: "我先看看", ToolCalls: []ToolCallDescriptor{pairingTestCall("call-a")}},
		{Role: "user", Content: "hi"},
	}
	repaired, _, _ := repairToolReplayPairingMessages(input)
	if len(repaired) != 2 {
		t.Fatalf("有正文的 assistant 消息应保留: %#v", repaired)
	}
	if len(repaired[0].ToolCalls) != 0 || repaired[0].Content != "我先看看" {
		t.Fatalf("正文保留但 tool_calls 应清空: %#v", repaired[0])
	}
}

func TestRepairOpenAIToolPairingDropsEmptyCallID(t *testing.T) {
	input := []Message{
		{Role: "assistant", ToolCalls: []ToolCallDescriptor{pairingTestCall("")}},
	}
	repaired, _, droppedCalls := repairToolReplayPairingMessages(input)
	if droppedCalls != 1 || len(repaired) != 0 {
		t.Fatalf("空 id 调用应删除且空消息丢弃，got calls=%d messages=%#v", droppedCalls, repaired)
	}
}

func TestBuildOpenAIChatBodyMapRepairsBrokenToolChain(t *testing.T) {
	req := StreamRequest{
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []ToolCallDescriptor{pairingTestCall("call-a")}},
			{Role: "user", Content: "继续"},
			pairingTestToolResult("call-a"),
		},
	}
	body, err := buildOpenAIChatBodyMap(req, "https://api.openai.com/v1", "gpt-4o", 0)
	if err != nil {
		t.Fatalf("buildOpenAIChatBodyMap() error = %v", err)
	}
	messages, _ := body["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if _, ok := message["tool_calls"]; ok {
			t.Fatalf("断链的 tool_calls 未清理: %#v", message)
		}
		if role, _ := message["role"].(string); role == "tool" {
			t.Fatalf("孤儿的 tool 消息未清理: %#v", message)
		}
	}
}

func TestBuildOpenAIResponsesBodyMapRepairsBrokenToolChain(t *testing.T) {
	req := StreamRequest{
		Messages: []Message{
			{Role: "assistant", ToolCalls: []ToolCallDescriptor{pairingTestCall("call-a")}},
			{Role: "user", Content: "继续"},
			pairingTestToolResult("call-a"),
		},
	}
	body, err := buildOpenAIResponsesBodyMap(req, "gpt-5.6", 0)
	if err != nil {
		t.Fatalf("buildOpenAIResponsesBodyMap() error = %v", err)
	}
	input, _ := body["input"].([]any)
	encoded, marshalErr := json.Marshal(input)
	if marshalErr != nil || !json.Valid(encoded) {
		t.Fatalf("input 不是合法 JSON: %v", marshalErr)
	}
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		switch item["type"] {
		case "function_call", "function_call_output":
			t.Fatalf("断链的 function call 未清理: %#v", item)
		}
	}
}
