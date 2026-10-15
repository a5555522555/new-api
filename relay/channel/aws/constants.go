package aws

import "strings"

var awsModelIDMap = map[string]string{
	"claude-3-sonnet-20240229":   "anthropic.claude-3-sonnet-20240229-v1:0",
	"claude-3-opus-20240229":     "anthropic.claude-3-opus-20240229-v1:0",
	"claude-3-haiku-20240307":    "anthropic.claude-3-haiku-20240307-v1:0",
	"claude-3-5-sonnet-20240620": "anthropic.claude-3-5-sonnet-20240620-v1:0",
	"claude-3-5-sonnet-20241022": "anthropic.claude-3-5-sonnet-20241022-v2:0",
	"claude-3-5-haiku-20241022":  "anthropic.claude-3-5-haiku-20241022-v1:0",
	"claude-3-7-sonnet-20250219": "anthropic.claude-3-7-sonnet-20250219-v1:0",
	"claude-sonnet-4-20250514":   "anthropic.claude-sonnet-4-20250514-v1:0",
	"claude-opus-4-20250514":     "anthropic.claude-opus-4-20250514-v1:0",
	"claude-opus-4-1-20250805":   "anthropic.claude-opus-4-1-20250805-v1:0",
	"claude-sonnet-4-5-20250929": "anthropic.claude-sonnet-4-5-20250929-v1:0",
	"claude-sonnet-4-6":          "anthropic.claude-sonnet-4-6",
	"claude-haiku-4-5-20251001":  "anthropic.claude-haiku-4-5-20251001-v1:0",
	"claude-opus-4-5-20251101":   "anthropic.claude-opus-4-5-20251101-v1:0",
	"claude-opus-4-6":            "anthropic.claude-opus-4-6-v1",
	"claude-opus-4-7":            "anthropic.claude-opus-4-7",
	"claude-opus-4-8":            "anthropic.claude-opus-4-8",
	// Claude 5 系列
	"claude-fable-5-1":           "anthropic.claude-fable-5-1",
	"claude-mythos-5-1":          "anthropic.claude-mythos-5-1",
	"claude-opus-5":              "anthropic.claude-opus-5",
	"claude-sonnet-5":            "anthropic.claude-sonnet-5",
	"claude-mythos-5":            "anthropic.claude-mythos-5",
	"claude-fable-5":             "anthropic.claude-fable-5",
	"claude-mythos-preview":      "anthropic.claude-mythos-preview",
	// Nova models
	"nova-micro-v1:0":   "amazon.nova-micro-v1:0",
	"nova-lite-v1:0":    "amazon.nova-lite-v1:0",
	"nova-pro-v1:0":     "amazon.nova-pro-v1:0",
	"nova-premier-v1:0": "amazon.nova-premier-v1:0",
	"nova-canvas-v1:0":  "amazon.nova-canvas-v1:0",
	"nova-reel-v1:0":    "amazon.nova-reel-v1:0",
	"nova-reel-v1:1":    "amazon.nova-reel-v1:1",
	"nova-sonic-v1:0":   "amazon.nova-sonic-v1:0",
	// OpenAI GPT 系列（Bedrock 上的 OpenAI 模型）
	"gpt-5.6-luna":      "openai.gpt-5.6-luna",
	"gpt-5.6-sol":       "openai.gpt-5.6-sol",
	"gpt-5.6-terra":     "openai.gpt-5.6-terra",
	"gpt-6-astra":       "openai.gpt-6-astra",
	// Gemma 4 系列：仅在 bedrock-mantle 端点可用（OpenAI 兼容协议）
	"gemma-4-31b":     "google.gemma-4-31b",
	"gemma-4-26b-a4b": "google.gemma-4-26b-a4b",
	"gemma-4-e2b":     "google.gemma-4-e2b",
}

