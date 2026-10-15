package aws

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"

	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockruntimeTypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go/auth/bearer"
)

// bedrock-mantle 端点相关常量（OpenAI 兼容协议，Gemma 4 系列专用）
const (
	mantleBaseURLFormat       = "https://bedrock-mantle.%s.api.aws"
	mantleChatCompletionsPath = "/openai/v1/chat/completions"
	mantleSigV4ServiceName    = "bedrock"
	mantleSharedProfile       = "bedrock-anywhere"
	mantleDefaultRegion       = "us-east-1"
)

// doMantleRequest 通过裸 HTTP 调用 bedrock-mantle 的 OpenAI 兼容端点。
// 认证优先级：共享 profile(credential_process/Roles Anywhere) -> 静态 AK/SK -> Bearer API Key。
func doMantleRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	ctx := c.Request.Context()
	awsSecret := strings.Split(info.ApiKey, "|")

	bodyBytes, err := io.ReadAll(requestBody)
	if err != nil {
		return nil, errors.Wrap(err, "read mantle request body")
	}

	var region string
	var creds aws.Credentials
	var bearerToken string

	// 1. 优先使用共享配置 profile（与 newAwsClient 一致，支持 IAM Roles Anywhere 临时凭证）
	if cfg, cfgErr := config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile(mantleSharedProfile)); cfgErr == nil {
		region = awsSecret[0] // profile 模式下 info.ApiKey 存放 region
		if region == "" {
			region = cfg.Region
		}
		creds, err = cfg.Credentials.Retrieve(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "retrieve credentials from shared config profile for mantle")
		}
	} else {
		// 2. 回退：从 info.ApiKey 解析
		switch len(awsSecret) {
		case 2: // <api-key>|<region>
			bearerToken = awsSecret[0]
			region = awsSecret[1]
		case 3: // <ak>|<sk>|<region>
			creds, err = credentials.NewStaticCredentialsProvider(awsSecret[0], awsSecret[1], "").Retrieve(ctx)
			if err != nil {
				return nil, errors.Wrap(err, "retrieve static credentials for mantle")
			}
			region = awsSecret[2]
		default:
			return nil, errors.New("invalid aws key for mantle, expect shared profile, <api-key>|<region> or <ak>|<sk>|<region>")
		}
	}

	if region == "" {
		region = mantleDefaultRegion
	}

	url := fmt.Sprintf(mantleBaseURLFormat, region) + mantleChatCompletionsPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, errors.Wrap(err, "new mantle request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	} else {
		payloadHash := sha256.Sum256(bodyBytes)
		signer := v4.NewSigner()
		// creds 带 SessionToken 时，signer 会自动加 X-Amz-Security-Token 并纳入签名
		if err := signer.SignHTTP(ctx, creds, req, hex.EncodeToString(payloadHash[:]), mantleSigV4ServiceName, region, time.Now()); err != nil {
			return nil, errors.Wrap(err, "sign mantle request with sigv4")
		}
	}

	headerOverride, err := channel.ResolveHeaderOverride(info, c)
	if err != nil {
		return nil, err
	}
	for key, value := range headerOverride {
		req.Header.Set(key, value)
	}

	httpClient, err := service.GetHttpClientWithProxySettings(info.ChannelSetting.Proxy, info.ChannelSetting)
	if err != nil {
		return nil, errors.Wrap(err, "new proxy http client for mantle")
	}
	return httpClient.Do(req)
}

// getAwsErrorStatusCode extracts HTTP status code from AWS SDK error
func getAwsErrorStatusCode(err error) int {
	// Check for HTTP response error which contains status code
	var httpErr interface{ HTTPStatusCode() int }
	if errors.As(err, &httpErr) {
		return httpErr.HTTPStatusCode()
	}
	// Default to 500 if we can't determine the status code
	return http.StatusInternalServerError
}

func newAwsInvokeContext(parent context.Context) (context.Context, context.CancelFunc) {
	if common.RelayTimeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, time.Duration(common.RelayTimeout)*time.Second)
}

