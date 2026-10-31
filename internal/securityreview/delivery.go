package securityreview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/flatrun/agent/pkg/securityprofile"
)

type Delivery struct {
	URL          string
	AllowedHosts []string
	Credential   func() (string, error)
	Client       *http.Client
}

func (d Delivery) Validate() error {
	u, err := url.Parse(d.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !slices.Contains(d.AllowedHosts, u.Hostname()) || d.Credential == nil {
		return fmt.Errorf("off-host delivery requires an allowlisted HTTPS host and a protected credential reference")
	}
	return nil
}

func (s *Store) Deliver(ctx context.Context, d Delivery, r Report) error {
	if err := d.Validate(); err != nil {
		return err
	}
	credential, err := d.Credential()
	if err != nil || credential == "" {
		return fmt.Errorf("off-host delivery credential unavailable")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	if d.Client != nil {
		copy := *d.Client
		client = &copy
		client.Timeout = 10 * time.Second
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
	send := func(kind, id string, data json.RawMessage) error {
		body, err := json.Marshal(struct {
			Kind       string          `json:"kind"`
			Deployment string          `json:"deployment"`
			Run        string          `json:"run"`
			ID         string          `json:"id"`
			Data       json.RawMessage `json:"data"`
		}{kind, r.Deployment, r.ID, id, data})
		if err != nil {
			return err
		}
		if len(body) > 2<<20 {
			return fmt.Errorf("off-host delivery object exceeds budget")
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+credential)
		request.Header.Set("Idempotency-Key", securityprofile.Digest([]string{r.Deployment, r.ID, id}))
		response, err := client.Do(request)
		if err != nil {
			return fmt.Errorf("off-host delivery transport failed")
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("off-host delivery was not acknowledged")
		}
		return nil
	}
	for offset := 0; ; offset += 8 {
		rows, err := s.db.Query("SELECT id,body FROM evidence WHERE deployment=? AND run=? ORDER BY id LIMIT 8 OFFSET ?", r.Deployment, r.ID, offset)
		if err != nil {
			return err
		}
		type item struct {
			id   string
			body []byte
		}
		var batch []item
		for rows.Next() {
			var entry item
			if err = rows.Scan(&entry.id, &entry.body); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, entry)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, entry := range batch {
			verified, verifyErr := s.Evidence(r.Deployment, r.ID, entry.id)
			if verifyErr != nil {
				return verifyErr
			}
			if err = send("evidence", entry.id, verified); err != nil {
				return err
			}
		}
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err = send("report", securityprofile.Digest(r), data); err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE reports SET body=json_set(body,'$.delivery_status','delivered') WHERE deployment=? AND id=? AND json_extract(body,'$.hold')=? AND json_extract(body,'$.model_status')=?", r.Deployment, r.ID, r.Hold, r.ModelStatus)
	return err
}
func (s *Store) DeliveryPending() ([]Report, error) {
	rows, err := s.db.Query("SELECT body FROM reports WHERE json_extract(body,'$.delivery_status') IN ('pending','failed') AND json_extract(body,'$.model_status')!='pending' AND json_extract(body,'$.completed_at') IS NOT NULL ORDER BY COALESCE(json_extract(body,'$.delivery_attempted_at'),''),created LIMIT 2")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reports := []Report{}
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		var r Report
		if err = json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		reports = append(reports, r)
	}
	return reports, rows.Err()
}
func (s *Store) DeliveryFailed(dep, id string) error {
	_, err := s.db.Exec("UPDATE reports SET body=json_set(body,'$.delivery_status','failed','$.delivery_attempted_at',?) WHERE deployment=? AND id=?", time.Now().UTC().Format(time.RFC3339Nano), dep, id)
	return err
}
