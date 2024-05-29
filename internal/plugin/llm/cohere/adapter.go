package cohere

import (
	"encoding/json"
	"github.com/bincooo/chatgpt-adapter/internal/common"
	"github.com/bincooo/chatgpt-adapter/internal/gin.handler/response"
	"github.com/bincooo/chatgpt-adapter/internal/plugin"
	"github.com/bincooo/chatgpt-adapter/logger"
	"github.com/bincooo/chatgpt-adapter/pkg"
	coh "github.com/bincooo/cohere-api"
	"github.com/gin-gonic/gin"
)

var (
	Adapter = API{}
	Model   = "cohere"
)

type API struct {
	plugin.BaseAdapter
}

func (API) Match(_ *gin.Context, model string) bool {
	switch model {
	case coh.COMMAND,
		coh.COMMAND_R,
		coh.COMMAND_LIGHT,
		coh.COMMAND_LIGHT_NIGHTLY,
		coh.COMMAND_NIGHTLY,
		coh.COMMAND_R_PLUS:
		return true
	default:
		return false
	}
}

func (API) Models() []plugin.Model {
	return []plugin.Model{
		{
			Id:      "command",
			Object:  "model",
			Created: 1686935002,
			By:      "cohere-adapter",
		}, {
			Id:      "command-r",
			Object:  "model",
			Created: 1686935002,
			By:      "cohere-adapter",
		}, {
			Id:      "command-light",
			Object:  "model",
			Created: 1686935002,
			By:      "cohere-adapter",
		}, {
			Id:      "command-light-nightly",
			Object:  "model",
			Created: 1686935002,
			By:      "cohere-adapter",
		}, {
			Id:      "command-nightly",
			Object:  "model",
			Created: 1686935002,
			By:      "cohere-adapter",
		}, {
			Id:      "command-r-plus",
			Object:  "model",
			Created: 1686935002,
			By:      "cohere-adapter",
		},
	}
}

func (API) Completion(ctx *gin.Context) {
	var (
		cookie     = ctx.GetString("token")
		proxies    = ctx.GetString("proxies")
		notebook   = ctx.GetBool("notebook")
		completion = common.GetGinCompletion(ctx)
		matchers   = common.GetGinMatchers(ctx)
	)

	var (
		system    string
		message   string
		pMessages []coh.Message
		chat      coh.Chat
		//toolObject = coh.ToolObject{
		//	Tools:   convertTools(completion),
		//	Results: convertToolResults(completion),
		//}
	)

	// 官方的文档toolCall描述十分模糊，简测功能不佳，改回提示词实现
	if /*notebook &&*/ plugin.NeedToToolCall(ctx) {
		if completeToolCalls(ctx, cookie, proxies, completion) {
			return
		}
	}

	// TODO - 官方Go库出了，后续修改
	if notebook {
		//toolObject = coh.ToolObject{}
		message = mergeMessages(completion.Messages)
		ctx.Set(ginTokens, common.CalcTokens(message))
		chat = coh.New(cookie, completion.Temperature, completion.Model, false)
		chat.Proxies(proxies)
		chat.TopK(completion.TopK)
		chat.MaxTokens(completion.MaxTokens)
		chat.StopSequences([]string{
			"user:",
			"assistant:",
			"system:",
		})
	} else {
		// chat模式已实现toolCall
		var tokens = 0
		pMessages, system, message, tokens = mergeChatMessages(completion.Messages)
		ctx.Set(ginTokens, tokens)
		chat = coh.New(cookie, completion.Temperature, completion.Model, true)
		chat.Proxies(proxies)
	}

	chatResponse, err := chat.Reply(ctx.Request.Context(), pMessages, system, message, coh.ToolObject{})
	if err != nil {
		logger.Error(err)
		response.Error(ctx, -1, err)
		return
	}

	content := waitResponse(ctx, matchers, chatResponse, completion.Stream)
	if content == "" && response.NotResponse(ctx) {
		response.Error(ctx, -1, "EMPTY RESPONSE")
	}
}

func convertToolResults(completion pkg.ChatCompletion) (toolResults []coh.ToolResult) {
	find := func(name string) map[string]interface{} {
		for pos := range completion.Messages {
			message := completion.Messages[pos]
			if !message.Is("role", "assistant") || !message.Has("tool_calls") {
				continue
			}

			toolCalls := message.GetSlice("tool_calls")
			if len(toolCalls) == 0 {
				continue
			}

			var toolCall pkg.Keyv[interface{}] = toolCalls[0].(map[string]interface{})
			if !toolCall.Has("function") {
				continue
			}

			var args interface{}
			fn := toolCall.GetKeyv("function")
			if !fn.Is("name", name) {
				continue
			}

			if err := json.Unmarshal([]byte(fn.GetString("arguments")), &args); err != nil {
				logger.Error(err)
				continue
			}

			return map[string]interface{}{
				"name":       name,
				"parameters": args,
			}
		}
		return nil
	}

	for pos := range completion.Messages {
		message := completion.Messages[pos]
		if message.Is("role", "tool") {
			call := find(message.GetString("name"))
			if call == nil {
				continue
			}

			var output interface{}
			if err := json.Unmarshal([]byte(message.GetString("content")), &output); err != nil {
				logger.Error(err)
				continue
			}

			toolResults = append(toolResults, coh.ToolResult{
				Call: call,
				Outputs: []interface{}{
					output,
				},
			})
		}
	}
	return
}

func convertTools(completion pkg.ChatCompletion) (tools []coh.ToolCall) {
	if len(completion.Tools) == 0 {
		return
	}

	condition := func(str string) string {
		switch str {
		case "string":
			return "str"
		case "boolean":
			return "bool"
		case "number":
			return str
		default:
			return "object"
		}
	}

	contains := func(slice []interface{}, str string) bool {
		for _, v := range slice {
			if v == str {
				return true
			}
		}
		return false
	}

	for pos := range completion.Tools {
		t := completion.Tools[pos]
		if !t.Is("type", "function") {
			continue
		}

		fn := t.GetKeyv("function")
		params := make(map[string]interface{})
		if fn.Has("parameters") {
			keyv := fn.GetKeyv("parameters")
			properties := keyv.GetKeyv("properties")
			required := keyv.GetSlice("required")
			for k, v := range properties {
				value := v.(map[string]interface{})
				value["required"] = contains(required, k)
				value["type"] = condition(value["type"].(string))
				params[k] = value
			}
		}

		tools = append(tools, coh.ToolCall{
			Name:        fn.GetString("name"),
			Description: fn.GetString("description"),
			Param:       params,
		})
	}
	return
}
