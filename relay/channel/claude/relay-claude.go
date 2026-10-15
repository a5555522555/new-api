package claude

import (
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"

	"github.com/gin-gonic/gin"
)

func stopReasonClaude2OpenAI(reason string) string {
	return relayconvert.StopReasonClaudeToOpenAI(reason)
}

func maybeMarkClaudeRefusal(c *gin.Context, stopReason string) {
	if c == nil {
		return
	}
	if strings.EqualFold(stopReason, "refusal") {
		common.SetContextKey(c, constant.ContextKeyAdminRejectReason, "claude_stop_reason=refusal")
	}
}

func StreamResponseClaude2OpenAI(claudeResponse *dto.ClaudeResponse) *dto.ChatCompletionsStreamResponse {
	return relayconvert.StreamResponseClaude2OpenAI(claudeResponse)
}

func ResponseClaude2OpenAI(claudeResponse *dto.ClaudeResponse) *dto.OpenAITextResponse {
	return relayconvert.ResponseClaude2OpenAI(claudeResponse)
}

type ClaudeResponseInfo = relayconvert.ClaudeResponseInfo

type NovaStreamResponseInfo struct {
	ResponseId       string
	Created          int64
	Model            string
	ResponseText     strings.Builder
	Usage            *dto.Usage
	StopReason       string
	FinishReasonSent bool
}

func cacheCreationTokensForOpenAIUsage(usage *dto.Usage) int {
	if usage == nil {
		return 0
	}
	openAIUsage := relayconvert.UsageFromClaudeUsage(usage)
	if openAIUsage == nil {
		return 0
	}
	return openAIUsage.PromptTokens - usage.PromptTokens - usage.PromptTokensDetails.CachedTokens
}

func buildOpenAIStyleUsageFromClaudeUsage(usage *dto.Usage) dto.Usage {
	mapped := relayconvert.UsageFromClaudeUsage(usage)
	if mapped == nil {
		return dto.Usage{}
	}
	return *mapped
}

func buildMessageDeltaPatchUsage(claudeResponse *dto.ClaudeResponse, claudeInfo *ClaudeResponseInfo) *dto.ClaudeUsage {
	return relayconvert.BuildMessageDeltaPatchUsage(claudeResponse, claudeInfo)
}

func shouldSkipClaudeMessageDeltaUsagePatch(info *relaycommon.RelayInfo) bool {
	if model_setting.GetGlobalSettings().PassThroughRequestEnabled {
		return true
	}
	if info == nil {
		return false
	}
	return info.ChannelSetting.PassThroughBodyEnabled
}

func patchClaudeMessageDeltaUsageData(data string, usage *dto.ClaudeUsage) string {
	return relayconvert.PatchClaudeMessageDeltaUsageData(data, usage)
}

func FormatClaudeResponseInfo(claudeResponse *dto.ClaudeResponse, oaiResponse *dto.ChatCompletionsStreamResponse, claudeInfo *ClaudeResponseInfo) bool {
	return relayconvert.FormatClaudeResponseInfo(claudeResponse, oaiResponse, claudeInfo)
}

