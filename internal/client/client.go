// Package client talks to icefsd over its unix socket.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/sabbakix/icefs/internal/engine"
	"github.com/sabbakix/icefs/internal/store"
)

type Client struct{ hc *http.Client }

func New(socket string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}
	return &Client{hc: &http.Client{Transport: tr, Timeout: 30 * time.Second}}
}

func (c *Client) do(method, path string, body, out any) error {
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, "http://icefs"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("icefsd: %s", resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Status() ([]engine.ShareStatus, error) {
	var r struct{ Shares []engine.ShareStatus }
	err := c.do("GET", "/v1/status", nil, &r)
	return r.Shares, err
}

func (c *Client) Chains(share string) ([]engine.Chain, error) {
	var r struct{ Chains []engine.Chain }
	err := c.do("GET", "/v1/shares/"+url.PathEscape(share)+"/chains", nil, &r)
	return r.Chains, err
}

func (c *Client) DeleteChain(share, chain string) error {
	return c.do("POST", "/v1/shares/"+url.PathEscape(share)+"/chains/delete", map[string]any{"chain": chain}, nil)
}

func (c *Client) ExtendChain(share, chain string, days int) error {
	return c.do("POST", "/v1/shares/"+url.PathEscape(share)+"/chains/extend", map[string]any{"chain": chain, "days": days}, nil)
}

type ShareSettings struct {
	RetentionDays  int     `json:"retention_days"`
	SettleMinutes  int     `json:"settle_minutes"`
	MinFreePercent float64 `json:"min_free_percent"`
	AutoDelete     bool    `json:"auto_delete"`
}

func (c *Client) UpdateShare(share string, s ShareSettings) error {
	return c.do("PUT", "/v1/shares/"+url.PathEscape(share), s, nil)
}

func (c *Client) Events(limit int) ([]store.Event, error) {
	var r struct{ Events []store.Event }
	err := c.do("GET", fmt.Sprintf("/v1/events?limit=%d", limit), nil, &r)
	return r.Events, err
}