func newAwsInvokeError(requestContext context.Context, err error, operation string) *types.NewAPIError {
	options := make([]types.NewAPIErrorOptions, 0, 1)
	if requestContext.Err() != nil {
		options = append(options, types.ErrOptionWithSkipRetry())
	}
	return types.NewOpenAIError(
		errors.Wrap(err, operation),
		types.ErrorCodeAwsInvokeError,
		getAwsErrorStatusCode(err),
		options...,
	)
}
func stopReasonClaude2OpenAI(reason string) string {
	return relayconvert.StopReasonClaudeToOpenAI(reason)
}

func newAwsClient(c *gin.Context, info *relaycommon.RelayInfo) (*bedrockruntime.Client, error) {
	httpClient, err := service.GetHttpClientWithProxySettings(info.ChannelSetting.Proxy, info.ChannelSetting)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client failed: %w", err)
	}

	awsSecret := strings.Split(info.ApiKey, "|")
	var client *bedrockruntime.Client

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithSharedConfigProfile("bedrock-anywhere"),  // 指定 profile
	)
	if err == nil {
		client := bedrockruntime.NewFromConfig(cfg, func(o *bedrockruntime.Options) {
			o.HTTPClient = httpClient
			o.Region = awsSecret[0]
		})
		return client, nil
	}
	switch len(awsSecret) {
	case 2:
		apiKey := awsSecret[0]
		region := awsSecret[1]
		client = bedrockruntime.New(bedrockruntime.Options{
			Region:                  region,
			BearerAuthTokenProvider: bearer.StaticTokenProvider{Token: bearer.Token{Value: apiKey}},
			HTTPClient:              httpClient,
		})
	case 3:
		ak := awsSecret[0]
		sk := awsSecret[1]
		region := awsSecret[2]
		client = bedrockruntime.New(bedrockruntime.Options{
			Region:      region,
			Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(ak, sk, "")),
			HTTPClient:  httpClient,
		})
	default:
		return nil, errors.New("invalid aws secret key")
	}

	return client, nil
}

func doAwsClientRequest(c *gin.Context, info *relaycommon.RelayInfo, a *Adaptor, requestBody io.Reader) (any, error) {
	awsCli, err := newAwsClient(c, info)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeChannelAwsClientError)
	}
	a.AwsClient = awsCli

	awsModelId := getAwsModelID(info.UpstreamModelName)

	awsRegionPrefix := getAwsRegionPrefix(awsCli.Options().Region)
	canCrossRegion := awsModelCanCrossRegion(awsModelId, awsRegionPrefix)
	if canCrossRegion {
		awsModelId = awsModelCrossRegion(awsModelId, awsRegionPrefix)
	}
	a.AwsModelId = awsModelId

	// init empty request.header
	requestHeader := http.Header{}
	a.SetupRequestHeader(c, &requestHeader, info)
	headerOverride, err := channel.ResolveHeaderOverride(info, c)
	if err != nil {
		return nil, err
	}
	for key, value := range headerOverride {
		requestHeader.Set(key, value)
	}

	if a.ModelType == ModelTypeConverse {
		return nil, nil
	}
	if isNovaModel(awsModelId) {
		var novaReq *NovaRequest
		err = common.DecodeJson(requestBody, &novaReq)
		if err != nil {
			return nil, types.NewError(errors.Wrap(err, "decode nova request fail"), types.ErrorCodeBadRequestBody)
		}

		reqBody, err := common.Marshal(novaReq)
		if err != nil {
			return nil, types.NewError(errors.Wrap(err, "marshal nova request"), types.ErrorCodeBadResponseBody)
		}
		if info.IsStream {
			awsReq := &bedrockruntime.InvokeModelWithResponseStreamInput{
				ModelId:     aws.String(awsModelId),
				Accept:      aws.String("application/json"),
				ContentType: aws.String("application/json"),
				Body: 		 reqBody,
			}
			a.AwsReq = awsReq
			return nil, nil
		} else {
			// 使用InvokeModel API，但使用Nova格式的请求体
			awsReq := &bedrockruntime.InvokeModelInput{
				ModelId:     aws.String(awsModelId),
				Accept:      aws.String("application/json"),
				ContentType: aws.String("application/json"),
				Body: 		 reqBody,
			}
			a.AwsReq = awsReq
			return nil, nil
		}
	} else {
		awsClaudeReq, err := formatRequest(requestBody, requestHeader)
		if err != nil {
			return nil, types.NewError(errors.Wrap(err, "format aws request fail"), types.ErrorCodeBadRequestBody)
		}

		if info.IsStream {
			awsReq := &bedrockruntime.InvokeModelWithResponseStreamInput{
				ModelId:     aws.String(awsModelId),
				Accept:      aws.String("application/json"),
				ContentType: aws.String("application/json"),
			}
			awsReq.Body, err = buildAwsRequestBody(c, info, awsClaudeReq)
			if err != nil {
				return nil, types.NewError(errors.Wrap(err, "marshal aws request fail"), types.ErrorCodeBadRequestBody)
			}
			a.AwsReq = awsReq
			return nil, nil
		} else {
			awsReq := &bedrockruntime.InvokeModelInput{
				ModelId:     aws.String(awsModelId),
				Accept:      aws.String("application/json"),
				ContentType: aws.String("application/json"),
			}
			awsReq.Body, err = buildAwsRequestBody(c, info, awsClaudeReq)
			if err != nil {
				return nil, types.NewError(errors.Wrap(err, "marshal aws request fail"), types.ErrorCodeBadRequestBody)
			}
			a.AwsReq = awsReq
			return nil, nil
		}
	}
}