func HandleStreamResponseData(c *gin.Context, info *relaycommon.RelayInfo, claudeInfo *ClaudeResponseInfo, data string) *types.NewAPIError {
	var claudeResponse dto.ClaudeResponse
	err := common.UnmarshalJsonStr(data, &claudeResponse)
	if err != nil {
		common.SysLog("error unmarshalling stream response: " + err.Error())
		return types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if claudeError := claudeResponse.GetClaudeError(); claudeError != nil && claudeError.Type != "" {
		return types.WithClaudeError(*claudeError, http.StatusInternalServerError)
	}
	if claudeResponse.StopReason != "" {
		maybeMarkClaudeRefusal(c, claudeResponse.StopReason)
	}
	if claudeResponse.Delta != nil && claudeResponse.Delta.StopReason != nil {
		maybeMarkClaudeRefusal(c, *claudeResponse.Delta.StopReason)
	}
	if info.RelayFormat == types.RelayFormatClaude {
		FormatClaudeResponseInfo(&claudeResponse, nil, claudeInfo)

		if claudeResponse.Type == "message_start" {
			// message_start, 获取usage
			if claudeResponse.Message != nil {
				info.UpstreamModelName = claudeResponse.Message.Model
			}
		} else if claudeResponse.Type == "message_delta" {
			// 确保 message_delta 的 usage 包含完整的 input_tokens 和 cache 相关字段
			// 解决 AWS Bedrock 等上游返回的 message_delta 缺少这些字段的问题
			if !shouldSkipClaudeMessageDeltaUsagePatch(info) {
				data = patchClaudeMessageDeltaUsageData(data, buildMessageDeltaPatchUsage(&claudeResponse, claudeInfo))
			}
		}
		countClaudeStreamBillableTools(c, info, &claudeResponse)
		helper.ClaudeChunkData(c, claudeResponse, data)
	} else if info.RelayFormat == types.RelayFormatOpenAI {
		response := StreamResponseClaude2OpenAI(&claudeResponse)

		if !FormatClaudeResponseInfo(&claudeResponse, response, claudeInfo) {
			return nil
		}

		countClaudeStreamBillableTools(c, info, &claudeResponse)

		err = helper.ObjectData(c, response)
		if err != nil {
			logger.LogError(c, "send_stream_response_failed: "+err.Error())
		}
	}
	return nil
}

func countClaudeStreamBillableTools(c *gin.Context, info *relaycommon.RelayInfo, claudeResponse *dto.ClaudeResponse) {
	if claudeResponse == nil {
		return
	}
	if claudeResponse.Type == "content_block_start" &&
		claudeResponse.ContentBlock != nil &&
		claudeResponse.ContentBlock.Type == "tool_use" {
		info.CountBillableToolCall(dto.BuildInCallToolUse, claudeResponse.ContentBlock.Name)
	}
	if claudeResponse.Type == "message_delta" &&
		claudeResponse.Usage != nil &&
		claudeResponse.Usage.ServerToolUse != nil &&
		claudeResponse.Usage.ServerToolUse.WebSearchRequests > 0 {
		c.Set("claude_web_search_requests", claudeResponse.Usage.ServerToolUse.WebSearchRequests)
	}
}

func HandleStreamFinalResponse(c *gin.Context, info *relaycommon.RelayInfo, claudeInfo *ClaudeResponseInfo) {
	if claudeInfo.Usage.PromptTokens == 0 {
		//上游出错
	}
	if claudeInfo.Usage.CompletionTokens == 0 || !claudeInfo.Done {
		if common.DebugEnabled {
			common.SysLog("claude response usage is not complete, maybe upstream error")
		}
		// 只补缺失字段，不整份覆盖——保留 message_start 已拿到的 cache 字段
		fallback := service.ResponseText2Usage(c, claudeInfo.ResponseText.String(), info.UpstreamModelName, info.GetEstimatePromptTokens())
		if claudeInfo.Usage.CompletionTokens == 0 ||
			(!claudeInfo.Done && fallback.CompletionTokens > claudeInfo.Usage.CompletionTokens) {
			claudeInfo.Usage.CompletionTokens = fallback.CompletionTokens
		}
		if claudeInfo.Usage.PromptTokens == 0 {
			claudeInfo.Usage.PromptTokens = fallback.PromptTokens
		}
		claudeInfo.Usage.TotalTokens = claudeInfo.Usage.PromptTokens + claudeInfo.Usage.CompletionTokens
	}
	if claudeInfo.Usage != nil {
		claudeInfo.Usage.UsageSemantic = "anthropic"
	}
	if claudeInfo.Usage != nil && claudeInfo.Usage.BillingUsage == nil {
		claudeInfo.Usage.BillingUsage = dto.NewClaudeMessagesBillingUsage(buildMessageDeltaPatchUsage(nil, claudeInfo))
	}

	if info.RelayFormat == types.RelayFormatClaude {
		//
	} else if info.RelayFormat == types.RelayFormatOpenAI {
		if info.ShouldIncludeUsage {
			openAIUsage := buildOpenAIStyleUsageFromClaudeUsage(claudeInfo.Usage)
			response := helper.GenerateFinalUsageResponse(claudeInfo.ResponseId, claudeInfo.Created, info.UpstreamModelName, openAIUsage)
			err := helper.ObjectData(c, response)
			if err != nil {
				common.SysLog("send final response failed: " + err.Error())
			}
		}
		helper.Done(c)
	}
}

func ClaudeStreamHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	claudeInfo := &ClaudeResponseInfo{
		ResponseId:   helper.GetResponseID(c),
		Created:      common.GetTimestamp(),
		Model:        info.UpstreamModelName,
		ResponseText: strings.Builder{},
		Usage:        &dto.Usage{},
	}
	var err *types.NewAPIError
	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		err = HandleStreamResponseData(c, info, claudeInfo, data)
		if err != nil {
			sr.Stop(err)
		}
	})
	if err != nil {
		return nil, err
	}

	HandleStreamFinalResponse(c, info, claudeInfo)
	return claudeInfo.Usage, nil
}

