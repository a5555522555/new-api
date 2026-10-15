package controller

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

func GetAllLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	username := c.Query("username")
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")
	logs, total, err := model.GetAllLogs(logType, startTimestamp, endTimestamp, modelName, username, tokenName, pageInfo.GetStartIdx(), pageInfo.GetPageSize(), channel, group, requestId, upstreamRequestId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(logs)
	common.ApiSuccess(c, pageInfo)
	return
}

func GetExport(c *gin.Context) {
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	username := c.Query("username")
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")

	logs, _, err := model.GetAllLogs(logType, startTimestamp, endTimestamp, modelName, username, tokenName, 0, 100000, channel, group, requestId, upstreamRequestId)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	tokenIds := make([]int, 0, len(logs))
	for _, l := range logs {
		if l.TokenId > 0 {
			tokenIds = append(tokenIds, l.TokenId)
		}
	}

	tokenStatusMap := make(map[int]string)
	tokenNameMap := make(map[int]string)
	if len(tokenIds) > 0 {
		var tokens []model.Token
		if err := model.DB.Select("id, status, name").Where("id IN ?", tokenIds).Find(&tokens).Error; err == nil {
			for _, t := range tokens {
				tokenStatusMap[t.Id] = tokenStatusToLabel(t.Status)
				tokenNameMap[t.Id] = t.Name
			}
		}
	}

	type groupKey struct {
		TokenId     int
		ModelName   string
		ChannelId   int
		Group       string
		BillingMode string
	}

	type groupRow struct {
		username    string
		tokenName   string
		tokenId     int
		modelName   string
		channelId   int
		channelName string
		group       string
		billingMode string
		earliest    int64
		latest      int64
		totalQuota  int
	}

	grouped := make(map[groupKey]*groupRow)
	for _, l := range logs {
		bm := parseBillingMode(l.Other)
		key := groupKey{
			TokenId:     l.TokenId,
			ModelName:   l.ModelName,
			ChannelId:   l.ChannelId,
			Group:       l.Group,
			BillingMode: bm,
		}
		endTs := l.CreatedAt + int64(l.UseTime)/1000
		if row, ok := grouped[key]; ok {
			if l.CreatedAt < row.earliest {
				row.earliest = l.CreatedAt
			}
			if endTs > row.latest {
				row.latest = endTs
			}
			row.totalQuota += l.Quota
		} else {
			tName := tokenNameMap[l.TokenId]
			if tName == "" {
				tName = l.TokenName
			}
			grouped[key] = &groupRow{
				username:    l.Username,
				tokenName:   tName,
				tokenId:     l.TokenId,
				modelName:   l.ModelName,
				channelId:   l.ChannelId,
				channelName: l.ChannelName,
				group:       l.Group,
				billingMode: bm,
				earliest:    l.CreatedAt,
				latest:      endTs,
				totalQuota:  l.Quota,
			}
		}
	}

	c.Writer.Header().Set("Content-Type", "text/csv; charset=utf-8")
	c.Writer.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"reconciliation_export_%s.csv\"", time.Now().Format("20060102_150405")))
	c.Writer.WriteString("\xEF\xBB\xBF")

	writer := csv.NewWriter(c.Writer)
	_ = writer.Write([]string{
		"开始时间", "结束时间", "用户名", "密钥名称", "密钥ID",
		"密钥当前状态", "模型", "渠道", "分组", "计费模式", "费用总计",
	})

	for _, row := range grouped {
		startTime := formatTimestamp(row.earliest)
		endTime := formatTimestamp(row.latest)
		tokenStatus := tokenStatusMap[row.tokenId]
		if tokenStatus == "" && row.tokenId > 0 {
			tokenStatus = "未知"
		}
		cost := fmt.Sprintf("%.6f", float64(row.totalQuota)/common.QuotaPerUnit)

		_ = writer.Write([]string{
			startTime, endTime, row.username, row.tokenName, strconv.Itoa(row.tokenId),
			tokenStatus, row.modelName, row.channelName, row.group, row.billingMode, cost,
		})
	}

	writer.Flush()
}

