package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const pythonBackend = "http://localhost:8000"

func main() {
	initDB()
	initJWT()

	r := gin.Default()
	r.Use(corsMiddleware())

	r.POST("/api/register", handleRegister)
	r.POST("/api/login", handleLogin)

	auth := r.Group("/api")
	auth.Use(authRequired())
	{
		auth.GET("/models", handleModels)
		auth.POST("/chat", handleChat)
		auth.GET("/usage/stats", handleGetTokenStats)
		auth.GET("/usage/recent", handleGetRecentUsage)
		// Token 余额和购买
		auth.GET("/balance", handleGetBalance)
		auth.POST("/purchase", handlePurchase)
		auth.GET("/purchase/history", handleGetPurchaseHistory)
	}

	log.Println("Go backend listening on :8080")
	r.Run(":8080")
}

func handleModels(c *gin.Context) {
	resp, err := http.Get(pythonBackend + "/models")
	if err != nil {
		c.String(http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	c.Data(resp.StatusCode, "application/json", body)
}

func getUserID(c *gin.Context) int {
	userID, _ := c.Get("user_id")
	switch v := userID.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return 0
	}
}

func handleChat(c *gin.Context) {
	userID := getUserID(c)

	// 读取请求体以获取 model 信息
	bodyBytes, _ := io.ReadAll(c.Request.Body)

	var reqBody struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal(bodyBytes, &reqBody)

	// 估算需要的 token 数（输入字符数 / 4 + 预估输出 500）
	estimatedInput := 0
	for _, msg := range reqBody.Messages {
		estimatedInput += len(msg.Content) / 4
	}
	estimatedTokens := estimatedInput + 500

	// 检查余额
	balance, err := getUserTokenBalance(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "无法获取余额"})
		return
	}
	if balance < estimatedTokens {
		c.JSON(http.StatusPaymentRequired, gin.H{"error": "Token 余额不足，请购买更多", "balance": balance, "required": estimatedTokens})
		return
	}

	// 创建新的请求体给 Python 后端
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, pythonBackend+"/chat", bytes.NewReader(bodyBytes))
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.String(http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()

	// 收集响应内容以提取 token 使用情况
	var responseData []byte
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Stream(func(w io.Writer) bool {
		buf := make([]byte, 4096)
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			responseData = append(responseData, buf[:n]...)
		}
		return err == nil
	})

	// 异步保存 token 使用情况并扣除余额
	go saveUsageAndDeduct(userID, reqBody.Model, responseData)
}

func saveUsageAndDeduct(userID int, model string, data []byte) {
	// 解析 SSE 流中的 token 使用情况
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "data: {") && strings.Contains(line, "usage") {
			var usageData struct {
				Usage struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
					TotalTokens      int `json:"total_tokens"`
				} `json:"usage"`
			}
			jsonStr := strings.TrimPrefix(line, "data: ")
			if err := json.Unmarshal([]byte(jsonStr), &usageData); err == nil {
				// 保存使用记录
				saveChatUsage(userID, model, usageData.Usage.PromptTokens, usageData.Usage.CompletionTokens, usageData.Usage.TotalTokens)
				// 扣除 token 余额
				deductTokens(userID, usageData.Usage.TotalTokens)
			}
		}
	}
}

func handleGetBalance(c *gin.Context) {
	userID := getUserID(c)
	balance, err := getUserTokenBalance(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"balance": balance})
}

func handlePurchase(c *gin.Context) {
	userID := getUserID(c)

	var req struct {
		Package string `json:"package"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求"})
		return
	}

	// 定义套餐
	var amount int
	var price float64
	switch req.Package {
	case "small":
		amount = 10000
		price = 9.9
	case "medium":
		amount = 50000
		price = 39.9
	case "large":
		amount = 200000
		price = 129.9
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的套餐"})
		return
	}

	// 添加 token 到用户余额
	if err := addTokens(userID, amount); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "购买失败"})
		return
	}

	// 记录购买
	if err := createPurchaseRecord(userID, amount, price); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "记录购买失败"})
		return
	}

	// 获取最新余额
	balance, _ := getUserTokenBalance(userID)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"amount":  amount,
		"balance": balance,
		"message": fmt.Sprintf("成功购买 %d tokens！", amount),
	})
}

func handleGetPurchaseHistory(c *gin.Context) {
	userID := getUserID(c)
	records, err := getPurchaseHistory(userID, 10)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"records": records})
}

func handleGetTokenStats(c *gin.Context) {
	userID := getUserID(c)
	stats, err := getUserTokenStats(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, stats)
}

func handleGetRecentUsage(c *gin.Context) {
	userID := getUserID(c)
	limit := 10
	usages, err := getRecentUsage(userID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"usages": usages})
}
