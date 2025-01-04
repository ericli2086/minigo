package middlewares

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/bwmarrin/snowflake"
	"github.com/gin-gonic/gin"
)

func TraceIDMiddleware(args ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 初始化 Snowflake 节点
		node, err := snowflake.NewNode(getMachineID()) // 节点 ID (可以配置为唯一)
		if err != nil {
			log.Fatalf("Error initializing Snowflake Node: %v", err)
		}

		// 生成一个全局唯一的 trace_id
		traceID := node.Generate().String()

		// 将 trace_id 存储在请求上下文中，可以自行增加其他的便于调试的信息
		if len(args) > 0 {
			c.Set("trace_id", fmt.Sprintf("%s,%s", strings.Join(args, ""), traceID))
			c.Header("X-Trace-ID", fmt.Sprintf("%s,%s", strings.Join(args, ""), traceID))
		} else {
			c.Set("trace_id", traceID)
			c.Header("X-Trace-ID", traceID)
		}

		// 继续处理请求
		c.Next()
	}
}

func getMachineID() int64 {
	// 获取主机名
	hostname, err := os.Hostname()
	if err != nil {
		log.Fatalf("Error getting hostname: %v", err)
	}

	// 基于主机名生成一个唯一标识符，可以自主选择使用更复杂的哈希算法
	nodeID := int64(0)
	for i := 0; i < len(hostname); i++ {
		nodeID = (nodeID << 5) - nodeID + int64(hostname[i])
	}

	return nodeID % 1024
}
