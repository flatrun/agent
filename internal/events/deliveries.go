package events

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var ErrQueueFull = errors.New("event queue full")

type Delivery struct {
	ID            string            `json:"id"`
	Source        string            `json:"source"`
	Type          string            `json:"type"`
	Time          time.Time         `json:"time"`
	Outcome       string            `json:"outcome"`
	Subscriptions map[string]string `json:"subscriptions"`
}

func (s *Store) BeginDelivery(event Event, limit int) (*Delivery, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	record := &Delivery{ID: event.ID, Source: event.Source, Type: event.Type, Time: event.OccurredAt, Outcome: "processing", Subscriptions: map[string]string{}}
	var data []byte
	err = tx.QueryRow("SELECT record FROM deliveries WHERE source = ? AND id = ?", event.Source, event.ID).Scan(&data)
	if err == nil {
		var previous Delivery
		if err := json.Unmarshal(data, &previous); err != nil {
			return nil, false, err
		}
		if previous.Outcome != "failed" {
			return nil, true, nil
		}
		record.Subscriptions = previous.Subscriptions
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	if limit <= 0 {
		limit = 1000
	}
	if limit > 10000 {
		limit = 10000
	}
	if _, err := tx.Exec("DELETE FROM deliveries WHERE source = ? AND id = ?", event.Source, event.ID); err != nil {
		return nil, false, err
	}
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM deliveries").Scan(&count); err != nil {
		return nil, false, err
	}
	if count >= limit {
		result, err := tx.Exec("DELETE FROM deliveries WHERE sequence IN (SELECT sequence FROM deliveries WHERE outcome != 'processing' ORDER BY sequence LIMIT ?)", count-limit+1)
		if err != nil {
			return nil, false, err
		}
		removed, err := result.RowsAffected()
		if err != nil {
			return nil, false, err
		}
		if removed < int64(count-limit+1) {
			return nil, false, ErrQueueFull
		}
	}
	data, err = json.Marshal(record)
	if err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec("INSERT INTO deliveries (source, id, record, outcome) VALUES (?, ?, ?, ?)", record.Source, record.ID, data, record.Outcome); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return record, false, nil
}

func (s *Store) FinishDelivery(record Delivery) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE deliveries SET record = ?, outcome = ? WHERE source = ? AND id = ?", data, record.Outcome, record.Source, record.ID)
	return err
}

func (s *Store) ListDeliveries() ([]Delivery, error) {
	rows, err := s.db.Query("SELECT record FROM deliveries ORDER BY sequence")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []Delivery{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var record Delivery
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}