// buildAwsRequestBody prepares the payload for AWS requests, applying passthrough rules when enabled.
func buildAwsRequestBody(c *gin.Context, info *relaycommon.RelayInfo, awsClaudeReq any) ([]byte, error) {
	if model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ChannelSetting.PassThroughBodyEnabled {
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return nil, errors.Wrap(err, "get request body for pass-through fail")
		}
		body, err := storage.Bytes()
		if err != nil {
			return nil, errors.Wrap(err, "get request body bytes fail")
		}
		var data map[string]interface{}
		if err := common.Unmarshal(body, &data); err != nil {
			return nil, errors.Wrap(err, "pass-through unmarshal request body fail")
		}
		delete(data, "model")
		delete(data, "stream")
		return common.Marshal(data)
	}
	return common.Marshal(awsClaudeReq)
}

func getAwsRegionPrefix(awsRegionId string) string {
	parts := strings.Split(awsRegionId, "-")
	regionPrefix := ""
	if len(parts) > 0 {
		regionPrefix = parts[0]
	}
	return regionPrefix
}

func awsModelCanCrossRegion(awsModelId, awsRegionPrefix string) bool {
	regionSet, exists := awsModelCanCrossRegionMap[awsModelId]
	return exists && regionSet[awsRegionPrefix]
}

func awsModelCrossRegion(awsModelId, awsRegionPrefix string) string {
	modelPrefix, find := awsRegionCrossModelPrefixMap[awsRegionPrefix]
	if !find {
		return awsModelId
	}
	return modelPrefix + "." + awsModelId
}

func getAwsModelID(requestModel string) string {
	if awsModelIDName, ok := awsModelIDMap[requestModel]; ok {
		return awsModelIDName
	}
	return requestModel
}

func awsHandler(c *gin.Context, info *relaycommon.RelayInfo, a *Adaptor) (*types.NewAPIError, *dto.Usage) {

	requestContext := c.Request.Context()
	ctx, cancel := newAwsInvokeContext(requestContext)
	defer cancel()

	awsResp, err := a.AwsClient.InvokeModel(ctx, a.AwsReq.(*bedrockruntime.InvokeModelInput))
	if err != nil {
		return newAwsInvokeError(requestContext, err, "InvokeModel"), nil
	}

	claudeInfo := &claude.ClaudeResponseInfo{
		ResponseId:   helper.GetResponseID(c),
		Created:      common.GetTimestamp(),
		Model:        info.UpstreamModelName,
		ResponseText: strings.Builder{},
		Usage:        &dto.Usage{},
	}

	// 复制上游 Content-Type 到客户端响应头
	if awsResp.ContentType != nil && *awsResp.ContentType != "" {
		c.Writer.Header().Set("Content-Type", *awsResp.ContentType)
	}

	handlerErr := claude.HandleClaudeResponseData(c, info, claudeInfo, nil, awsResp.Body)
	if handlerErr != nil {
		return handlerErr, nil
	}
	return nil, claudeInfo.Usage
}

func awsStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, a *Adaptor) (*types.NewAPIError, *dto.Usage) {
	requestContext := c.Request.Context()
	ctx, cancel := newAwsInvokeContext(requestContext)
	defer cancel()

	awsResp, err := a.AwsClient.InvokeModelWithResponseStream(ctx, a.AwsReq.(*bedrockruntime.InvokeModelWithResponseStreamInput))
	if err != nil {
		return newAwsInvokeError(requestContext, err, "InvokeModelWithResponseStream"), nil
	}
	stream := awsResp.GetStream()
	defer stream.Close()

	isNova := a.ModelType == ModelTypeNova

	var (
		claudeInfo *claude.ClaudeResponseInfo
		novaInfo   *claude.NovaStreamResponseInfo
	)

	if isNova {
		novaInfo = &claude.NovaStreamResponseInfo{
			ResponseId:   helper.GetResponseID(c),
			Created:      common.GetTimestamp(),
			Model:        info.UpstreamModelName,
			ResponseText: strings.Builder{},
			Usage:        &dto.Usage{},
		}
	} else {
		claudeInfo = &claude.ClaudeResponseInfo{
			ResponseId:   helper.GetResponseID(c),
			Created:      common.GetTimestamp(),
			Model:        info.UpstreamModelName,
			ResponseText: strings.Builder{},
			Usage:        &dto.Usage{},
		}
	}
	events := stream.Events()
streamLoop:
	for {
		select {
		case <-ctx.Done():
			break streamLoop
		case event, ok := <-events:
			if !ok {
				break streamLoop
			}
			if ctx.Err() != nil {
				break streamLoop
			}

			switch v := event.(type) {
			case *bedrockruntimeTypes.ResponseStreamMemberChunk:
				info.SetFirstResponseTime()
				var respErr *types.NewAPIError
				if isNova {
					// 使用 Nova 专用的流式处理
					respErr = claude.HandleNovaStreamResponseData(c, info, novaInfo, string(v.Value.Bytes))
				} else {
					// 使用原有的 Claude 流式处理
					respErr = claude.HandleStreamResponseData(c, info, claudeInfo, string(v.Value.Bytes))
				}
				if respErr != nil {
					return respErr, nil
				}
			case *bedrockruntimeTypes.UnknownUnionMember:
				common.SysLog(fmt.Sprintf("aws stream: unknown tag: %s", v.Tag))
				return types.NewError(errors.New("unknown response type"), types.ErrorCodeInvalidRequest), nil
			default:
				common.SysLog("aws stream: nil or unknown response type")
				return types.NewError(errors.New("nil or unknown response type"), types.ErrorCodeInvalidRequest), nil
			}
		}
	}

	_ = stream.Close()
	// 发送结束信号
	if isNova {
		// Nova 结束处理
		claude.HandleNovaStreamFinal(c, info, novaInfo)
		return nil, novaInfo.Usage
	} else {
		// Claude 结束处理
		claude.HandleStreamFinalResponse(c, info, claudeInfo)
		return nil, claudeInfo.Usage
	}
}

