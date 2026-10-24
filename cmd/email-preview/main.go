package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/flatrun/agent/internal/notify"
)

func main() {
	output := flag.String("output", "email-previews", "directory for rendered HTML previews")
	flag.Parse()
	if err := os.MkdirAll(*output, 0755); err != nil {
		panic(err)
	}

	previews := map[string]notify.Notification{
		"positive.html": {
			Kind: notify.KindPositive, Title: "High CPU resolved",
			Message: "Resolved after 3 related events. High CPU is back to normal: shop-web in shop is 41.2%.",
			Panels:  []notify.Panel{{Title: "Incident ID", Value: "inc_4f2a91", Detail: "Source: observability"}},
		},
		"negative.html": {
			Kind: notify.KindNegative, Title: "Deployment failed",
			Message: "shop failed to start: container shop-web exited with code 1 during its health check.",
			Panels:  []notify.Panel{{Title: "Incident ID", Value: "inc_9b03c2", Detail: "Source: deployments"}},
		},
		"alert.html": alertSample(),
		"sign-in.html": {
			Title:   "Your sign-in code is 481962",
			Message: "Use this code to open shop.example.com. It works once.",
			Action:  &notify.Action{Label: "Sign in", URL: "https://shop.example.com/_flatrun/access/verify?token=eyJraW5kIjoidmVyaWZ5IiwiZW1haWwiOiJwZXJzb25AZXhhbXBsZS5jb20iLCJob3N0Ijoic2hvcC5leGFtcGxlLmNvbSJ9.y3Vp99EZ"},
			Panels:  []notify.Panel{{Title: "Sign-in code", Value: "481962", Detail: "Expires in 15 minutes"}},
		},
	}

	for name, notification := range previews {
		body, err := notify.RenderEmail(notification)
		if err != nil {
			panic(err)
		}
		path := filepath.Join(*output, name)
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			panic(err)
		}
		fmt.Println(path)
	}
}

func alertSample() notify.Notification {
	threshold := 80.0
	points := make([]float64, 120)
	for i := range points {
		points[i] = 38 + 14*math.Sin(float64(i)/9) + float64(i*i)/320
	}
	return notify.Notification{
		Kind: notify.KindWarning, Title: "High CPU",
		Message: "High CPU: shop-web in shop is 92.4%, above 80.0%.",
		Panels: []notify.Panel{
			{Title: "Now", Value: "92.4%", Detail: "Alerts when above 80.0%"},
			{Title: "Last hour", Detail: "The dashed line is the alert threshold.", Chart: &notify.Chart{Points: points, Threshold: &threshold}},
			{Title: "Top containers", Rows: []notify.PanelRow{
				{Label: "shop-web", Value: "61.2%", Share: 1},
				{Label: "shop-worker-1", Value: "18.7%", Share: 0.31},
				{Label: "shop-postgres", Value: "7.9%", Share: 0.13},
			}},
			{Title: "Incident ID", Value: "inc_4f2a91", Detail: "Source: observability"},
		},
	}
}
