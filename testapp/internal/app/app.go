// Package app is a small gin + GORM service whose only job is to produce
// realistic, varied logs for Argus: normal traffic, validation errors, 404s,
// slow requests, random failures and panics.
package app

import (
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Item is the demo table.
type Item struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:100;not null;index" json:"name"`
	Qty       int       `gorm:"not null;default:0" json:"qty"`
	CreatedAt time.Time `json:"created_at"`
}

// App holds the dependencies of the routes.
type App struct {
	DB  *gorm.DB
	Log *slog.Logger
}

// Migrate creates the schema. (This demo app uses AutoMigrate; Argus itself does not.)
func Migrate(db *gorm.DB) error { return db.AutoMigrate(&Item{}) }

// Router builds the gin engine.
func (a *App) Router() *gin.Engine {
	r := gin.New()
	r.Use(RequestID(), RequestLogger(a.Log), Recovery(a.Log))

	r.GET("/", a.index)
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	r.GET("/items", a.listItems)
	r.POST("/items", a.createItem)
	r.GET("/items/:id", a.getItem)
	r.DELETE("/items/:id", a.deleteItem)
	r.POST("/seed", a.seed)

	r.GET("/slow", a.slow)
	r.GET("/flaky", a.flaky)
	r.GET("/error", a.errorRoute)
	r.GET("/panic", func(*gin.Context) { panic("deliberate panic from /panic") })
	r.GET("/noise", a.noise)
	return r
}

func (a *App) index(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"app": "argus test app",
		"routes": []string{
			"GET /items?q=", "POST /items {name,qty}", "GET /items/:id", "DELETE /items/:id", "POST /seed?n=",
			"GET /slow?ms=", "GET /flaky?p=", "GET /error", "GET /panic", "GET /noise?n=", "GET /healthz",
		},
	})
}

func (a *App) listItems(c *gin.Context) {
	var items []Item
	tx := a.DB.WithContext(c.Request.Context()).Order("id desc").Limit(100)
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		tx = tx.Where("name LIKE ?", "%"+q+"%")
	}
	if err := tx.Find(&items).Error; err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"count": len(items), "items": items})
}

type itemInput struct {
	Name string `json:"name"`
	Qty  int    `json:"qty"`
}

func (a *App) createItem(c *gin.Context) {
	var in itemInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 || in.Qty < 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "name is required (max 100 chars) and qty must be >= 0"})
		return
	}
	item := Item{Name: in.Name, Qty: in.Qty}
	if err := a.DB.WithContext(c.Request.Context()).Create(&item).Error; err != nil {
		a.fail(c, err)
		return
	}
	a.Log.InfoContext(c.Request.Context(), "item created", "request_id", requestID(c), "item_id", item.ID, "qty", item.Qty)
	c.JSON(http.StatusCreated, item)
}

func (a *App) getItem(c *gin.Context) {
	var item Item
	err := a.DB.WithContext(c.Request.Context()).First(&item, "id = ?", c.Param("id")).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
	case err != nil:
		a.fail(c, err)
	default:
		c.JSON(http.StatusOK, item)
	}
}

func (a *App) deleteItem(c *gin.Context) {
	res := a.DB.WithContext(c.Request.Context()).Delete(&Item{}, "id = ?", c.Param("id"))
	switch {
	case res.Error != nil:
		a.fail(c, res.Error)
	case res.RowsAffected == 0:
		c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
	default:
		c.Status(http.StatusNoContent)
	}
}

var words = []string{"anvil", "bolt", "cable", "dial", "engine", "flange", "gasket", "hinge", "ingot", "jack", "knob", "lever"}

func (a *App) seed(c *gin.Context) {
	n := intParam(c, "n", 20, 1, 500)
	items := make([]Item, n)
	for i := range items {
		items[i] = Item{Name: fmt.Sprintf("%s-%d", words[rand.IntN(len(words))], rand.IntN(1000)), Qty: rand.IntN(100)}
	}
	if err := a.DB.WithContext(c.Request.Context()).CreateInBatches(items, 100).Error; err != nil {
		a.fail(c, err)
		return
	}
	a.Log.InfoContext(c.Request.Context(), "seeded items", "request_id", requestID(c), "count", n)
	c.JSON(http.StatusCreated, gin.H{"created": n})
}

// slow sleeps for ?ms= (default 800, max 5000) unless the client goes away first.
func (a *App) slow(c *gin.Context) {
	ms := intParam(c, "ms", 800, 0, 5000)
	select {
	case <-time.After(time.Duration(ms) * time.Millisecond):
	case <-c.Request.Context().Done():
		a.Log.WarnContext(c.Request.Context(), "client gave up on slow request", "request_id", requestID(c), "wanted_ms", ms)
		return
	}
	if ms >= 500 {
		a.Log.WarnContext(c.Request.Context(), "slow handler", "request_id", requestID(c), "slept_ms", ms)
	}
	c.JSON(http.StatusOK, gin.H{"slept_ms": ms})
}

// flaky fails with probability ?p= (default 0.3).
func (a *App) flaky(c *gin.Context) {
	p := 0.3
	if v, err := strconv.ParseFloat(c.Query("p"), 64); err == nil && v >= 0 && v <= 1 {
		p = v
	}
	if rand.Float64() < p {
		a.Log.ErrorContext(c.Request.Context(), "upstream dependency failed", "request_id", requestID(c), "dependency", "payments", "error", "connection reset by peer")
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream failed", "request_id": requestID(c)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (a *App) errorRoute(c *gin.Context) {
	a.Log.ErrorContext(c.Request.Context(), "unexpected failure", "request_id", requestID(c), "error", "simulated: could not reach the cache")
	c.JSON(http.StatusInternalServerError, gin.H{"error": "simulated failure", "request_id": requestID(c)})
}

var noiseMsgs = []struct {
	level slog.Level
	msg   string
}{
	{slog.LevelDebug, "cache lookup"},
	{slog.LevelInfo, "job finished"},
	{slog.LevelInfo, "user session refreshed"},
	{slog.LevelWarn, "retrying after timeout"},
	{slog.LevelWarn, "deprecated endpoint used"},
	{slog.LevelError, "job failed"},
}

// noise writes ?n= extra log lines at mixed levels: handy for watching live tail.
func (a *App) noise(c *gin.Context) {
	n := intParam(c, "n", 25, 1, 1000)
	for i := 0; i < n; i++ {
		m := noiseMsgs[rand.IntN(len(noiseMsgs))]
		a.Log.Log(c.Request.Context(), m.level, m.msg, "request_id", requestID(c), "job", rand.IntN(50), "took_ms", rand.IntN(300))
	}
	c.JSON(http.StatusOK, gin.H{"logged": n})
}

// fail logs an unexpected error and answers 500 without leaking details.
func (a *App) fail(c *gin.Context, err error) {
	a.Log.ErrorContext(c.Request.Context(), "database error", "request_id", requestID(c), "route", c.FullPath(), "error", err.Error())
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error", "request_id": requestID(c)})
}

func intParam(c *gin.Context, name string, def, min, max int) int {
	v, err := strconv.Atoi(c.Query(name))
	if err != nil {
		return def
	}
	return max2(min, min2(max, v))
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}