// Nova模型处理函数
func handleNovaRequest(c *gin.Context, info *relaycommon.RelayInfo, a *Adaptor) (*types.NewAPIError, *dto.Usage) {

	requestContext := c.Request.Context()
	ctx, cancel := newAwsInvokeContext(requestContext)
	defer cancel()

	awsResp, err := a.AwsClient.InvokeModel(ctx, a.AwsReq.(*bedrockruntime.InvokeModelInput))
	if err != nil {
		return newAwsInvokeError(requestContext, err, "InvokeModel"), nil
	}

	// 解析Nova响应
	var novaResp struct {
		Output struct {
			Message struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		} `json:"output"`
		StopReason string `json:"stopReason"`
		Usage struct {
			InputTokens  int `json:"inputTokens"`
			OutputTokens int `json:"outputTokens"`
			TotalTokens  int `json:"totalTokens"`
		} `json:"usage"`
	}

	if err := common.Unmarshal(awsResp.Body, &novaResp); err != nil {
		return types.NewError(errors.Wrap(err, "unmarshal nova response"), types.ErrorCodeBadResponseBody), nil
	}

	contentText := ""
	if len(novaResp.Output.Message.Content) > 0 {
		contentText = novaResp.Output.Message.Content[0].Text
	}
	stopReason := "stop"
	if novaResp.StopReason != "" {
		stopReason = novaResp.StopReason
	}
	// 构造OpenAI格式响应
	response := dto.OpenAITextResponse{
		Id:      helper.GetResponseID(c),
		Object:  "chat.completion",
		Created: common.GetTimestamp(),
		Model:   info.UpstreamModelName,
		Choices: []dto.OpenAITextResponseChoice{{
			Index: 0,
			Message: dto.Message{
				Role:    "assistant",
				Content: contentText,
			},
			FinishReason: stopReason,
		}},
		Usage: dto.Usage{
			PromptTokens:     novaResp.Usage.InputTokens,
			CompletionTokens: novaResp.Usage.OutputTokens,
			TotalTokens:      novaResp.Usage.TotalTokens,
		},
	}

	c.JSON(http.StatusOK, response)
	return nil, &response.Usage
}

func handleGemmaRequest(c *gin.Context, info *relaycommon.RelayInfo, a *Adaptor) (*types.NewAPIError, *dto.Usage) {
	requestContext := c.Request.Context()
	ctx, cancel := newAwsInvokeContext(requestContext)
	defer cancel()

	converseInput := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(a.AwsModelId),
		Messages: buildConverseMessages(a.ConverseReq),
	}
	if a.ConverseReq.InferenceConfig != nil {
		converseInput.InferenceConfig = buildConverseInferenceConfig(a.ConverseReq.InferenceConfig, a.AwsModelId)
	}

	awsResp, err := a.AwsClient.Converse(ctx, converseInput)
	if err != nil {
		return newAwsInvokeError(requestContext, err, "Converse"), nil
	}

	contentText := ""
	stopReason := "stop"
	if awsResp.Output != nil {
		if msg, ok := awsResp.Output.(*bedrockruntimeTypes.ConverseOutputMemberMessage); ok {
			for _, content := range msg.Value.Content {
				if textBlock, ok := content.(*bedrockruntimeTypes.ContentBlockMemberText); ok {
					contentText = textBlock.Value
				}
			}
		}
	}
	if awsResp.StopReason != "" {
		stopReason = string(awsResp.StopReason)
	}

	response := dto.OpenAITextResponse{
		Id:      helper.GetResponseID(c),
		Object:  "chat.completion",
		Created: common.GetTimestamp(),
		Model:   info.UpstreamModelName,
		Choices: []dto.OpenAITextResponseChoice{{
			Index: 0,
			Message: dto.Message{
				Role:    "assistant",
				Content: contentText,
			},
			FinishReason: stopReason,
		}},
	}

	if awsResp.Usage != nil {
		response.Usage = dto.Usage{}
		if awsResp.Usage.InputTokens != nil {
			response.Usage.PromptTokens  = int(*awsResp.Usage.InputTokens)
		}
		if awsResp.Usage.OutputTokens != nil {
			response.Usage.CompletionTokens = int(*awsResp.Usage.OutputTokens)
		}
		response.Usage.TotalTokens = response.Usage.PromptTokens + response.Usage.CompletionTokens
		if awsResp.Usage.CacheReadInputTokens != nil {
			response.Usage.PromptTokensDetails.CachedTokens = int(*awsResp.Usage.CacheReadInputTokens)
		}
		if awsResp.Usage.CacheWriteInputTokens != nil {
			response.Usage.PromptTokensDetails.CachedCreationTokens = int(*awsResp.Usage.CacheWriteInputTokens)
			response.Usage.PromptTokensDetails.CacheWriteTokens = int(*awsResp.Usage.CacheWriteInputTokens)
		}
		response.Usage.UsageSemantic = "anthropic"
	}

	c.JSON(http.StatusOK, response)
	return nil, &response.Usage
}

func gemmaStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, a *Adaptor) (*types.NewAPIError, *dto.Usage) {
	requestContext := c.Request.Context()
	ctx, cancel := newAwsInvokeContext(requestContext)
	defer cancel()

	converseInput := &bedrockruntime.ConverseStreamInput{
		ModelId:  aws.String(a.AwsModelId),
		Messages: buildConverseMessages(a.ConverseReq),
	}
	if a.ConverseReq.InferenceConfig != nil {
		converseInput.InferenceConfig = buildConverseInferenceConfig(a.ConverseReq.InferenceConfig, a.AwsModelId)
	}

	awsResp, err := a.AwsClient.ConverseStream(ctx, converseInput)
	if err != nil {
		return newAwsInvokeError(requestContext, err, "ConverseStream"), nil
	}
	stream := awsResp.GetStream()
	defer stream.Close()

	novaInfo := &claude.NovaStreamResponseInfo{
		ResponseId:   helper.GetResponseID(c),
		Created:      common.GetTimestamp(),
		Model:        info.UpstreamModelName,
		ResponseText: strings.Builder{},
		Usage:        &dto.Usage{},
	}

	finishReasonSent := false
	events := stream.Events()
streamLoop:
	for {
		select {
		case <-ctx.Done():
			break streamLoop
		case event, ok := <-events:
			if !ok {
				break streamLoop
			}
			if ctx.Err() != nil {
				break streamLoop
			}

			info.SetFirstResponseTime()

			switch v := event.(type) {
			case *bedrockruntimeTypes.ConverseStreamOutputMemberContentBlockDelta:
				if textDelta, ok := v.Value.Delta.(*bedrockruntimeTypes.ContentBlockDeltaMemberText); ok {
					respErr := claude.HandleNovaStreamResponseData(c, info, novaInfo,
						fmt.Sprintf(`{"contentBlockDelta":{"delta":{"text":%q}}}`, textDelta.Value))
					if respErr != nil {
						return respErr, nil
					}
				}
			case *bedrockruntimeTypes.ConverseStreamOutputMemberMessageStop:
				if finishReasonSent {
					common.SysLog("MessageStop: already sent finish_reason, skipping")
					continue
				}
				finishReasonSent = true
				stopReason := string(v.Value.StopReason)
				if stopReason != "" {
					respErr := claude.HandleNovaStreamResponseData(c, info, novaInfo,
						fmt.Sprintf(`{"stopReason":%q}`, stopReason))
					if respErr != nil {
						return respErr, nil
					}
				}
			case *bedrockruntimeTypes.ConverseStreamOutputMemberMetadata:
				if v.Value.Usage != nil {
					var inputTokens, outputTokens int32
					if v.Value.Usage.InputTokens != nil {
						inputTokens = *v.Value.Usage.InputTokens
					}
					if v.Value.Usage.OutputTokens != nil {
						outputTokens = *v.Value.Usage.OutputTokens
					}
					usageJson := fmt.Sprintf(`{"metadata":{"usage":{"inputTokens":%d,"outputTokens":%d}}}`,
						inputTokens, outputTokens)
					respErr := claude.HandleNovaStreamResponseData(c, info, novaInfo, usageJson)
					if respErr != nil {
						return respErr, nil
					}
				}
			}
		}
	}

	_ = stream.Close()
	claude.HandleNovaStreamFinal(c, info, novaInfo)
	return nil, novaInfo.Usage
}

