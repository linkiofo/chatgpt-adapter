package claude

import (
	"bytes"
	"fmt"
	"github.com/bincooo/chatgpt-adapter/internal/common"
	"github.com/bincooo/chatgpt-adapter/internal/gin.handler/response"
	"github.com/bincooo/chatgpt-adapter/internal/vars"
	"github.com/bincooo/chatgpt-adapter/logger"
	"github.com/bincooo/chatgpt-adapter/pkg"
	"github.com/bincooo/claude-api/types"
	"github.com/gin-gonic/gin"
	"strings"
	"time"
)

const ginTokens = "__tokens__"

func waitMessage(chatResponse chan types.PartialResponse, cancel func(str string) bool) (content string, err error) {

	for {
		message, ok := <-chatResponse
		if !ok {
			break
		}

		if message.Error != nil {
			return "", message.Error
		}

		if len(message.Text) > 0 {
			content += message.Text
			if cancel != nil && cancel(content) {
				return content, nil
			}
		}
	}

	return content, nil
}

func waitResponse(ctx *gin.Context, matchers []common.Matcher, chatResponse chan types.PartialResponse, sse bool) (content string) {
	var (
		created = time.Now().Unix()
		tokens  = ctx.GetInt(ginTokens)
	)
	logger.Infof("waitResponse ...")

	for {
		message, ok := <-chatResponse
		if !ok {
			break
		}

		if message.Error != nil {
			logger.Error(message.Error)
			if response.NotSSEHeader(ctx) {
				logger.Error(message.Error)
				response.Error(ctx, -1, message.Error)
			}
			return
		}

		logger.Debug("----- raw -----")
		logger.Debug(message.Text)

		raw := common.ExecMatchers(matchers, message.Text)
		if len(raw) == 0 {
			continue
		}

		if sse {
			response.SSEResponse(ctx, Model, raw, created)
		}
		content += raw
	}

	if content == "" && response.NotSSEHeader(ctx) {
		return
	}

	ctx.Set(vars.GinCompletionUsage, common.CalcUsageTokens(content, tokens))
	if !sse {
		response.Response(ctx, Model, content)
	} else {
		response.SSEResponse(ctx, Model, "[DONE]", created)
	}
	return
}

func mergeMessages(messages []pkg.Keyv[interface{}]) (attachment []types.Attachment, tokens int) {
	condition := func(expr string) string {
		switch expr {
		case "system", "assistant", "function", "tool", "end":
			return expr
		default:
			return "human"
		}
	}

	// 合并历史对话
	iterator := func(opts struct {
		Previous string
		Next     string
		Message  map[string]string
		Buffer   *bytes.Buffer
		Initial  func() pkg.Keyv[interface{}]
	}) (messages []string, _ error) {
		role := opts.Message["role"]
		tokens += common.CalcTokens(opts.Message["content"])
		if condition(role) == condition(opts.Next) {
			// cache buffer
			if role == "function" || role == "tool" {
				opts.Buffer.WriteString(fmt.Sprintf("这是系统内置tools工具的返回结果: (%s)\n\n##\n%s\n##", opts.Message["name"], opts.Message["content"]))
				return
			}
			opts.Buffer.WriteString(opts.Message["content"])
			return
		}

		defer opts.Buffer.Reset()
		opts.Buffer.WriteString(fmt.Sprintf(opts.Message["content"]))
		messages = []string{
			fmt.Sprintf("%s： %s", condition(role), opts.Buffer.String()),
		}
		return
	}

	nMessages, _ := common.TextMessageCombiner(messages, iterator)
	join := strings.Join(nMessages, "\n\n")
	join = common.PadJunkMessage(padMaxCount-len(join), join)

	tokens = common.CalcTokens(join)
	attachment = append(attachment, types.Attachment{
		Content:  join,
		FileName: "paste.txt",
		FileSize: len(join),
		FileType: "text/plain",
	})

	return
}
