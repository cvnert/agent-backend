package main

import (
	"io"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

const pythonBackend = "http://localhost:8000"

func main() {
	r := gin.Default()
	r.Use(cors())

	r.GET("/api/models", handleModels)
	r.POST("/api/chat", handleChat)

	log.Println("Go backend listening on :8080")
	r.Run(":8080")
}

func cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
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

func handleChat(c *gin.Context) {
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, pythonBackend+"/chat", c.Request.Body)
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

	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Stream(func(w io.Writer) bool {
		buf := make([]byte, 4096)
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
		}
		return err == nil
	})
}
