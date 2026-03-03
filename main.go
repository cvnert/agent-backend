package main

import (
	"bytes"
	"encoding/json"
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
		Model string `json:"model"`
	}
	json.Unmarshal(bodyBytes, &reqBody)

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

	// 异步保存 token 使用情况（如果需要的话）
	go saveUsageFromResponse(userID, reqBody.Model, responseData)
}

func saveUsageFromResponse(userID int, model string, data []byte) {
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
				saveChatUsage(userID, model, usageData.Usage.PromptTokens, usageData.Usage.CompletionTokens, usageData.Usage.TotalTokens)
			}
		}
	}
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