func GetSelfExport(c *gin.Context) {
	userId := c.GetInt("id")
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")

	logs, _, err := model.GetUserLogs(userId, logType, startTimestamp, endTimestamp, modelName, tokenName, 0, 100000, group, requestId, upstreamRequestId)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	tokenIds := make([]int, 0, len(logs))
	channelIds := make(map[int]struct{})
	for _, l := range logs {
		if l.TokenId > 0 {
			tokenIds = append(tokenIds, l.TokenId)
		}
		if l.ChannelId > 0 {
			channelIds[l.ChannelId] = struct{}{}
		}
	}

	tokenStatusMap := make(map[int]string)
	tokenNameMap := make(map[int]string)
	if len(tokenIds) > 0 {
		var tokens []model.Token
		if err := model.DB.Select("id, status, name").Where("id IN ?", tokenIds).Find(&tokens).Error; err == nil {
			for _, t := range tokens {
				tokenStatusMap[t.Id] = tokenStatusToLabel(t.Status)
				tokenNameMap[t.Id] = t.Name
			}
		}
	}

	channelNameMap := make(map[int]string)
	if len(channelIds) > 0 {
		ids := make([]int, 0, len(channelIds))
		for id := range channelIds {
			ids = append(ids, id)
		}
		var channels []struct {
			Id   int    `gorm:"column:id"`
			Name string `gorm:"column:name"`
		}
		if err := model.DB.Table("channels").Select("id, name").Where("id IN ?", ids).Find(&channels).Error; err == nil {
			for _, ch := range channels {
				channelNameMap[ch.Id] = ch.Name
			}
		}
	}

	type groupKey struct {
		TokenId     int
		ModelName   string
		ChannelId   int
		Group       string
		BillingMode string
	}

	type groupRow struct {
		username    string
		tokenName   string
		tokenId     int
		modelName   string
		channelId   int
		channelName string
		group       string
		billingMode string
		earliest    int64
		latest      int64
		totalQuota  int
	}

	grouped := make(map[groupKey]*groupRow)
	for _, l := range logs {
		bm := parseBillingMode(l.Other)
		key := groupKey{
			TokenId:     l.TokenId,
			ModelName:   l.ModelName,
			ChannelId:   l.ChannelId,
			Group:       l.Group,
			BillingMode: bm,
		}
		endTs := l.CreatedAt + int64(l.UseTime)/1000
		if row, ok := grouped[key]; ok {
			if l.CreatedAt < row.earliest {
				row.earliest = l.CreatedAt
			}
			if endTs > row.latest {
				row.latest = endTs
			}
			row.totalQuota += l.Quota
		} else {
			tName := tokenNameMap[l.TokenId]
			if tName == "" {
				tName = l.TokenName
			}
			grouped[key] = &groupRow{
				username:    l.Username,
				tokenName:   tName,
				tokenId:     l.TokenId,
				modelName:   l.ModelName,
				channelId:   l.ChannelId,
				channelName: channelNameMap[l.ChannelId],
				group:       l.Group,
				billingMode: bm,
				earliest:    l.CreatedAt,
				latest:      endTs,
				totalQuota:  l.Quota,
			}
		}
	}

	c.Writer.Header().Set("Content-Type", "text/csv; charset=utf-8")
	c.Writer.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"reconciliation_export_%s.csv\"", time.Now().Format("20060102_150405")))
	c.Writer.WriteString("\xEF\xBB\xBF")

	writer := csv.NewWriter(c.Writer)
	_ = writer.Write([]string{
		"开始时间", "结束时间", "用户名", "密钥名称", "密钥ID",
		"密钥当前状态", "模型", "渠道", "分组", "计费模式", "费用总计",
	})

	for _, row := range grouped {
		startTime := formatTimestamp(row.earliest)
		endTime := formatTimestamp(row.latest)
		tokenStatus := tokenStatusMap[row.tokenId]
		if tokenStatus == "" && row.tokenId > 0 {
			tokenStatus = "未知"
		}
		cost := fmt.Sprintf("%.6f", float64(row.totalQuota)/common.QuotaPerUnit)

		_ = writer.Write([]string{
			startTime, endTime, row.username, row.tokenName, strconv.Itoa(row.tokenId),
			tokenStatus, row.modelName, row.channelName, row.group, row.billingMode, cost,
		})
	}

	writer.Flush()
}

func tokenStatusToLabel(status int) string {
	switch status {
	case common.TokenStatusEnabled:
		return "启用"
	case common.TokenStatusDisabled:
		return "禁用"
	case common.TokenStatusExpired:
		return "已过期"
	case common.TokenStatusExhausted:
		return "已用尽"
	default:
		return "未知"
	}
}

func formatTimestamp(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
}

func formatEndTime(createdAt int64, useTimeMs int) string {
	if createdAt <= 0 {
		return ""
	}
	endTs := createdAt + int64(useTimeMs)/1000
	return time.Unix(endTs, 0).Format("2006-01-02 15:04:05")
}

func parseBillingMode(other string) string {
	if other == "" {
		return "按 Token"
	}
	var data map[string]interface{}
	if err := common.UnmarshalJsonStr(other, &data); err != nil {
		return "按 Token"
	}
	billingMode, _ := data["billing_mode"].(string)
	switch billingMode {
	case "tiered_expr":
		return "分级定价"
	default:
		return "按 Token"
	}
}

func GetUserLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	userId := c.GetInt("id")
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")
	logs, total, err := model.GetUserLogs(userId, logType, startTimestamp, endTimestamp, modelName, tokenName, pageInfo.GetStartIdx(), pageInfo.GetPageSize(), group, requestId, upstreamRequestId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(logs)
	common.ApiSuccess(c, pageInfo)
	return
}

// Deprecated: SearchAllLogs 已废弃，前端未使用该接口。
func SearchAllLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": "该接口已废弃",
	})
}

// Deprecated: SearchUserLogs 已废弃，前端未使用该接口。
func SearchUserLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": "该接口已废弃",
	})
}

func GetLogByKey(c *gin.Context) {
	tokenId := c.GetInt("token_id")
	if tokenId == 0 {
		c.JSON(200, gin.H{
			"success": false,
			"message": "无效的令牌",
		})
		return
	}
	logs, err := model.GetLogByTokenId(tokenId)
	if err != nil {
		c.JSON(200, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	c.JSON(200, gin.H{
		"success": true,
		"message": "",
		"data":    logs,
	})
}

func GetLogsStat(c *gin.Context) {
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	username := c.Query("username")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	stat, err := model.SumUsedQuota(logType, startTimestamp, endTimestamp, modelName, username, tokenName, channel, group)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	//tokenNum := model.SumUsedToken(logType, startTimestamp, endTimestamp, modelName, username, "")
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"quota": stat.Quota,
			"rpm":   stat.Rpm,
			"tpm":   stat.Tpm,
		},
	})
	return
}

func GetLogsSelfStat(c *gin.Context) {
	username := c.GetString("username")
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	quotaNum, err := model.SumUsedQuota(logType, startTimestamp, endTimestamp, modelName, username, tokenName, channel, group)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	//tokenNum := model.SumUsedToken(logType, startTimestamp, endTimestamp, modelName, username, tokenName)
	c.JSON(200, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"quota": quotaNum.Quota,
			"rpm":   quotaNum.Rpm,
			"tpm":   quotaNum.Tpm,
			//"token": tokenNum,
		},
	})
	return
}
