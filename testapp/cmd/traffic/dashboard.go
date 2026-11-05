package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DashboardTitle is the dashboard SetupDashboard creates.
const DashboardTitle = "Test app"

type panelSpec struct {
	Title   string         `json:"title"`
	Type    string         `json:"type"`
	Query   string         `json:"query"`
	Step    string         `json:"step,omitempty"`
	Options map[string]any `json:"options"`
}

// Panels are written against the JSON lines the test app logs
// (msg "request", fields status/route/latency_ms/level) under {service="testapp"}.
var Panels = []panelSpec{
	{"Requests by status", "timeseries", `sum by (status) (count_over_time({service="testapp"} | json | msg="request" [1m]))`, "", map[string]any{"width": "half"}},
	{"Server errors by route", "timeseries", `sum by (route) (count_over_time({service="testapp"} | json | status>=500 [1m]))`, "", map[string]any{"width": "half"}},
	{"Busiest routes (15m)", "top_n", `topk(8, sum by (route) (count_over_time({service="testapp"} | json | msg="request" [15m])))`, "", map[string]any{"width": "half"}},
	{"5xx in the last 15m", "stat", `sum(count_over_time({service="testapp"} | json | status>=500 [15m]))`, "", map[string]any{"width": "half", "height": "s"}},
	{"Log volume by level", "timeseries", `sum by (level) (count_over_time({service="testapp"} | json [1m]))`, "", map[string]any{"width": "full"}},
	{"Errors and warnings", "logs", `{service="testapp"} | json | level=~"error|warn"`, "", map[string]any{"width": "full", "height": "l"}},
}

// SetupDashboard logs in to Argus and creates the demo dashboard unless one with
// the same title already exists. It returns the dashboard id.
func SetupDashboard(ctx context.Context, c *http.Client, base, email, password string) (id string, created bool, err error) {
	base = strings.TrimRight(base, "/") + "/api/v1"
	var login struct {
		AccessToken string `json:"access_token"`
	}
	if err := call(ctx, c, "POST", base+"/auth/login", "", map[string]string{"email": email, "password": password}, &login); err != nil {
		return "", false, fmt.Errorf("sign in: %w", err)
	}
	tok := login.AccessToken

	var existing []struct{ ID, Title string }
	if err := call(ctx, c, "GET", base+"/dashboards", tok, nil, &existing); err != nil {
		return "", false, err
	}
	for _, d := range existing {
		if d.Title == DashboardTitle {
			return d.ID, false, nil
		}
	}

	var d struct{ ID string }
	if err := call(ctx, c, "POST", base+"/dashboards", tok, map[string]any{"title": DashboardTitle, "description": "Logs from the local gin + GORM test app"}, &d); err != nil {
		return "", false, fmt.Errorf("create dashboard (the account needs the editor or admin role): %w", err)
	}
	for i, p := range Panels {
		body := map[string]any{"title": p.Title, "type": p.Type, "query": p.Query, "step": p.Step, "options": p.Options, "position": i}
		if err := call(ctx, c, "POST", base+"/dashboards/"+d.ID+"/panels", tok, body, nil); err != nil {
			return d.ID, true, fmt.Errorf("add panel %q: %w", p.Title, err)
		}
	}
	return d.ID, true, nil
}

func call(ctx context.Context, c *http.Client, method, url, token string, in, out any) error {
	var rd io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			return fmt.Errorf("%s (HTTP %d)", e.Error.Message, res.StatusCode)
		}
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