func HandleClaudeResponseData(c *gin.Context, info *relaycommon.RelayInfo, claudeInfo *ClaudeResponseInfo, httpResp *http.Response, data []byte) *types.NewAPIError {
	var claudeResponse dto.ClaudeResponse
	err := common.Unmarshal(data, &claudeResponse)
	if err != nil {
		return types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if claudeError := claudeResponse.GetClaudeError(); claudeError != nil && claudeError.Type != "" {
		return types.WithClaudeError(*claudeError, http.StatusInternalServerError)
	}
	maybeMarkClaudeRefusal(c, claudeResponse.StopReason)
	if claudeInfo.Usage == nil {
		claudeInfo.Usage = &dto.Usage{}
	}
	if claudeResponse.Usage != nil {
		claudeInfo.Usage.PromptTokens = claudeResponse.Usage.InputTokens
		claudeInfo.Usage.CompletionTokens = claudeResponse.Usage.OutputTokens
		claudeInfo.Usage.TotalTokens = claudeResponse.Usage.InputTokens + claudeResponse.Usage.OutputTokens
		claudeInfo.Usage.UsageSemantic = "anthropic"
		claudeInfo.Usage.BillingUsage = dto.NewClaudeMessagesBillingUsage(claudeResponse.Usage)
		claudeInfo.Usage.PromptTokensDetails.CachedTokens = claudeResponse.Usage.CacheReadInputTokens
		claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens = claudeResponse.Usage.CacheCreationInputTokens
		claudeInfo.Usage.ClaudeCacheCreation5mTokens = claudeResponse.Usage.GetCacheCreation5mTokens()
		claudeInfo.Usage.ClaudeCacheCreation1hTokens = claudeResponse.Usage.GetCacheCreation1hTokens()
	}
	var responseData []byte
	switch info.RelayFormat {
	case types.RelayFormatOpenAI:
		openaiResponse := ResponseClaude2OpenAI(&claudeResponse)
		openaiResponse.Usage = buildOpenAIStyleUsageFromClaudeUsage(claudeInfo.Usage)
		responseData, err = common.Marshal(openaiResponse)
		if err != nil {
			return types.NewError(err, types.ErrorCodeBadResponseBody)
		}
	case types.RelayFormatClaude:
		responseData = data
	}

	if claudeResponse.Usage != nil && claudeResponse.Usage.ServerToolUse != nil && claudeResponse.Usage.ServerToolUse.WebSearchRequests > 0 {
		c.Set("claude_web_search_requests", claudeResponse.Usage.ServerToolUse.WebSearchRequests)
	}

	for _, block := range claudeResponse.Content {
		if block.Type == "tool_use" {
			info.CountBillableToolCall(dto.BuildInCallToolUse, block.Name)
		}
	}

	service.IOCopyBytesGracefully(c, httpResp, responseData)
	return nil
}

func ClaudeHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	claudeInfo := &ClaudeResponseInfo{
		ResponseId:   helper.GetResponseID(c),
		Created:      common.GetTimestamp(),
		Model:        info.UpstreamModelName,
		ResponseText: strings.Builder{},
		Usage:        &dto.Usage{},
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	logger.LogDebug(c, "responseBody: %s", responseBody)
	handleErr := HandleClaudeResponseData(c, info, claudeInfo, resp, responseBody)
	if handleErr != nil {
		return nil, handleErr
	}
	return claudeInfo.Usage, nil
}

// HandleNovaStreamResponseData 处理 Nova 流式响应的单个 chunk
func HandleNovaStreamResponseData(c *gin.Context, info *relaycommon.RelayInfo, novaInfo *NovaStreamResponseInfo, data string) *types.NewAPIError {
	var chunk map[string]interface{}
	if err := common.UnmarshalJsonStr(data, &chunk); err != nil {
		common.SysLog("error unmarshalling Nova stream response: " + err.Error())
		return types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	// 1. 检查是否有错误
	if errMsg, ok := chunk["error"].(string); ok && errMsg != "" {
		return types.NewError(fmt.Errorf("nova stream error: %s", errMsg), types.ErrorCodeBadResponseBody)
	}
	// 2. 提取 usage（Bedrock metadata 事件最后到达，且不带文本/stopReason，
	//    必须在 shouldSend 提前 return 之前记录，否则真实 usage 会被丢弃）
	if metadata, ok := chunk["metadata"].(map[string]interface{}); ok {
		if usage, ok := metadata["usage"].(map[string]interface{}); ok {
			if inputTokens, ok := usage["inputTokens"].(float64); ok {
				novaInfo.Usage.PromptTokens = int(inputTokens)
			}
			if outputTokens, ok := usage["outputTokens"].(float64); ok {
				novaInfo.Usage.CompletionTokens = int(outputTokens)
			}
			if cacheRead, ok := usage["cacheReadInputTokens"].(float64); ok {
				novaInfo.Usage.PromptTokensDetails.CachedTokens = int(cacheRead)
			}
			if cacheWrite, ok := usage["cacheWriteInputTokens"].(float64); ok {
				novaInfo.Usage.PromptTokensDetails.CachedCreationTokens = int(cacheWrite)
			}
			if cache5m, ok := usage["cacheCreation5mInputTokens"].(float64); ok {
				novaInfo.Usage.ClaudeCacheCreation5mTokens = int(cache5m)
			}
			if cache1h, ok := usage["cacheCreation1hInputTokens"].(float64); ok {
				novaInfo.Usage.ClaudeCacheCreation1hTokens = int(cache1h)
			}
			novaInfo.Usage.TotalTokens = novaInfo.Usage.PromptTokens + novaInfo.Usage.CompletionTokens
			novaInfo.Usage.UsageSemantic = "anthropic"
		}
	}
	shouldSend := false
	// 3. 提取文本内容 (Nova 格式)
	var contentText string
	if contentBlockDelta, ok := chunk["contentBlockDelta"].(map[string]interface{}); ok {
		if delta, ok := contentBlockDelta["delta"].(map[string]interface{}); ok {
			if text, ok := delta["text"].(string); ok && text != "" {
				contentText = text
				novaInfo.ResponseText.WriteString(text)
			}
		}
	}
	if contentText != "" {
		shouldSend = true
	}
	// 4. 提取 stopReason
	if stopReason, ok := chunk["stopReason"].(string); ok && stopReason != "" {
		novaInfo.StopReason = stopReason
	}
	if novaInfo.StopReason != "" && !novaInfo.FinishReasonSent {
		shouldSend = true
	}
	if !shouldSend {
		return nil
	}
	// 5. 转换为 OpenAI 格式并发送
	//    使用 ChatCompletionsStreamResponse：其 Usage 是指针，未赋值时序列化为 "usage":null，
	//    符合 OpenAI 流式协议（非最终 chunk 不应带 usage 数字）。
	response := dto.ChatCompletionsStreamResponse{
		Id:      novaInfo.ResponseId,
		Object:  "chat.completion.chunk",
		Created: novaInfo.Created,
		Model:   novaInfo.Model,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Index: 0,
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					Content: &contentText,
					Role:    "assistant",
				},
				FinishReason: nil,
			},
		},
	}
	// 如果有 stopReason，在最后一个 chunk 中设置
	if novaInfo.StopReason != "" && !novaInfo.FinishReasonSent {
		response.Choices[0].FinishReason = aws.String(stopReasonClaude2OpenAI(novaInfo.StopReason))
		response.Choices[0].Delta = dto.ChatCompletionsStreamResponseChoiceDelta{}
		novaInfo.FinishReasonSent = true
	}

	err := helper.ObjectData(c, response)
	if err != nil {
		logger.LogError(c, "send_nova_stream_response_failed: "+err.Error())
	}

	return nil
}

