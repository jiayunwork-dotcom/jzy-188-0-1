// Command msagrr runs the Gauge R&R HTTP service with an embedded bbolt
// database on the mounted data volume.
package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"

	"msagrr/httpapi"
	"msagrr/service"
	"msagrr/store"
)

func main() {
	dataDir := os.Getenv("MSA_DATA_DIR")
	if dataDir == "" {
		dataDir = "/data"
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("create data dir %s: %v", dataDir, err)
	}
	dbPath := filepath.Join(dataDir, "msagrr.db")

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := service.New(st)

	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestLogger())
	httpapi.New(svc).Register(r)

	addr := os.Getenv("MSA_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("msagrr listening on %s, data file %s", addr, dbPath)
	if err := r.Run(addr); err != nil {
		log.Fatal(err)
	}
}

func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		log.Printf("%s %s -> %d", c.Request.Method, c.Request.URL.Path, c.Writer.Status())
	}
}
