package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"
)

type alert struct {
	Event        string    `json:"event"`
	Backup       string    `json:"backup"`
	LastValid    time.Time `json:"last_valid"`
	MinutesSince int       `json:"minutes_since"`
	Reason       string    `json:"reason,omitempty"`
	Text         string    `json:"text"`
}

var webhookClient = &http.Client{Timeout: 10 * time.Second}

// checkOverdue sends an overdue notice for every backup that just went past
// its alert threshold, and a recovery notice for every backup that came back.
// State only changes once the webhook accepts the notice, so a failed notice
// is retried on the next check.
func (a *app) checkOverdue(now time.Time) {
	for _, b := range a.config.Load().Backups {
		lastValid, notified := a.tracker.status(b.Name)
		overdue := now.Sub(lastValid) > b.alertAfter()
		if overdue == notified {
			continue
		}

		minutes := int(now.Sub(lastValid).Minutes())
		msg := alert{Event: "recovered", Backup: b.Name, LastValid: lastValid.UTC(), MinutesSince: minutes,
			Text: fmt.Sprintf("%s: valid dump received, backups are current again", b.Name)}
		if overdue {
			msg.Event = "overdue"
			msg.Text = fmt.Sprintf("%s: no valid dump in %d minutes", b.Name, minutes)
		}

		if err := notify(b.NotifyWebhook, msg); err != nil {
			log.Printf("%s: sending %s notice: %v", b.Name, msg.Event, err)
			continue
		}
		log.Printf("%s: sent %s notice", b.Name, msg.Event)
		a.tracker.setNotifiedOverdue(b.Name, overdue)
	}
}

func (a *app) alertInvalid(b *Backup, reason error) {
	lastValid, _ := a.tracker.status(b.Name)
	msg := alert{
		Event:        "invalid",
		Backup:       b.Name,
		LastValid:    lastValid.UTC(),
		MinutesSince: int(time.Since(lastValid).Minutes()),
		Reason:       reason.Error(),
		Text:         fmt.Sprintf("%s: dump rejected (%v)", b.Name, reason),
	}
	if err := notify(b.NotifyWebhook, msg); err != nil {
		log.Printf("%s: sending invalid notice: %v", b.Name, err)
	}
}

func notify(webhook string, msg alert) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	resp, err := webhookClient.Post(webhook, "application/json", bytes.NewReader(body))
	if err != nil {
		// A url.Error repeats the webhook URL, which usually contains a secret.
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			err = urlErr.Err
		}
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %s", resp.Status)
	}
	return nil
}