// HandleNovaStreamFinal 处理 Nova 流式结束
func HandleNovaStreamFinal(c *gin.Context, info *relaycommon.RelayInfo, novaInfo *NovaStreamResponseInfo) {
	// 1. 补全缺失的 Usage 信息
	if novaInfo.Usage.PromptTokens == 0 || novaInfo.Usage.CompletionTokens == 0 || novaInfo.Usage.TotalTokens == 0 {
		// 使用回退方案：根据响应文本估算（参考 Claude 的做法）
		fallback := service.ResponseText2Usage(c, novaInfo.ResponseText.String(), info.UpstreamModelName, info.GetEstimatePromptTokens())

		// 补全缺失的字段
		if novaInfo.Usage.PromptTokens == 0 {
			novaInfo.Usage.PromptTokens = fallback.PromptTokens
		}
		if novaInfo.Usage.CompletionTokens == 0 {
			novaInfo.Usage.CompletionTokens = fallback.CompletionTokens
		}
		if novaInfo.Usage.TotalTokens == 0 {
			novaInfo.Usage.TotalTokens = fallback.TotalTokens
		}
	}

	// 2. 设置语义标识（用于计费）
	if novaInfo.Usage != nil {
		novaInfo.Usage.UsageSemantic = "anthropic"
	}
	if novaInfo.Usage != nil && novaInfo.Usage.BillingUsage == nil {
		claudeUsage := &dto.ClaudeUsage{
			InputTokens:               novaInfo.Usage.PromptTokens,
			OutputTokens:              novaInfo.Usage.CompletionTokens,
			CacheReadInputTokens:      novaInfo.Usage.PromptTokensDetails.CachedTokens,
			CacheCreationInputTokens:  novaInfo.Usage.PromptTokensDetails.CachedCreationTokens,
		}
		if novaInfo.Usage.ClaudeCacheCreation5mTokens > 0 || novaInfo.Usage.ClaudeCacheCreation1hTokens > 0 {
			claudeUsage.CacheCreation = &dto.ClaudeCacheCreationUsage{
				Ephemeral5mInputTokens: novaInfo.Usage.ClaudeCacheCreation5mTokens,
				Ephemeral1hInputTokens: novaInfo.Usage.ClaudeCacheCreation1hTokens,
			}
		}
		novaInfo.Usage.BillingUsage = dto.NewClaudeMessagesBillingUsage(claudeUsage)
	}

	// 3. 发送最终 Usage 响应（OpenAI 格式）
	if info.RelayFormat == types.RelayFormatOpenAI {
		if info.ShouldIncludeUsage {
			// 构造 OpenAI 格式的 Usage
			openAIUsage := dto.Usage{
				PromptTokens:     novaInfo.Usage.PromptTokens,
				CompletionTokens: novaInfo.Usage.CompletionTokens,
				TotalTokens:      novaInfo.Usage.TotalTokens,
			}
			// 生成最终响应
			response := helper.GenerateFinalUsageResponse(
				novaInfo.ResponseId,
				novaInfo.Created,
				info.UpstreamModelName,
				openAIUsage,
			)
			// 发送响应
			err := helper.ObjectData(c, response)
			if err != nil {
				logger.LogError(c, "send nova final usage response failed: " + err.Error())
			}
		}
		// 发送 [DONE] 信号
		helper.Done(c)
	}
}