func buildConverseMessages(req *NovaRequest) []bedrockruntimeTypes.Message {
	messages := make([]bedrockruntimeTypes.Message, len(req.Messages))
	for i, msg := range req.Messages {
		role := bedrockruntimeTypes.ConversationRoleUser
		if msg.Role == "assistant" {
			role = bedrockruntimeTypes.ConversationRoleAssistant
		}
		contentBlocks := make([]bedrockruntimeTypes.ContentBlock, len(msg.Content))
		for j, c := range msg.Content {
			contentBlocks[j] = &bedrockruntimeTypes.ContentBlockMemberText{Value: c.Text}
		}
		messages[i] = bedrockruntimeTypes.Message{
			Role:    role,
			Content: contentBlocks,
		}
	}
	return messages
}

// modelMaxOutputTokensMap 定义模型的最大输出 token 限制
var modelMaxOutputTokensMap = map[string]int32{
	"meta.llama3-":          2048,   // Llama 3（非 3.1）
	"writer.palmyra-vision": 2048,   // Palmyra Vision 7B（总上下文 4096，留一半给输入）
	// 其他模型按需添加
}

// getModelMaxOutputTokens 返回模型的最大输出 tokens，0 表示无限制
func getModelMaxOutputTokens(modelId string) int32 {
	for prefix, maxTokens := range modelMaxOutputTokensMap {
		if strings.HasPrefix(modelId, prefix) {
			return maxTokens
		}
	}
	return 0
}

// capMaxTokensInRequest 对请求中的 max_tokens 进行模型级别截断
func capMaxTokensInRequest(req *dto.GeneralOpenAIRequest, modelId string) {
	if req.MaxTokens == nil {
		return
	}
	if maxTokens := getModelMaxOutputTokens(modelId); maxTokens > 0 {
		if int32(*req.MaxTokens) > maxTokens {
			v := uint(maxTokens)
			req.MaxTokens = &v
		}
	}
}

func buildConverseInferenceConfig(cfg *NovaInferenceConfig, modelId string) *bedrockruntimeTypes.InferenceConfiguration {
	result := &bedrockruntimeTypes.InferenceConfiguration{}
	if cfg.MaxTokens != nil {
		v := int32(*cfg.MaxTokens)
		if maxTokens := getModelMaxOutputTokens(modelId); maxTokens > 0 && v > maxTokens {
			v = maxTokens
		}
		result.MaxTokens = &v
	}
	if !isReasoningModel(modelId) && cfg.Temperature != nil {
		v := float32(*cfg.Temperature)
		result.Temperature = &v
	}
	if !isReasoningModel(modelId) && cfg.TopP != nil {
		v := float32(*cfg.TopP)
		result.TopP = &v
	}
	if cfg.StopSequences != nil {
		result.StopSequences = cfg.StopSequences
	}
	return result
}

// isReasoningModel 判断模型是否不支持 temperature / top_p / top_k 参数。
// 根据 Anthropic 官方文档：Claude Opus 4.7+ 和所有 Claude 5 系列已弃用这些参数。
// 根据 OpenAI Bedrock 模型卡：GPT-5.6+ 推理模型同样不支持。
func isReasoningModel(modelId string) bool {
	// OpenAI GPT-5.6+ 推理模型
	if strings.HasPrefix(modelId, "openai.gpt-5.6") ||
		strings.HasPrefix(modelId, "openai.gpt-6") ||
		strings.HasPrefix(modelId, "openai.gpt-daybreak") {
		return true
	}
	// Claude Opus 4.7+（含 4.8）
	if strings.HasPrefix(modelId, "anthropic.claude-opus-4-7") ||
		strings.HasPrefix(modelId, "anthropic.claude-opus-4-8") {
		return true
	}
	// Claude 5 系列全部
	if strings.HasPrefix(modelId, "anthropic.claude-opus-5") ||
		strings.HasPrefix(modelId, "anthropic.claude-sonnet-5") ||
		strings.HasPrefix(modelId, "anthropic.claude-fable-5") ||
		strings.HasPrefix(modelId, "anthropic.claude-mythos-5") ||
		strings.HasPrefix(modelId, "anthropic.claude-haiku-5") {
		return true
	}
	// Claude Mythos Preview
	if modelId == "anthropic.claude-mythos-preview" {
		return true
	}
	return false
}