var awsModelCanCrossRegionMap = map[string]map[string]bool{
	"anthropic.claude-3-sonnet-20240229-v1:0": {
		"us": true,
	},
	"anthropic.claude-3-opus-20240229-v1:0": {
		"us": true,
	},
	"anthropic.claude-3-haiku-20240307-v1:0": {
		"us": true,
	},
	"anthropic.claude-3-5-sonnet-20240620-v1:0": {
		"us": true,
	},
	"anthropic.claude-3-5-sonnet-20241022-v2:0": {
		"us": true,
	},
	"anthropic.claude-3-5-haiku-20241022-v1:0": {
		"us": true,
	},
	"anthropic.claude-3-7-sonnet-20250219-v1:0": {
		"us": true,
	},
	"anthropic.claude-sonnet-4-20250514-v1:0": {
		"us": true,
	},
	"anthropic.claude-opus-4-20250514-v1:0": {
		"us": true,
	},
	"anthropic.claude-opus-4-1-20250805-v1:0": {
		"us": true,
	},
	"anthropic.claude-sonnet-4-5-20250929-v1:0": {
		"us": true,
	},
	"anthropic.claude-sonnet-4-6": {
		"us": true,
	},
	"anthropic.claude-opus-4-5-20251101-v1:0": {
		"us": true,
	},
	"anthropic.claude-opus-4-6-v1": {
		"us": true,
	},
	"anthropic.claude-opus-4-7": {
		"us": true,
	},
	"anthropic.claude-opus-4-8": {
		"us": true,
	},
	"anthropic.claude-haiku-4-5-20251001-v1:0": {
		"us": true,
	},
	"anthropic.claude-fable-5-1" : {
		"us": true,
	},
	"anthropic.claude-mythos-5-1" : {
		"us": true,
	},
	"anthropic.claude-opus-5" : {
		"us": true,
	},
	"anthropic.claude-sonnet-5" : {
		"us": true,
	},
	"anthropic.claude-mythos-5" : {
		"us": true,
	},
	"anthropic.claude-fable-5" : {
		"us": true,
	},
	"anthropic.claude-mythos-preview" : {
		"us": true,
	},
	// Nova models - all support three major regions
	"amazon.nova-micro-v1:0": {
		"us":   true,
		"eu":   true,
		"apac": true,
	},
	"amazon.nova-lite-v1:0": {
		"us":   true,
		"eu":   true,
		"apac": true,
	},
	"amazon.nova-pro-v1:0": {
		"us":   true,
		"eu":   true,
		"apac": true,
	},
	"amazon.nova-premier-v1:0": {
		"us": true,
	},
	"amazon.nova-canvas-v1:0": {
		"us":   true,
		"eu":   true,
		"apac": true,
	},
	"amazon.nova-reel-v1:0": {
		"us":   true,
		"eu":   true,
		"apac": true,
	},
	"amazon.nova-reel-v1:1": {
		"us": true,
	},
	"amazon.nova-sonic-v1:0": {
		"us":   true,
		"eu":   true,
		"apac": true,
	},
	// Nova 2.0 系列
	"amazon.nova-2-lite-v1:0": {
		"us": true,
	},
	"amazon.nova-2-lite-v1:0:256k": {
		"us": true,
	},
	"amazon.nova-2-sonic-v1:0": {
		"us": true,
	},
	"openai.gpt-5.6-luna": {
		"us":	true,
	},
	"openai.gpt-5.6-sol": {
		"us":	true,
	},
	"openai.gpt-5.6-terra": {
		"us":	true,
	},
	"openai.gpt-6-astra": {
		"us":	true,
	},
	"meta.llama3-1-8b-instruct-v1:0": {
		"us":   true,
	},
	"meta.llama3-1-70b-instruct-v1:0": {
		"us": true,
	},
	"meta.llama3-3-70b-instruct-v1:0": {
		"us": true,
	},
	"meta.llama3-1-405b-instruct-v1:0": {
		"us":   true,
	},
	"meta.llama4-maverick-17b-instruct-v1:0": {
		"us":   true,
	},
	"meta.llama4-scout-17b-instruct-v1:0": {
		"us":   true,
	},
}

var awsRegionCrossModelPrefixMap = map[string]string{
	"us": "us",
	"eu": "eu",
	"ap": "apac",
}

var ChannelName = "aws"

// 判断是否为Nova模型
func isNovaModel(modelId string) bool {
	return strings.Contains(modelId, "nova-")
}

func isConverseModel(modelId string) bool {
	return strings.HasPrefix(modelId, "google.gemma") ||
		strings.HasPrefix(modelId, "openai.gpt-") ||
		strings.HasPrefix(modelId, "meta.llama") ||
		strings.HasPrefix(modelId, "writer.palmyra") ||
		strings.HasPrefix(modelId, "zai.glm")
}

// isMantleModel 判断是否为仅支持 bedrock-mantle 端点的模型。
// 注意：必须在 isConverseModel 之前判断，因为 google.gemma-4 也满足 google.gemma 前缀。
func isMantleModel(modelId string) bool {
	// Gemma 4 系列
	if strings.HasPrefix(modelId, "google.gemma-4") {
		return true
	}
	// MiniMax 系列
	if strings.HasPrefix(modelId, "minimax.minimax-m") {
		return true
	}
	// Mistral AI 系列
	if strings.HasPrefix(modelId, "mistral.") {
		return true
	}
	// NVIDIA Nemotron 系列
	if strings.HasPrefix(modelId, "nvidia.nemotron") {
		return true
	}
	// OpenAI 模型中仅支持 mantle 的
	mantleOnlyOpenAIModels := []string{
		"openai.gpt-5.6-cyber",
		"openai.gpt-daybreak-blue-5.6-sol",
		"openai.gpt-5.5",
		"openai.gpt-5.4",
	}
	for _, m := range mantleOnlyOpenAIModels {
		if modelId == m {
			return true
		}
	}
	return false
}
